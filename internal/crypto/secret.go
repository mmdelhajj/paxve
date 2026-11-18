// Package crypto encrypts secrets stored in the database (provider tokens, router passwords)
// with AES-256-GCM. Sealed values look like "enc:v1:<base64(nonce|ciphertext)>"; the "v1" is the
// key version, so a key can be rotated later by adding v2 and re-sealing.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

const (
	prefix  = "enc:"
	version = "v1"
	aad     = "paxve-secret"
)

var ErrBadSecret = errors.New("crypto: cannot decrypt secret (wrong APP_SECRET_KEY or damaged value)")

// Box seals and opens secrets with one 32-byte key.
type Box struct {
	aead cipher.AEAD
}

// NewBox accepts the key as 64 hex characters or base64 (standard or URL, padded or not) of 32 bytes.
func NewBox(key string) (*Box, error) {
	key = strings.TrimSpace(key)
	var raw []byte
	if b, err := hex.DecodeString(key); err == nil && len(b) == 32 {
		raw = b
	} else {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			if b, err := enc.DecodeString(key); err == nil && len(b) == 32 {
				raw = b
				break
			}
		}
	}
	if raw == nil {
		return nil, errors.New("crypto: APP_SECRET_KEY must be 32 bytes (64 hex characters or base64)")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// IsSealed reports whether s was produced by Seal.
func IsSealed(s string) bool { return strings.HasPrefix(s, prefix) }

// Seal encrypts plain. Empty strings stay empty; already sealed values are returned unchanged.
func (b *Box) Seal(plain string) (string, error) {
	if plain == "" || IsSealed(plain) {
		return plain, nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := b.aead.Seal(nonce, nonce, []byte(plain), []byte(aad))
	return prefix + version + ":" + base64.RawStdEncoding.EncodeToString(ct), nil
}

// Open decrypts a sealed value. A value that is not sealed (legacy plaintext) is returned as is.
func (b *Box) Open(s string) (string, error) {
	if !IsSealed(s) {
		return s, nil
	}
	rest := strings.TrimPrefix(s, prefix)
	ver, data, ok := strings.Cut(rest, ":")
	if !ok || ver != version {
		return "", ErrBadSecret
	}
	raw, err := base64.RawStdEncoding.DecodeString(data)
	if err != nil || len(raw) < b.aead.NonceSize()+b.aead.Overhead() {
		return "", ErrBadSecret
	}
	n := b.aead.NonceSize()
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], []byte(aad))
	if err != nil {
		return "", ErrBadSecret
	}
	return string(plain), nil
}
