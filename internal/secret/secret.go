// Package secret encrypts small values (users' API keys) at rest with AES-256-GCM.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

type Box struct {
	aead cipher.AEAD
}

// New builds a Box from a base64-encoded 32-byte key (e.g. `openssl rand -base64 32`).
func New(keyBase64 string) (*Box, error) {
	key, err := base64.StdEncoding.DecodeString(keyBase64)
	if err != nil {
		return nil, fmt.Errorf("secret key is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secret key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Seal(plaintext string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func (b *Box) Open(sealed []byte) (string, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("sealed value too short")
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", fmt.Errorf("cannot decrypt (wrong secret key?): %w", err)
	}
	return string(plain), nil
}
