// Package secrets seals saved connection secrets (API tokens, client
// secrets) with AES-256-GCM, so a downloaded or copied database file
// doesn't carry them in plain text. The key lives in secrets.key next to
// the database. Someone with the whole server has the key too; that's the
// same exposure the environment values have.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Prefix marks a sealed value: "enc:v1:<base64 nonce+ciphertext>".
const Prefix = "enc:v1:"

// KeyFile is the key's file name, in the same folder as the database.
const KeyFile = "secrets.key"

// ErrUnreadable is returned when a sealed value can't be opened, usually
// because the database was restored onto a server without its key.
var ErrUnreadable = errors.New("can't read the saved secret")

type Box struct{ aead cipher.AEAD }

// LoadOrCreate reads the 32-byte key at path, making it (mode 0600) if it
// doesn't exist yet.
func LoadOrCreate(path string) (*Box, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		// O_EXCL: never overwrite a key another process just made.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", path, err)
		}
		if _, err := f.Write(key); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return New(key)
}

// New makes a Box from a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secrets key must be 32 bytes, got %d", len(key))
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

// Seal encrypts a value. "" stays "", so a cleared secret reads as unset.
func (b *Box) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return Prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a sealed value. A value without the prefix is returned as
// it is (a plain value saved before it was sealed).
func (b *Box) Open(stored string) (string, error) {
	if !IsSealed(stored) {
		return stored, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, Prefix))
	n := b.aead.NonceSize()
	if err != nil || len(raw) < n {
		return "", ErrUnreadable
	}
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", ErrUnreadable
	}
	return string(plain), nil
}

// IsSealed reports whether a stored value is encrypted.
func IsSealed(stored string) bool { return strings.HasPrefix(stored, Prefix) }
