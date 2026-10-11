// Package secrets seals API keys at rest with the instance secret.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// Box seals and opens values with AES-256-GCM.
type Box struct {
	aead cipher.AEAD
}

// GenerateKey returns a new random instance secret, base64-encoded.
func GenerateKey() string {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// NewBox takes a base64-encoded 32-byte secret, as made by GenerateKey.
func NewBox(secret string) (*Box, error) {
	k, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		return nil, fmt.Errorf("secrets: GMESH_SECRET is not base64: %w", err)
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("secrets: GMESH_SECRET must decode to 32 bytes, got %d (make one with `gmesh keygen`)", len(k))
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plain. context is bound into the ciphertext (for example
// "user:3"), so a sealed key copied to another user's row won't open.
func (b *Box) Seal(plain []byte, context string) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return b.aead.Seal(nonce, nonce, plain, []byte(context))
}

func (b *Box) Open(sealed []byte, context string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("secrets: sealed value too short")
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(context))
	if err != nil {
		return nil, errors.New("secrets: can't open sealed key (wrong GMESH_SECRET?)")
	}
	return plain, nil
}
