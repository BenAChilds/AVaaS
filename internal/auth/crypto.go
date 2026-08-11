package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"strings"
)

type URLCipher struct{ aead cipher.AEAD }

func NewURLCipher(secret []byte) (*URLCipher, error) {
	sum := sha256.Sum256(secret)
	b, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &URLCipher{aead: a}, nil
}
func (c *URLCipher) Encrypt(value string) ([]byte, error) {
	n := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, n); err != nil {
		return nil, err
	}
	return c.aead.Seal(n, n, []byte(value), nil), nil
}
func (c *URLCipher) Decrypt(value []byte) (string, error) {
	n := c.aead.NonceSize()
	if len(value) < n {
		return "", errors.New("invalid ciphertext")
	}
	b, err := c.aead.Open(nil, value[:n], value[n:], nil)
	return string(b), err
}
func FingerprintHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	h := sha256.Sum256([]byte(strings.ToLower(u.Hostname())))
	return hex.EncodeToString(h[:8])
}
