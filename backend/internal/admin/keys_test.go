package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/apikey"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/token"
)

var fixedTime = time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

// memKeyStore is an in-memory keyStore so handler tests exercise real flows
// (create -> list -> update -> revoke) without a database.
type memKeyStore struct {
	seq  int
	rows map[string]apikey.APIKey
	// failNext, when set, makes the next call of that method return a non-
	// ErrNotFound error, to exercise the 500 paths.
	failCreate bool
}

func newMemStore() *memKeyStore { return &memKeyStore{rows: map[string]apikey.APIKey{}} }

func (s *memKeyStore) Create(_ context.Context, p apikey.CreateParams) (apikey.APIKey, error) {
	if s.failCreate {
		return apikey.APIKey{}, context.DeadlineExceeded
	}
	s.seq++
	k := apikey.APIKey{
		ID:         "id-" + strconv.Itoa(s.seq),
		KeyID:      p.KeyID,
		SecretHash: p.SecretHash,
		Name:       p.Name,
		Persona:    p.Persona,
		CreatedBy:  p.CreatedBy,
		CreatedAt:  fixedTime,
	}
	s.rows[k.ID] = k
	return k, nil
}

func (s *memKeyStore) GetByID(_ context.Context, id string) (apikey.Metadata, error) {
	k, ok := s.rows[id]
	if !ok {
		return apikey.Metadata{}, apikey.ErrNotFound
	}
	return meta(k), nil
}

func (s *memKeyStore) List(_ context.Context) ([]apikey.Metadata, error) {
	out := make([]apikey.Metadata, 0, len(s.rows))
	for _, k := range s.rows {
		out = append(out, meta(k))
	}
	return out, nil
}

func (s *memKeyStore) Update(_ context.Context, id string, p apikey.UpdateParams) (apikey.Metadata, error) {
	k, ok := s.rows[id]
	if !ok {
		return apikey.Metadata{}, apikey.ErrNotFound
	}
	k.Name = p.Name
	k.Persona = p.Persona
	s.rows[id] = k
	return meta(k), nil
}

func (s *memKeyStore) Revoke(_ context.Context, id string) error {
	k, ok := s.rows[id]
	if !ok || k.RevokedAt != nil {
		return apikey.ErrNotFound
	}
	rt := fixedTime
	k.RevokedAt = &rt
	s.rows[id] = k
	return nil
}

func meta(k apikey.APIKey) apikey.Metadata {
	return apikey.Metadata{
		ID: k.ID, KeyID: k.KeyID, Name: k.Name, Persona: k.Persona,
		CreatedBy: k.CreatedBy, CreatedAt: k.CreatedAt, RevokedAt: k.RevokedAt, LastUsedAt: k.LastUsedAt,
	}
}

// keysServer wires the handler behind the real auth middleware on a real mux, so
// tests drive requests through routing + the guard end to end.
func keysServer(t *testing.T, store keyStore) (http.Handler, string) {
	t.Helper()
	iss := token.NewIssuer("keys-test-secret", time.Hour)
	authn := NewAuthenticator(iss)
	mux := http.NewServeMux()
	RegisterKeyRoutes(mux, authn, NewKeysHandler(store))
	tok, err := iss.Issue("admin-1", true, time.Now())
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return mux, tok
}

func do(h http.Handler, method, target, body, tok string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if tok != "" {
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: tok})
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}

func decodeBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body %q: %v", rr.Body.String(), err)
	}
	return m
}

func TestCreate_ReturnsSecretOnceWithPersona(t *testing.T) {
	store := newMemStore()
	h, tok := keysServer(t, store)

	rr := do(h, http.MethodPost, "/api/admin/keys", `{"name":"DAB Hub","persona":"You are helpful."}`, tok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (body %s)", rr.Code, rr.Body)
	}
	body := decodeBody(t, rr)
	id, _ := body["id"].(string)
	if id == "" {
		t.Error("create response missing id")
	}
	// created_by is wired from the authenticated admin's token subject for the
	// audit trail; it is deliberately not in the JSON response, so verify the
	// linkage by inspecting the stored row.
	if stored := store.rows[id]; stored.CreatedBy == nil || *stored.CreatedBy != "admin-1" {
		t.Errorf("stored created_by = %v, want admin-1 (the token subject)", stored.CreatedBy)
	}
	if kid, _ := body["key_id"].(string); !strings.HasPrefix(kid, "dab_pk_") {
		t.Errorf("key_id = %v, want dab_pk_ prefix", body["key_id"])
	}
	if sec, _ := body["secret"].(string); !strings.HasPrefix(sec, "dab_sk_") {
		t.Errorf("secret = %v, want dab_sk_ prefix", body["secret"])
	}
	if body["persona"] != "You are helpful." {
		t.Errorf("persona = %v, want the submitted persona", body["persona"])
	}

	// The secret is shown exactly once: the subsequent list never carries it.
	rr = do(h, http.MethodGet, "/api/admin/keys", "", tok)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "dab_sk_") || strings.Contains(rr.Body.String(), "secret") {
		t.Errorf("list must not include the secret: %s", rr.Body)
	}
}

