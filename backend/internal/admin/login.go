package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/auth"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/token"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/user"
)

// SessionCookieName is the cookie carrying the admin session JWT.
//
// Cookie attributes (set on login): HttpOnly (JS can never read the token),
// Secure when the gateway runs in production (HTTPS), SameSite=Lax, and an
// expiry matching the token TTL. Lax works for the intended topology — the
// frontend and gateway share a site (different ports in dev, same registrable
// domain in prod), so the cookie rides cross-origin credentialed requests that
// the CORS layer allows. If the frontend is deployed on a different registrable
// domain, switch to SameSite=None (which requires Secure/HTTPS).
const SessionCookieName = "dab_admin_session"

// userGetter is the slice of the user repository the login handler needs.
type userGetter interface {
	GetByEmail(ctx context.Context, email string) (user.User, error)
}

// LoginHandler authenticates an admin by email+password and, on success, sets
// the session cookie.
type LoginHandler struct {
	users  userGetter
	issuer *token.Issuer
	secure bool
	now    func() time.Time
}

// NewLoginHandler builds the handler. secureCookies should be true in
// production so the session cookie is only sent over HTTPS.
func NewLoginHandler(users userGetter, issuer *token.Issuer, secureCookies bool) *LoginHandler {
	return &LoginHandler{users: users, issuer: issuer, secure: secureCookies, now: time.Now}
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *LoginHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "email and password are required")
		return
	}

	u, err := h.users.GetByEmail(r.Context(), req.Email)
	switch {
	case errors.Is(err, user.ErrNotFound):
		// Spend the same time as a real verification so the response does not
		// reveal whether the account exists, then fail generically.
		auth.DummyVerify(req.Password)
		h.unauthorized(w)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal_error", "login failed")
		return
	}

	ok, err := auth.VerifyPassword(req.Password, u.PasswordHash)
	if err != nil || !ok {
		h.unauthorized(w)
		return
	}

	now := h.now()
	tok, err := h.issuer.Issue(u.ID, u.IsSuperuser, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not issue session")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  now.Add(h.issuer.TTL()),
		MaxAge:   int(h.issuer.TTL().Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"email":        u.Email,
		"is_superuser": u.IsSuperuser,
	})
}

// unauthorized writes the single generic 401 used for both an unknown email and
// a wrong password, so neither response reveals which was wrong.
func (h *LoginHandler) unauthorized(w http.ResponseWriter) {
	writeError(w, http.StatusUnauthorized, "unauthorized", "invalid email or password")
}
