// Package token issues and verifies the admin session JWT. It is the single
// place HS256 signing/verification with JWT_SECRET happens, shared by the login
// endpoint (issue) and the admin auth middleware (verify).
//
// This is the ADMIN UI session token, distinct from the project two-key gateway
// credentials.
package token

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// DefaultTTL is the session lifetime used when none is configured.
const DefaultTTL = 24 * time.Hour

// signingMethod is the only accepted algorithm; tokens using anything else
// (including "none") are rejected on parse.
var signingMethod = jwt.SigningMethodHS256

// ErrInvalidToken is returned for any verification failure (bad signature,
// expiry, wrong algorithm, malformed input). It deliberately carries no detail
// so callers cannot leak why a token was rejected.
var ErrInvalidToken = errors.New("token: invalid")

// Claims is the validated, decoded session identity.
type Claims struct {
	UserID      string
	IsSuperuser bool
}

// appClaims is the on-the-wire claim set: sub holds the user id, plus a custom
// is_superuser flag and the standard iat/exp.
type appClaims struct {
	IsSuperuser bool `json:"is_superuser"`
	jwt.RegisteredClaims
}

// Issuer signs and verifies session tokens with a fixed secret and TTL.
type Issuer struct {
	secret []byte
	ttl    time.Duration
}

// NewIssuer returns an Issuer using secret (the loaded JWT_SECRET) and ttl
// (DefaultTTL when ttl <= 0).
func NewIssuer(secret string, ttl time.Duration) *Issuer {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Issuer{secret: []byte(secret), ttl: ttl}
}

// TTL is the configured session lifetime (handy for cookie max-age).
func (i *Issuer) TTL() time.Duration { return i.ttl }

// Issue signs a token for the user. now is the issued-at time (a parameter so
// tests are deterministic); expiry is now+TTL.
func (i *Issuer) Issue(userID string, isSuperuser bool, now time.Time) (string, error) {
	claims := appClaims{
		IsSuperuser: isSuperuser,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(i.ttl)),
		},
	}
	signed, err := jwt.NewWithClaims(signingMethod, claims).SignedString(i.secret)
	if err != nil {
		return "", fmt.Errorf("token: signing: %w", err)
	}
	return signed, nil
}

// Parse verifies the token's signature, algorithm, and expiry (as of now) and
// returns its claims. Any failure returns ErrInvalidToken with no detail.
func (i *Issuer) Parse(tokenString string, now time.Time) (Claims, error) {
	var claims appClaims
	_, err := jwt.ParseWithClaims(tokenString, &claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != signingMethod.Alg() {
			return nil, fmt.Errorf("unexpected signing method %q", t.Method.Alg())
		}
		return i.secret, nil
	},
		jwt.WithValidMethods([]string{signingMethod.Alg()}),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	if claims.Subject == "" {
		return Claims{}, ErrInvalidToken
	}
	return Claims{UserID: claims.Subject, IsSuperuser: claims.IsSuperuser}, nil
}
