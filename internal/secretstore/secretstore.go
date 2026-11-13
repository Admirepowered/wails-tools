// Package secretstore encrypts locally persisted secrets (passwords, private
// keys, tokens) with a machine-local AES-256-GCM key.
package secretstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const prefix = "enc.v1:"

// Vault seals and opens secret strings.
type Vault struct {
	mu   sync.Mutex
	aead cipher.AEAD
}

var (
	shared     *Vault
	sharedOnce sync.Once
	sharedErr  error
)

// Shared returns the process-wide vault rooted at dir.
func Shared(dir string) (*Vault, error) {
	sharedOnce.Do(func() {
		shared, sharedErr = Open(dir)
	})
	return shared, sharedErr
}

// Open loads (or creates) the master key in dir.
func Open(dir string) (*Vault, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "master.key")
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("secretstore: master key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead}, nil
}

// Seal encrypts plain, returning an empty string for empty input.
func (v *Vault) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if strings.HasPrefix(plain, prefix) {
		return plain, nil
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := v.aead.Seal(nil, nonce, []byte(plain), nil)
	return prefix + base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

// Reveal decrypts a sealed value. Plain legacy values pass through unchanged.
func (v *Vault) Reveal(blob string) (string, error) {
	if blob == "" {
		return "", nil
	}
	if !strings.HasPrefix(blob, prefix) {
		return blob, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(blob, prefix))
	if err != nil {
		return "", err
	}
	ns := v.aead.NonceSize()
	if len(raw) <= ns {
		return "", errors.New("secretstore: ciphertext too short")
	}
	plain, err := v.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// Seal is a helper on an error-tolerant shared vault.
func Seal(dir, plain string) string {
	if plain == "" {
		return ""
	}
	v, err := Shared(dir)
	if err != nil {
		return plain
	}
	out, err := v.Seal(plain)
	if err != nil {
		return plain
	}
	return out
}

// Reveal is a helper on an error-tolerant shared vault.
func Reveal(dir, blob string) string {
	if blob == "" {
		return ""
	}
	v, err := Shared(dir)
	if err != nil {
		return blob
	}
	out, err := v.Reveal(blob)
	if err != nil {
		return ""
	}
	return out
}
