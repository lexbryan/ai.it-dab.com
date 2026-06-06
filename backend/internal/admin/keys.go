package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/apikey"
)

// keyStore is the slice of the api-key repository the CRUD handlers need.
// *apikey.Repository satisfies it.
type keyStore interface {
	Create(ctx context.Context, p apikey.CreateParams) (apikey.APIKey, error)
	GetByID(ctx context.Context, id string) (apikey.Metadata, error)
	List(ctx context.Context) ([]apikey.Metadata, error)
	Update(ctx context.Context, id string, p apikey.UpdateParams) (apikey.Metadata, error)
	Revoke(ctx context.Context, id string) error
}

// KeysHandler serves the admin API-key CRUD endpoints. Mount its routes with
// RegisterKeyRoutes, which puts every route behind the admin auth guard.
type KeysHandler struct {
	keys keyStore
}

// NewKeysHandler builds the handler over the given key store.
func NewKeysHandler(keys keyStore) *KeysHandler { return &KeysHandler{keys: keys} }

// RouteRegistrar is the route-registration seam, satisfied by *httpserver.Router
// and *http.ServeMux.
type RouteRegistrar interface {
	Handle(pattern string, handler http.Handler)
}

// RegisterKeyRoutes mounts the API-key CRUD routes on mux, each wrapped in the
// admin auth guard so an unauthenticated request never reaches a handler. The id
// routes use Go 1.22 path values ({id}). Mirrors httpserver.RegisterHealth.
func RegisterKeyRoutes(mux RouteRegistrar, authn *Authenticator, h *KeysHandler) {
	guard := authn.RequireAdmin
	mux.Handle("POST /api/admin/keys", guard(http.HandlerFunc(h.Create)))
	mux.Handle("GET /api/admin/keys", guard(http.HandlerFunc(h.List)))
	mux.Handle("PATCH /api/admin/keys/{id}", guard(http.HandlerFunc(h.Update)))
	mux.Handle("DELETE /api/admin/keys/{id}", guard(http.HandlerFunc(h.Revoke)))
}

type createKeyRequest struct {
	Name    string  `json:"name"`
	Persona *string `json:"persona"`
}

// keyResponse is the create response. It is the ONLY place the plaintext secret
// is ever returned — it cannot be retrieved again after this.
type keyResponse struct {
	ID        string    `json:"id"`
	KeyID     string    `json:"key_id"`
	Secret    string    `json:"secret"`
	Name      string    `json:"name"`
	Persona   *string   `json:"persona"`
	CreatedAt time.Time `json:"created_at"`
}

// keyMetadataResponse is the non-secret view returned by list/update. It has no
// secret or hash field, so neither can leak through those paths.
type keyMetadataResponse struct {
	ID         string     `json:"id"`
	KeyID      string     `json:"key_id"`
	Name       string     `json:"name"`
	Persona    *string    `json:"persona"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// Create generates a new credential, stores only the secret hash, and returns
// the plaintext secret exactly once.
func (h *KeysHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createKeyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}

	cred, err := apikey.Generate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not generate credential")
		return
	}
	created, err := h.keys.Create(r.Context(), apikey.CreateParams{
		KeyID:      cred.KeyID,
		SecretHash: cred.SecretHash,
		Name:       name,
		Persona:    normalizePersona(req.Persona),
		CreatedBy:  creatorID(r.Context()),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create key")
		return
	}

	writeJSON(w, http.StatusCreated, keyResponse{
		ID:        created.ID,
		KeyID:     created.KeyID,
		Secret:    cred.Secret,
		Name:      created.Name,
		Persona:   created.Persona,
		CreatedAt: created.CreatedAt,
	})
}

// List returns metadata for every credential (active and revoked); never the
// secret or its hash.
func (h *KeysHandler) List(w http.ResponseWriter, r *http.Request) {
	keys, err := h.keys.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not list keys")
		return
	}
	out := make([]keyMetadataResponse, 0, len(keys))
	for _, k := range keys {
		out = append(out, toMetadataResponse(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

type updateKeyRequest struct {
	Name    *string `json:"name"`
	Persona *string `json:"persona"`
}

// Update edits a credential's label and/or persona without rotating the secret.
// Fields omitted from the body are left unchanged; an empty persona clears it.
func (h *KeysHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateKeyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "name cannot be empty")
		return
	}

	current, err := h.keys.GetByID(r.Context(), id)
	if err != nil {
		h.writeStoreError(w, err, "could not load key")
		return
	}

	name := current.Name
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	persona := current.Persona
	if req.Persona != nil {
		persona = normalizePersona(req.Persona)
	}

	updated, err := h.keys.Update(r.Context(), id, apikey.UpdateParams{Name: name, Persona: persona})
	if err != nil {
		h.writeStoreError(w, err, "could not update key")
		return
	}
	writeJSON(w, http.StatusOK, toMetadataResponse(updated))
}

// Revoke soft-revokes a credential so it immediately stops authenticating.
func (h *KeysHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.keys.Revoke(r.Context(), id); err != nil {
		h.writeStoreError(w, err, "could not revoke key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeStoreError maps a repository error to its HTTP response: a missing key is
// a 404, anything else a generic 500.
func (h *KeysHandler) writeStoreError(w http.ResponseWriter, err error, internalMsg string) {
	if errors.Is(err, apikey.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "api key not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", internalMsg)
}

func toMetadataResponse(m apikey.Metadata) keyMetadataResponse {
	return keyMetadataResponse{
		ID:         m.ID,
		KeyID:      m.KeyID,
		Name:       m.Name,
		Persona:    m.Persona,
		CreatedAt:  m.CreatedAt,
		RevokedAt:  m.RevokedAt,
		LastUsedAt: m.LastUsedAt,
	}
}

// normalizePersona treats a nil or blank persona as "no persona" (NULL), so an
// empty string clears it; otherwise the value is kept verbatim.
func normalizePersona(p *string) *string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return nil
	}
	return p
}

// creatorID is the authenticated admin's user id (from the auth middleware), for
// the api_keys.created_by audit column; nil if somehow unauthenticated.
func creatorID(ctx context.Context) *string {
	if c, ok := ClaimsFromContext(ctx); ok && c.UserID != "" {
		id := c.UserID
		return &id
	}
	return nil
}

// decodeJSON strictly decodes the (size-capped) request body into dst, writing a
// 400 and returning false on any problem.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is not valid JSON")
		return false
	}
	return true
}
