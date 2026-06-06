package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/apikey"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/dbtest"
)

// fakeStore is an in-memory credentialStore for the unit tests.
type fakeStore struct {
	byKeyID  map[string]apikey.APIKey
	getErr   error // when set, GetActiveByKeyID returns it (infra failure)
	touched  chan string
	touchErr error
}

func newFakeStore() *fakeStore { return &fakeStore{byKeyID: map[string]apikey.APIKey{}} }

// add registers an active credential whose stored hash matches secret, with the
// given persona and owner (created_by) so their propagation into context can be
// asserted.
func (f *fakeStore) add(id, keyID, secret string, persona, owner *string) {
	f.byKeyID[keyID] = apikey.APIKey{
		ID:         id,
		KeyID:      keyID,
		SecretHash: apikey.HashSecret(secret),
		Name:       "test key",
		Persona:    persona,
		CreatedBy:  owner,
	}
}

func (f *fakeStore) GetActiveByKeyID(_ context.Context, keyID string) (apikey.APIKey, error) {
	if f.getErr != nil {
		return apikey.APIKey{}, f.getErr
	}
	k, ok := f.byKeyID[keyID]
	if !ok {
		return apikey.APIKey{}, apikey.ErrNotFound
	}
	return k, nil
}

func (f *fakeStore) TouchLastUsed(_ context.Context, id string) error {
	if f.touched != nil {
		f.touched <- id
	}
	return f.touchErr
}

// probe records whether the guarded handler ran and what credential it saw.
func probe() (http.Handler, *bool, *Credential) {
	ran := new(bool)
	seen := new(Credential)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*ran = true
		if c, ok := CredentialFromContext(r.Context()); ok {
			*seen = c
		}
		w.WriteHeader(http.StatusOK)
	})
	return h, ran, seen
}

func req(keyID, secret string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/gateway/chat", nil)
	if keyID != "" {
		r.Header.Set(HeaderKeyID, keyID)
	}
	if secret != "" {
		r.Header.Set(HeaderSecret, secret)
	}
	return r
}

func TestRequireCredential_ValidPairPasses(t *testing.T) {
	store := newFakeStore()
	persona := "You are the DAB Hub assistant."
	owner := "admin-uuid-9"
	store.add("key-uuid-1", "dab_pk_hub", "dab_sk_secret", &persona, &owner)

	a := NewAuthenticator(store)
	var touchedID string
	a.touch = func(id string) { touchedID = id } // synchronous, for deterministic assertion

	h, ran, seen := probe()
	rr := httptest.NewRecorder()
	a.RequireCredential(h).ServeHTTP(rr, req("dab_pk_hub", "dab_sk_secret"))

	if rr.Code != http.StatusOK || !*ran {
		t.Fatalf("valid pair should reach handler; status=%d ran=%v", rr.Code, *ran)
	}
	if seen.ID != "key-uuid-1" || seen.KeyID != "dab_pk_hub" {
		t.Errorf("context credential = %+v, want id key-uuid-1 / dab_pk_hub", *seen)
	}
	if seen.Name != "test key" {
		t.Errorf("context name = %q, want %q", seen.Name, "test key")
	}
	if seen.Persona == nil || *seen.Persona != persona {
		t.Errorf("context persona = %v, want %q", seen.Persona, persona)
	}
	// The resolved owner/identity must match the key used (created_by -> OwnerID).
	if seen.OwnerID == nil || *seen.OwnerID != owner {
		t.Errorf("context owner = %v, want %q", seen.OwnerID, owner)
	}
	if touchedID != "key-uuid-1" {
		t.Errorf("last_used touch id = %q, want key-uuid-1", touchedID)
	}
}