func TestCreate_ValidationAndErrors(t *testing.T) {
	store := newMemStore()
	h, tok := keysServer(t, store)

	for name, body := range map[string]string{
		"empty body":    `{}`,
		"blank name":    `{"name":"   "}`,
		"unknown field": `{"name":"x","nope":1}`,
		"not json":      `not-json`,
	} {
		rr := do(h, http.MethodPost, "/api/admin/keys", body, tok)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rr.Code)
		}
	}

	// A store failure surfaces as a 500, not a partial success.
	store.failCreate = true
	rr := do(h, http.MethodPost, "/api/admin/keys", `{"name":"x"}`, tok)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("store failure status = %d, want 500", rr.Code)
	}
}

func TestList_OmitsSecretsAndShowsPersona(t *testing.T) {
	store := newMemStore()
	h, tok := keysServer(t, store)
	do(h, http.MethodPost, "/api/admin/keys", `{"name":"k1","persona":"p1"}`, tok)

	rr := do(h, http.MethodGet, "/api/admin/keys", "", tok)
	body := decodeBody(t, rr)
	keys, ok := body["keys"].([]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("keys = %v, want one entry", body["keys"])
	}
	entry := keys[0].(map[string]any)
	if entry["persona"] != "p1" {
		t.Errorf("list persona = %v, want p1", entry["persona"])
	}
	if _, leaked := entry["secret"]; leaked {
		t.Error("list entry leaked a secret field")
	}
	if _, leaked := entry["secret_hash"]; leaked {
		t.Error("list entry leaked a secret_hash field")
	}
}

func TestUpdate_PartialEditAndClearPersona(t *testing.T) {
	store := newMemStore()
	h, tok := keysServer(t, store)
	created := decodeBody(t, do(h, http.MethodPost, "/api/admin/keys", `{"name":"orig","persona":"orig-p"}`, tok))
	id := created["id"].(string)

	// Persona-only edit leaves the name unchanged.
	rr := do(h, http.MethodPatch, "/api/admin/keys/"+id, `{"persona":"new-p"}`, tok)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	got := decodeBody(t, rr)
	if got["persona"] != "new-p" || got["name"] != "orig" {
		t.Errorf("after persona edit: name=%v persona=%v, want orig/new-p", got["name"], got["persona"])
	}

	// Name-only edit leaves the persona unchanged.
	got = decodeBody(t, do(h, http.MethodPatch, "/api/admin/keys/"+id, `{"name":"renamed"}`, tok))
	if got["name"] != "renamed" || got["persona"] != "new-p" {
		t.Errorf("after name edit: name=%v persona=%v, want renamed/new-p", got["name"], got["persona"])
	}

	// An empty persona clears it to null.
	got = decodeBody(t, do(h, http.MethodPatch, "/api/admin/keys/"+id, `{"persona":""}`, tok))
	if got["persona"] != nil {
		t.Errorf("cleared persona = %v, want null", got["persona"])
	}
}

func TestUpdate_BlankNameAndNotFound(t *testing.T) {
	store := newMemStore()
	h, tok := keysServer(t, store)
	created := decodeBody(t, do(h, http.MethodPost, "/api/admin/keys", `{"name":"orig"}`, tok))
	id := created["id"].(string)

	if rr := do(h, http.MethodPatch, "/api/admin/keys/"+id, `{"name":"  "}`, tok); rr.Code != http.StatusBadRequest {
		t.Errorf("blank-name patch status = %d, want 400", rr.Code)
	}
	if rr := do(h, http.MethodPatch, "/api/admin/keys/missing", `{"name":"x"}`, tok); rr.Code != http.StatusNotFound {
		t.Errorf("patch missing status = %d, want 404", rr.Code)
	}
}

func TestRevoke_RemovesFromActiveAndIsIdempotentlyNotFound(t *testing.T) {
	store := newMemStore()
	h, tok := keysServer(t, store)
	created := decodeBody(t, do(h, http.MethodPost, "/api/admin/keys", `{"name":"k"}`, tok))
	id := created["id"].(string)

	if rr := do(h, http.MethodDelete, "/api/admin/keys/"+id, "", tok); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204", rr.Code)
	}

	// Still listed, now marked revoked.
	keys := decodeBody(t, do(h, http.MethodGet, "/api/admin/keys", "", tok))["keys"].([]any)
	if entry := keys[0].(map[string]any); entry["revoked_at"] == nil {
		t.Error("revoked key should report revoked_at in the list")
	}

	// Re-revoking and revoking an unknown id both 404.
	if rr := do(h, http.MethodDelete, "/api/admin/keys/"+id, "", tok); rr.Code != http.StatusNotFound {
		t.Errorf("re-revoke status = %d, want 404", rr.Code)
	}
	if rr := do(h, http.MethodDelete, "/api/admin/keys/missing", "", tok); rr.Code != http.StatusNotFound {
		t.Errorf("revoke missing status = %d, want 404", rr.Code)
	}
}

func TestAllRoutesRequireAdminToken(t *testing.T) {
	h, _ := keysServer(t, newMemStore())
	cases := []struct {
		method, target, body string
	}{
		{http.MethodPost, "/api/admin/keys", `{"name":"x"}`},
		{http.MethodGet, "/api/admin/keys", ""},
		{http.MethodPatch, "/api/admin/keys/some-id", `{"name":"x"}`},
		{http.MethodDelete, "/api/admin/keys/some-id", ""},
	}
	for _, c := range cases {
		rr := do(h, c.method, c.target, c.body, "") // no token
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without token = %d, want 401", c.method, c.target, rr.Code)
		}
	}
}
