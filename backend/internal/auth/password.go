// Package auth holds the gateway's authentication primitives. This file is the
// password hashing helper used by the superuser CLI, the admin login endpoint,
// and any password-set flow.
//
// # Algorithm
//
// Passwords are hashed with bcrypt (golang.org/x/crypto/bcrypt). bcrypt is
// adaptive (the cost is encoded in the hash, so the work factor can be raised
// later without a schema change), bundles a per-hash random salt, and ships a
// constant-time verifier. Verification always uses that library compare —
// derived values are never compared with ==. bcrypt's input is limited to 72
// bytes; longer inputs are rejected rather than silently truncated.
package auth

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLength is the minimum accepted password length in bytes.
const MinPasswordLength = 8

// maxPasswordBytes is bcrypt's hard input limit.
const maxPasswordBytes = 72

// Cost is the bcrypt work factor. It is a variable so tests can lower it; it
// defaults to bcrypt.DefaultCost (10).
var Cost = bcrypt.DefaultCost

var (
	// ErrPasswordTooShort is returned when a password is shorter than
	// MinPasswordLength (this also covers the empty password).
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d bytes", MinPasswordLength)
	// ErrPasswordTooLong is returned when a password exceeds bcrypt's 72-byte
	// input limit.
	ErrPasswordTooLong = errors.New("password must be at most 72 bytes")
)

// HashPassword returns a bcrypt-encoded hash of plaintext (algorithm, cost, salt
// and digest in one string) suitable for storage. It rejects empty/short and
// over-long passwords.
func HashPassword(plaintext string) (string, error) {
	if len(plaintext) < MinPasswordLength {
		return "", ErrPasswordTooShort
	}
	if len(plaintext) > maxPasswordBytes {
		return "", ErrPasswordTooLong
	}
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), Cost)
	if err != nil {
		return "", fmt.Errorf("auth: hashing password: %w", err)
	}
	return string(h), nil
}

// VerifyPassword reports whether plaintext matches the bcrypt-encoded hash,
// using bcrypt's constant-time comparison. A correct password returns
// (true, nil); an incorrect one returns (false, nil); a malformed or
// unparseable hash returns (false, error) — never a panic.
func VerifyPassword(plaintext, encodedHash string) (bool, error) {
	err := bcrypt.CompareHashAndPassword([]byte(encodedHash), []byte(plaintext))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, bcrypt.ErrMismatchedHashAndPassword):
		return false, nil
	default:
		return false, fmt.Errorf("auth: verifying password: %w", err)
	}
}