func TestRequireCredential_RejectionsAre401AndDoNotTouch(t *testing.T) {
	store := newFakeStore()
	store.add("key-uuid-1", "dab_pk_hub", "dab_sk_secret", nil, nil)

	cases := map[string]struct{ keyID, secret string }{
		"missing key id": {"", "dab_sk_secret"},
		"missing secret": {"dab_pk_hub", ""},
		"both missing":   {"", ""},
		"unknown key id": {"dab_pk_unknown", "dab_sk_secret"},
		"wrong secret":   {"dab_pk_hub", "dab_sk_wrong"},
	}
	for name, c := range cases {
		a := NewAuthenticator(store)
		touched := false
		a.touch = func(string) { touched = true }

		h, ran, _ := probe()
		rr := httptest.NewRecorder()
		a.RequireCredential(h).ServeHTTP(rr, req(c.keyID, c.secret))

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rr.Code)
		}
		if *ran {
			t.Errorf("%s: handler must not run", name)
		}
		if touched {
			t.Errorf("%s: must not record usage on a rejected request", name)
		}
	}
}

// The 401 body must be identical whether the key id was unknown or the secret
// was wrong, so a caller cannot distinguish the two.
func TestRequireCredential_GenericBodyDoesNotLeakWhichHalfFailed(t *testing.T) {
	store := newFakeStore()
	store.add("key-uuid-1", "dab_pk_hub", "dab_sk_secret", nil, nil)
	a := NewAuthenticator(store)
	a.touch = func(string) {}

	body := func(keyID, secret string) string {
		h, _, _ := probe()
		rr := httptest.NewRecorder()
		a.RequireCredential(h).ServeHTTP(rr, req(keyID, secret))
		return rr.Body.String()
	}
	if got, want := body("dab_pk_unknown", "dab_sk_secret"), body("dab_pk_hub", "dab_sk_wrong"); got != want {
		t.Errorf("unknown-key body %q != wrong-secret body %q", got, want)
	}
}

// A store/infrastructure failure (e.g. the DB is down) must NOT masquerade as a
// 401: a caller with a valid credential would otherwise be told it is invalid.
func TestRequireCredential_StoreErrorIs503NotUnauthorized(t *testing.T) {
	store := newFakeStore()
	store.add("key-uuid-1", "dab_pk_hub", "dab_sk_secret", nil, nil)
	store.getErr = errors.New("connection refused")

	a := NewAuthenticator(store)
	touched := false
	a.touch = func(string) { touched = true }

	h, ran, _ := probe()
	rr := httptest.NewRecorder()
	a.RequireCredential(h).ServeHTTP(rr, req("dab_pk_hub", "dab_sk_secret"))

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("store failure status = %d, want 503", rr.Code)
	}
	if *ran {
		t.Error("handler must not run on a store failure")
	}
	if touched {
		t.Error("must not record usage on a store failure")
	}
}

func TestTouchLastUsedAsync_CallsStore(t *testing.T) {
	store := newFakeStore()
	store.touched = make(chan string, 1)
	a := NewAuthenticator(store)

	a.touchLastUsedAsync("the-id")
	select {
	case got := <-store.touched:
		if got != "the-id" {
			t.Errorf("touched id = %q, want the-id", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("async touch did not call the store")
	}
}

// TestRevokedCredentialRejectedOnNextRequest exercises the middleware against the
// real repository: a credential authenticates, then a CRUD-style revoke makes the
// very next request fail.
func TestRevokedCredentialRejectedOnNextRequest(t *testing.T) {
	pool := dbtest.Pool(t)
	dbtest.Truncate(t, pool, "api_keys", "users")
	repo := apikey.NewRepository(pool)
	ctx := context.Background()

	cred, err := apikey.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	created, err := repo.Create(ctx, apikey.CreateParams{KeyID: cred.KeyID, SecretHash: cred.SecretHash, Name: "hub"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	a := NewAuthenticator(repo)
	call := func() int {
		h, _, _ := probe()
		rr := httptest.NewRecorder()
		a.RequireCredential(h).ServeHTTP(rr, req(cred.KeyID, cred.Secret))
		return rr.Code
	}

	if code := call(); code != http.StatusOK {
		t.Fatalf("active credential should authenticate, got %d", code)
	}
	if err := repo.Revoke(ctx, created.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if code := call(); code != http.StatusUnauthorized {
		t.Errorf("revoked credential should be rejected, got %d", code)
	}
}
