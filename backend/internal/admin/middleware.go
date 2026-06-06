package admin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/token"
)

type ctxKey int

const claimsKey ctxKey = iota

// Authenticator guards admin routes by validating the session JWT. It is used
// only on the admin router group; the public LLM gateway uses the two-key
// middleware instead.
type Authenticator struct {
	issuer *token.Issuer
	now    func() time.Time
}

// NewAuthenticator builds an Authenticator that verifies tokens with issuer.
func NewAuthenticator(issuer *token.Issuer) *Authenticator {
	return &Authenticator{issuer: issuer, now: time.Now}
}

// RequireAdmin returns middleware that requires a valid, unexpired session
// token (from the session cookie, or an "Authorization: Bearer" header). It
// attaches the resolved claims to the request context and answers any failure
// with a detail-free 401.
func (a *Authenticator) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := a.authenticate(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		next.ServeHTTP(w, r.WithContext(withClaims(r.Context(), claims)))
	})
}

// RequireSuperuser is RequireAdmin plus a superuser check: a valid non-superuser
// token is rejected with 403.
func (a *Authenticator) RequireSuperuser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := a.authenticate(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if !claims.IsSuperuser {
			writeError(w, http.StatusForbidden, "forbidden", "superuser privileges required")
			return
		}
		next.ServeHTTP(w, r.WithContext(withClaims(r.Context(), claims)))
	})
}

// authenticate extracts and verifies the token, returning the claims on success.
func (a *Authenticator) authenticate(r *http.Request) (token.Claims, bool) {
	raw := tokenFromRequest(r)
	if raw == "" {
		return token.Claims{}, false
	}
	claims, err := a.issuer.Parse(raw, a.now())
	if err != nil {
		return token.Claims{}, false
	}
	return claims, true
}

// tokenFromRequest reads the session token from the cookie, falling back to an
// "Authorization: Bearer <token>" header (both match the login ticket).
func tokenFromRequest(r *http.Request) string {
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	if h := r.Header.Get("Authorization"); h != "" {
		if after, found := strings.CutPrefix(h, "Bearer "); found {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

func withClaims(ctx context.Context, claims token.Claims) context.Context {
	return context.WithValue(ctx, claimsKey, claims)
}

// ClaimsFromContext returns the authenticated admin's claims, set by the auth
// middleware. ok is false when no authenticated claims are present.
func ClaimsFromContext(ctx context.Context) (token.Claims, bool) {
	claims, ok := ctx.Value(claimsKey).(token.Claims)
	return claims, ok
}
