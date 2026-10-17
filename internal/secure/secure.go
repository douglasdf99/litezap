// Package secure generates identifiers and tokens and compares secrets safely.
package secure

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
)

// NewID returns prefix followed by 16 random hex characters.
func NewID(prefix string) string {
	b := make([]byte, 8)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// NewInstanceToken returns a new instance API token ("lzi_" + 32 random bytes).
func NewInstanceToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return "lzi_" + base64.RawURLEncoding.EncodeToString(b)
}

// HashToken returns the hex SHA-256 of a token; only hashes are stored.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Equal compares two secrets in constant time.
func Equal(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
