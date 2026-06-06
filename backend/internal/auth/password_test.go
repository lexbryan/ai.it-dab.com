package auth

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestHashAndVerify_RoundTrip(t *testing.T) {
	const pw = "correct horse battery"
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == pw {
		t.Fatal("hash must not equal the plaintext")
	}
	ok, err := VerifyPassword(pw, hash)
	if err != nil || !ok {
		t.Fatalf("VerifyPassword(correct) = %v, %v; want true, nil", ok, err)
	}
}

func TestVerify_WrongPassword(t *testing.T) {
	hash, err := HashPassword("the right password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := VerifyPassword("the wrong password", hash)
	if err != nil {
		t.Fatalf("VerifyPassword returned an error for a plain mismatch: %v", err)
	}
	if ok {
		t.Error("wrong password verified true")
	}
}

func TestHash_DifferentEachCall(t *testing.T) {
	const pw = "same input password"
	h1, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Error("two hashes of the same password should differ (random salt)")
	}
	// Both must still verify.
	for _, h := range []string{h1, h2} {
		if ok, _ := VerifyPassword(pw, h); !ok {
			t.Error("a freshly produced hash failed to verify")
		}
	}
}

func TestVerify_MalformedHash(t *testing.T) {
	ok, err := VerifyPassword("whatever", "not-a-bcrypt-hash")
	if err == nil {
		t.Error("a malformed hash should return an error")
	}
	if ok {
		t.Error("a malformed hash must not verify true")
	}
}

func TestHash_RejectsShortAndEmpty(t *testing.T) {
	for _, pw := range []string{"", "short"} {
		if _, err := HashPassword(pw); !errors.Is(err, ErrPasswordTooShort) {
			t.Errorf("HashPassword(%q) error = %v, want ErrPasswordTooShort", pw, err)
		}
	}
}

func TestHash_RejectsTooLong(t *testing.T) {
	long := strings.Repeat("a", maxPasswordBytes+1)
	if _, err := HashPassword(long); !errors.Is(err, ErrPasswordTooLong) {
		t.Errorf("HashPassword(73 bytes) error = %v, want ErrPasswordTooLong", err)
	}
}

func TestVerify_EmptyAgainstValidHash(t *testing.T) {
	hash, err := HashPassword("a real password")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := VerifyPassword("", hash); ok {
		t.Error("empty password must not verify against a real hash")
	}
}

// TestCost_EncodedInHash confirms the configured cost lands in the hash, so the
// work factor can be raised later without a schema change.
func TestCost_EncodedInHash(t *testing.T) {
	hash, err := HashPassword("another password")
	if err != nil {
		t.Fatal(err)
	}
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatalf("bcrypt.Cost: %v", err)
	}
	if cost != Cost {
		t.Errorf("encoded cost = %d, want %d", cost, Cost)
	}
}
