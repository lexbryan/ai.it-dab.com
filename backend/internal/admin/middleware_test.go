package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/token"
)

func authTestSetup(t *testing.T) (*Authenticator, *token.Issuer) {
	t.Helper()
	iss := token.NewIssuer("mw-secret", time.Hour)
	return NewAuthenticator(iss), iss
}

// probe records whether the wrapped handler ran and what claims it saw.
func probe() (http.Handler, *bool, *token.Claims) {
	ran := new(bool)
	seen := new(token.Claims)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*ran = true
		if c, ok := ClaimsFromContext(r.Context()); ok {
			*seen = c
		}
		w.WriteHeader(http.StatusOK)
	})
	return h, ran, seen
}

func withCookie(tok string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/admin/keys", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: tok})
	return r
}

func TestRequireAdmin_ValidTokenPasses(t *testing.T) {
	a, iss := authTestSetup(t)
	tok, _ := iss.Issue("user-7", false, time.Now())
	h, ran, seen := probe()

	rr := httptest.NewRecorder()
	a.RequireAdmin(h).ServeHTTP(rr, withCookie(tok))

	if rr.Code != http.StatusOK || !*ran {
		t.Fatalf("valid token should reach handler; status=%d ran=%v", rr.Code, *ran)
	}
	if seen.UserID != "user-7" {
		t.Errorf("claims in context = %+v, want user-7", *seen)
	}
}

func TestRequireAdmin_BearerHeaderPasses(t *testing.T) {
	a, iss := authTestSetup(t)
	tok, _ := iss.Issue("user-8", false, time.Now())
	h, ran, _ := probe()

	r := httptest.NewRequest(http.MethodGet, "/api/admin/keys", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	a.RequireAdmin(h).ServeHTTP(rr, r)

	if rr.Code != http.StatusOK || !*ran {
		t.Errorf("bearer token should reach handler; status=%d", rr.Code)
	}
}

func TestRequireAdmin_RejectsBadTokens(t *testing.T) {
	a, iss := authTestSetup(t)
	expired, _ := iss.Issue("u", false, time.Now().Add(-2*time.Hour))
	wrongSig, _ := token.NewIssuer("other-secret", time.Hour).Issue("u", false, time.Now())

	cases := map[string]*http.Request{
		"missing":   httptest.NewRequest(http.MethodGet, "/api/admin/keys", nil),
		"malformed": withCookie("not-a-jwt"),
		"expired":   withCookie(expired),
		"wrong-sig": withCookie(wrongSig),
	}
	for name, req := range cases {
		h, ran, _ := probe()
		rr := httptest.NewRecorder()
		a.RequireAdmin(h).ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rr.Code)
		}
		if *ran {
			t.Errorf("%s: handler must not run", name)
		}
	}
}

func TestRequireSuperuser(t *testing.T) {
	a, iss := authTestSetup(t)

	// Non-superuser → 403, handler not reached.
	nonSuper, _ := iss.Issue("user-9", false, time.Now())
	h, ran, _ := probe()
	rr := httptest.NewRecorder()
	a.RequireSuperuser(h).ServeHTTP(rr, withCookie(nonSuper))
	if rr.Code != http.StatusForbidden || *ran {
		t.Errorf("non-superuser: status=%d ran=%v, want 403 and no handler", rr.Code, *ran)
	}

	// Superuser → passes.
	super, _ := iss.Issue("admin-1", true, time.Now())
	h2, ran2, seen := probe()
	rr2 := httptest.NewRecorder()
	a.RequireSuperuser(h2).ServeHTTP(rr2, withCookie(super))
	if rr2.Code != http.StatusOK || !*ran2 || !seen.IsSuperuser {
		t.Errorf("superuser should pass: status=%d ran=%v claims=%+v", rr2.Code, *ran2, *seen)
	}
}
