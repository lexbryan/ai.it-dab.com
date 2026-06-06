package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/auth"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/token"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/user"
)

type fakeUsers struct{ byEmail map[string]user.User }

func (f fakeUsers) GetByEmail(_ context.Context, email string) (user.User, error) {
	u, ok := f.byEmail[strings.ToLower(email)]
	if !ok {
		return user.User{}, user.ErrNotFound
	}
	return u, nil
}

func testHandler(t *testing.T, secure bool) (*LoginHandler, *token.Issuer, user.User) {
	t.Helper()
	hash, err := auth.HashPassword("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	u := user.User{ID: "user-1", Email: "admin@example.com", PasswordHash: hash, IsSuperuser: true}
	users := fakeUsers{byEmail: map[string]user.User{"admin@example.com": u}}
	iss := token.NewIssuer("test-secret", time.Hour)
	return NewLoginHandler(users, iss, secure), iss, u
}

func post(h http.Handler, body string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	return rr
}

func sessionCookie(rr *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == SessionCookieName {
			return c
		}
	}
	return nil
}

func TestLogin_Success(t *testing.T) {
	h, iss, u := testHandler(t, false)
	rr := post(h, `{"email":"admin@example.com","password":"correct-password"}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	c := sessionCookie(rr)
	if c == nil {
		t.Fatal("no session cookie set")
	}
	if !c.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	claims, err := iss.Parse(c.Value, time.Now())
	if err != nil {
		t.Fatalf("cookie token does not verify: %v", err)
	}
	if claims.UserID != u.ID || !claims.IsSuperuser {
		t.Errorf("token claims = %+v, want %s/superuser", claims, u.ID)
	}
}

func TestLogin_SecureCookieInProd(t *testing.T) {
	h, _, _ := testHandler(t, true)
	rr := post(h, `{"email":"admin@example.com","password":"correct-password"}`)
	c := sessionCookie(rr)
	if c == nil || !c.Secure {
		t.Errorf("production session cookie must be Secure: %+v", c)
	}
}

func TestLogin_WrongPasswordAndUnknownEmailAreIdentical(t *testing.T) {
	h, _, _ := testHandler(t, false)

	wrong := post(h, `{"email":"admin@example.com","password":"nope"}`)
	unknown := post(h, `{"email":"ghost@example.com","password":"whatever-123"}`)

	for name, rr := range map[string]*httptest.ResponseRecorder{"wrong-password": wrong, "unknown-email": unknown} {
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rr.Code)
		}
		if sessionCookie(rr) != nil {
			t.Errorf("%s: must not set a session cookie", name)
		}
	}
	if wrong.Body.String() != unknown.Body.String() {
		t.Errorf("401 bodies differ (user enumeration): %q vs %q", wrong.Body.String(), unknown.Body.String())
	}
}

func TestLogin_MalformedAndMissing(t *testing.T) {
	h, _, _ := testHandler(t, false)
	for _, body := range []string{`not json`, `{"email":"admin@example.com"}`, `{}`} {
		rr := post(h, body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rr.Code)
		}
	}
}
