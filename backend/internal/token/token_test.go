package token

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIssueAndParse_RoundTrip(t *testing.T) {
	iss := NewIssuer("a-strong-secret", time.Hour)
	now := time.Unix(1_700_000_000, 0)

	tok, err := iss.Issue("user-123", true, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims, err := iss.Parse(tok, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.UserID != "user-123" || !claims.IsSuperuser {
		t.Errorf("claims = %+v, want user-123/superuser", claims)
	}
}

func TestParse_WrongSecretRejected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok, _ := NewIssuer("right-secret", time.Hour).Issue("u", false, now)
	if _, err := NewIssuer("wrong-secret", time.Hour).Parse(tok, now); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("Parse with wrong secret err = %v, want ErrInvalidToken", err)
	}
}

func TestParse_ExpiredRejected(t *testing.T) {
	iss := NewIssuer("secret", time.Hour)
	now := time.Unix(1_700_000_000, 0)
	tok, _ := iss.Issue("u", false, now)
	if _, err := iss.Parse(tok, now.Add(2*time.Hour)); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("Parse of expired token err = %v, want ErrInvalidToken", err)
	}
}

func TestParse_TamperedRejected(t *testing.T) {
	iss := NewIssuer("secret", time.Hour)
	now := time.Unix(1_700_000_000, 0)
	tok, _ := iss.Issue("u", false, now)
	// Flip a character in the signature segment.
	parts := strings.Split(tok, ".")
	parts[2] = "AAAA" + parts[2][4:]
	if _, err := iss.Parse(strings.Join(parts, "."), now); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("Parse of tampered token err = %v, want ErrInvalidToken", err)
	}
}

func TestParse_AlgNoneRejected(t *testing.T) {
	// A hand-crafted alg=none token must not be accepted.
	// header {"alg":"none","typ":"JWT"} . payload {"sub":"u"} . (empty sig)
	const noneToken = "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiJ1In0."
	if _, err := NewIssuer("secret", time.Hour).Parse(noneToken, time.Now()); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("alg=none token err = %v, want ErrInvalidToken", err)
	}
}

func TestParse_GarbageRejected(t *testing.T) {
	if _, err := NewIssuer("secret", time.Hour).Parse("not.a.jwt", time.Now()); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("garbage token err = %v, want ErrInvalidToken", err)
	}
}

func TestNewIssuer_DefaultsTTL(t *testing.T) {
	if got := NewIssuer("s", 0).TTL(); got != DefaultTTL {
		t.Errorf("TTL = %v, want default %v", got, DefaultTTL)
	}
}
