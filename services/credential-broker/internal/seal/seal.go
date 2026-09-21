// Package seal wraps standard-library AES-256-GCM with random nonces and
// purpose-separated keys. It does not define a new cryptographic primitive.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
)

var ErrSealed = errors.New("invalid sealed data")

type Box struct{ aead cipher.AEAD }

func Derive(master []byte, purpose string) []byte {
	h := hmac.New(sha256.New, master)
	_, _ = h.Write([]byte("hermes-credential-broker/v1/" + purpose))
	return h.Sum(nil)
}

func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("key must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{a}, nil
}

func (b *Box) Seal(plain, aad []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, aad), nil
}
func (b *Box) Open(data, aad []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(data) < n+b.aead.Overhead() {
		return nil, ErrSealed
	}
	out, err := b.aead.Open(nil, data[:n], data[n:], aad)
	if err != nil {
		return nil, ErrSealed
	}
	return out, nil
}
func Random() string {
	var p [32]byte
	if _, err := io.ReadFull(rand.Reader, p[:]); err != nil {
		panic("OS randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(p[:])
}
func Digest(v string) string {
	h := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func Equal(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }

// Wipe is best effort only: Go and OS copies cannot be guaranteed to be erased.
func Wipe(b []byte) { clear(b) }
