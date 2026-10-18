package attendant

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/douglasdf99/litezap/internal/media"
)

// Vault encrypts the provider keys at rest with AES-256-GCM. The key lives in a file of
// the data dir (data/attendant_key, 0600, created on first use); the instance id is the
// additional data, so a ciphertext copied to another instance does not open.
type Vault struct {
	aead cipher.AEAD
	pkey []byte // HMAC key of pseudonyms, derived from the vault key
}

const sealedPrefix = "v1:"

// LoadVault opens (creating if needed) the key file at path.
func LoadVault(path string) (*Vault, error) {
	key, err := media.LoadOrCreateKey(path)
	if err != nil {
		return nil, err
	}
	return NewVault(key)
}

// NewVault uses a 32-byte key.
func NewVault(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, errors.New("attendant key must have 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	pk := sha256.Sum256(append([]byte("litezap-attendant-pseudonym:"), key...))
	return &Vault{aead: aead, pkey: pk[:]}, nil
}

// Seal encrypts plaintext for instanceID ("" for empty plaintext).
func (v *Vault) Seal(instanceID string, plaintext []byte) (string, error) {
	if len(plaintext) == 0 {
		return "", nil
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := v.aead.Seal(nonce, nonce, plaintext, []byte(instanceID))
	return sealedPrefix + base64.RawStdEncoding.EncodeToString(out), nil
}

// Open decrypts a Seal result of the same instance.
func (v *Vault) Open(instanceID, sealed string) ([]byte, error) {
	if sealed == "" {
		return nil, nil
	}
	raw, ok := strings.CutPrefix(sealed, sealedPrefix)
	if !ok {
		return nil, errors.New("unknown secret format")
	}
	b, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil || len(b) < v.aead.NonceSize() {
		return nil, errors.New("corrupt secret")
	}
	n := v.aead.NonceSize()
	pt, err := v.aead.Open(nil, b[:n], b[n:], []byte(instanceID))
	if err != nil {
		return nil, errors.New("secret does not open (wrong key file or instance)")
	}
	return pt, nil
}

// Pseudonym is a stable, non-reversible id of a chat (HMAC with a key derived from the
// vault key), sent to providers as the end-user id instead of a phone number.
func (v *Vault) Pseudonym(instanceID, chat string) string {
	m := hmac.New(sha256.New, v.pkey)
	m.Write([]byte(instanceID + "|" + chat))
	return "lz_" + hex.EncodeToString(m.Sum(nil))[:24]
}
