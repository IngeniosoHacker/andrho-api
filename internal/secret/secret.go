// Package secret encrypts the few credentials andrho-api must keep (an
// account's Odoo password) with AES-256-GCM. The key is SHA-256 of
// SECRETS_KEY; set it in production and never change it, or stored secrets
// can't be read back.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

// Box seals and opens secrets with one key.
type Box struct{ aead cipher.AEAD }

// New derives the key from passphrase.
func New(passphrase string) (*Box, error) {
	if passphrase == "" {
		return nil, errors.New("secret: empty key")
	}
	key := sha256.Sum256([]byte(passphrase))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plain; the nonce is prepended.
func (b *Box) Seal(plain string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, []byte(plain), nil), nil
}

// Open decrypts what Seal produced.
func (b *Box) Open(sealed []byte) (string, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("secret: too short")
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
