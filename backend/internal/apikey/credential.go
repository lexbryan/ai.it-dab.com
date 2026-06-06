package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// Credential prefixes. The public key id and the secret carry distinct prefixes
// so a leaked value is immediately recognizable (and greppable) as a DAB key and
// the two halves are never confused.
const (
	// KeyIDPrefix marks the public key id (safe to store/show plainly).
	KeyIDPrefix = "dab_pk_"
	// SecretPrefix marks the secret (shown once, then only stored hashed).
	SecretPrefix = "dab_sk_"
)

// Random byte counts behind each token (before base64 encoding): a 128-bit
// public id is collision-safe, and a 256-bit secret is infeasible to guess or
// brute-force, which is what lets HashSecret use a fast hash (see the package
// doc on secret hashing).
const (
	keyIDBytes  = 16
	secretBytes = 32
)

// Credential is a freshly minted key pair. Secret is the plaintext shown to the
// caller exactly once; SecretHash is what gets persisted (Secret is never
// stored). Hand SecretHash to Repository.Create.
type Credential struct {
	KeyID      string
	Secret     string
	SecretHash string
}

// Generate mints a new credential: a public key id and a high-entropy secret,
// each from crypto/rand, plus the hash of the secret to store.
func Generate() (Credential, error) {
	keyID, err := randToken(KeyIDPrefix, keyIDBytes)
	if err != nil {
		return Credential{}, err
	}
	secret, err := randToken(SecretPrefix, secretBytes)
	if err != nil {
		return Credential{}, err
	}
	return Credential{KeyID: keyID, Secret: secret, SecretHash: HashSecret(secret)}, nil
}

// randToken returns prefix followed by n bytes of crypto/rand, base64url-encoded
// (no padding, so the token is safe in URLs and headers).
func randToken(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("apikey: generating token: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// HashSecret returns the hex-encoded SHA-256 digest of secret for storage. A
// fast hash is correct here because the secret is high-entropy; see the package
// doc for why bcrypt is deliberately not used.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// VerifySecret reports whether secret matches storedHash (as produced by
// HashSecret). The comparison is constant-time so a timing side channel cannot
// reveal how much of the hash matched; a malformed storedHash returns false
// rather than panicking.
func VerifySecret(secret, storedHash string) bool {
	want, err := hex.DecodeString(storedHash)
	if err != nil {
		return false
	}
	got := sha256.Sum256([]byte(secret))
	return subtle.ConstantTimeCompare(got[:], want) == 1
}

// dummyHash is a fixed, valid hash used by DummyVerify.
var dummyHash = HashSecret("dab.api-key.timing-equalizer")

// DummyVerify runs a verification against a fixed hash and discards the result.
// The gateway calls it when a key id is unknown so the response spends the same
// hashing work as the known-key path, leaving no timing signal for whether a key
// id exists.
func DummyVerify(secret string) {
	_ = VerifySecret(secret, dummyHash)
}
