// Package auth implements password hashing and bearer-token sessions.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Params tunes the argon2id KDF.
type Params struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultParams are tuned for interactive logins on modest hardware.
var DefaultParams = Params{Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32}

// ErrInvalidHash marks a malformed hash string.
var ErrInvalidHash = errors.New("auth: malformed password hash")

// HashPassword returns an argon2id PHC string.
func HashPassword(password string) (string, error) {
	return HashPasswordWith(password, DefaultParams)
}

// HashPasswordWith hashes with explicit parameters.
func HashPasswordWith(password string, p Params) (string, error) {
	salt := make([]byte, p.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)
	return fmt.Sprintf("argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword compares a password against a PHC string.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[1], "v=%d", &version); err != nil {
		return false, ErrInvalidHash
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false, ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NewToken creates a random session token and its storage hash.
func NewToken() (token string, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

// HashToken derives the storage key for a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TokenExpiry returns the expiry timestamp for a new token.
func TokenExpiry(ttl time.Duration) string {
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	return time.Now().Add(ttl).UTC().Format(time.RFC3339)
}

// ValidateUsername enforces a conservative account-name policy.
func ValidateUsername(name string) error {
	name = strings.TrimSpace(name)
	if len(name) < 3 || len(name) > 32 {
		return errors.New("username must be 3-32 characters")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return errors.New("username may only contain letters, digits, dot, dash and underscore")
		}
	}
	return nil
}

// ValidatePassword enforces a minimum password policy.
func ValidatePassword(pw string) error {
	if len(pw) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if len(pw) > 256 {
		return errors.New("password too long")
	}
	return nil
}

// SignHMAC signs a payload with a server secret (used for short-lived links).
func SignHMAC(secret []byte, payload string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}
