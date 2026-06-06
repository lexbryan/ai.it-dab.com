package apikey

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerate_ShapeAndUniqueness(t *testing.T) {
	a, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	b, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if !strings.HasPrefix(a.KeyID, KeyIDPrefix) {
		t.Errorf("key id %q lacks prefix %q", a.KeyID, KeyIDPrefix)
	}
	if !strings.HasPrefix(a.Secret, SecretPrefix) {
		t.Errorf("secret %q lacks prefix %q", a.Secret, SecretPrefix)
	}
	if a.SecretHash == a.Secret {
		t.Error("secret hash must not equal the plaintext secret")
	}
	if a.SecretHash == "" || a.SecretHash == HashSecret("") {
		t.Error("secret hash should be the hash of the generated secret")
	}

	// Two generations must not collide on either half.
	if a.KeyID == b.KeyID {
		t.Error("two generated key ids collided")
	}
	if a.Secret == b.Secret {
		t.Error("two generated secrets collided")
	}
}

func TestGenerate_SecretHas256BitsOfEntropy(t *testing.T) {
	c, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(c.Secret, SecretPrefix))
	if err != nil {
		t.Fatalf("secret body is not base64url: %v", err)
	}
	if len(raw) < 32 {
		t.Errorf("secret entropy = %d bytes, want >= 32 (256 bits)", len(raw))
	}
}

func TestHashSecret_VerifyRoundTrip(t *testing.T) {
	const secret = "dab_sk_some-high-entropy-token"
	hash := HashSecret(secret)

	if hash == secret {
		t.Fatal("hash must differ from the plaintext")
	}
	if len(hash) != 64 { // hex-encoded SHA-256
		t.Errorf("hash length = %d, want 64 hex chars", len(hash))
	}
	if HashSecret(secret) != hash {
		t.Error("HashSecret must be deterministic")
	}
	if !VerifySecret(secret, hash) {
		t.Error("VerifySecret should accept the correct secret")
	}
	if VerifySecret("dab_sk_wrong", hash) {
		t.Error("VerifySecret should reject a wrong secret")
	}
}

func TestVerifySecret_MalformedHashIsFalseNotPanic(t *testing.T) {
	for _, bad := range []string{"", "not-hex", "zz", "abc"} {
		if VerifySecret("dab_sk_whatever", bad) {
			t.Errorf("VerifySecret(_, %q) = true, want false", bad)
		}
	}
}
