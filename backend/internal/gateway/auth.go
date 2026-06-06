package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/apikey"
)

// errInvalidCredential signals a genuine authentication failure (missing,
// unknown, revoked, or wrong credential) that maps to a generic 401. Any other
// error from the store is an infrastructure failure that maps to 503 — a valid
// caller must not be told their credentials are wrong because the database
// hiccuped.
var errInvalidCredential = errors.New("gateway: invalid credential")

// Two-key transport. A caller presents BOTH halves of its credential on every
// request as two headers:
//
//	X-DAB-Key-Id:  dab_pk_...   (the public key id)
//	X-DAB-Secret:  dab_sk_...   (the secret, revealed once at creation)
//
// Both are required. Either header missing/blank, an unknown or revoked key id,
// or a wrong secret all yield the SAME generic 401, so a caller cannot tell
// which half was wrong. The secret is never logged.
const (
	HeaderKeyID  = "X-DAB-Key-Id"
	HeaderSecret = "X-DAB-Secret"
)

type ctxKey int

const credentialKey ctxKey = iota

// Credential is the resolved calling project's credential, attached to the
// request context on successful auth. The gateway core injects Persona as the
// leading system message on every upstream call.
type Credential struct {
	ID      string  // api_keys.id
	KeyID   string  // public dab_pk_...
	Name    string  // human label
	Persona *string // per-key system prompt; nil when none is set
	OwnerID *string // created_by (the admin who created the key), if any
}

// credentialStore is the slice of the api-key repository the middleware needs.
// *apikey.Repository satisfies it.
type credentialStore interface {
	GetActiveByKeyID(ctx context.Context, keyID string) (apikey.APIKey, error)
	TouchLastUsed(ctx context.Context, id string) error
}

// Authenticator guards the public gateway endpoint with two-key credentials. It
// is distinct from internal/admin's session-JWT Authenticator and is used only
// on the gateway router group.
type Authenticator struct {
	store credentialStore
	// touch runs the best-effort last_used_at update. It is a field so tests can
	// observe it; production uses the detached goroutine set by NewAuthenticator.
	touch func(id string)
}

// NewAuthenticator builds an Authenticator over the given credential store.
func NewAuthenticator(store credentialStore) *Authenticator {
	a := &Authenticator{store: store}
	a.touch = a.touchLastUsedAsync
	return a
}

// RequireCredential returns middleware that requires a valid two-key credential.
// On success it records best-effort usage and attaches the resolved credential
// to the request context. A bad credential answers with an identical generic
// 401; a store/infrastructure failure answers 503 (so a valid caller is never
// told its credentials are wrong during a database outage).
func (a *Authenticator) RequireCredential(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred, err := a.authenticate(r)
		switch {
		case errors.Is(err, errInvalidCredential):
			writeError(w, errUnauthorized, "a valid api key id and secret are required")
			return
		case err != nil:
			writeError(w, errUnavailable, "could not verify credentials, try again")
			return
		}
		a.touch(cred.ID)
		next.ServeHTTP(w, r.WithContext(withCredential(r.Context(), cred)))
	})
}

// authenticate reads both headers, resolves the active credential by key id, and
// verifies the secret. It returns errInvalidCredential (no detail) for an
// authentication failure, or the underlying error for an infrastructure failure.
func (a *Authenticator) authenticate(r *http.Request) (Credential, error) {
	keyID := strings.TrimSpace(r.Header.Get(HeaderKeyID))
	secret := strings.TrimSpace(r.Header.Get(HeaderSecret))
	if keyID == "" || secret == "" {
		return Credential{}, errInvalidCredential
	}

	row, err := a.store.GetActiveByKeyID(r.Context(), keyID)
	switch {
	case errors.Is(err, apikey.ErrNotFound):
		// Unknown or revoked key id. Spend the same hashing work as a real
		// verification so timing does not reveal whether the key id existed.
		apikey.DummyVerify(secret)
		return Credential{}, errInvalidCredential
	case err != nil:
		// A real store failure (pool exhausted, connection reset, shutdown):
		// surface it so the caller answers 503, not a misleading 401.
		return Credential{}, err
	}
	if !apikey.VerifySecret(secret, row.SecretHash) {
		return Credential{}, errInvalidCredential
	}
	return Credential{
		ID:      row.ID,
		KeyID:   row.KeyID,
		Name:    row.Name,
		Persona: row.Persona,
		OwnerID: row.CreatedBy,
	}, nil
}

// touchLastUsedAsync updates last_used_at without blocking or failing the
// request: it runs detached from the request context (so it still completes
// after the response is sent) and ignores errors (usage bookkeeping is
// best-effort).
func (a *Authenticator) touchLastUsedAsync(id string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.store.TouchLastUsed(ctx, id)
	}()
}

func withCredential(ctx context.Context, c Credential) context.Context {
	return context.WithValue(ctx, credentialKey, c)
}

// CredentialFromContext returns the calling project's credential, set by
// RequireCredential. ok is false when no authenticated credential is present.
func CredentialFromContext(ctx context.Context) (Credential, bool) {
	c, ok := ctx.Value(credentialKey).(Credential)
	return c, ok
}
