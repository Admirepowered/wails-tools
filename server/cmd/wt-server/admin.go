package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"wailstools/server/internal/auth"
)

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "seed"
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashPassword(pw string) (string, error) {
	if err := auth.ValidatePassword(pw); err != nil {
		return "", err
	}
	return auth.HashPassword(pw)
}

// parseCreateSpec accepts "user" (random password) or "user:password".
func parseCreateSpec(spec string) (string, string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", errors.New("empty user spec")
	}
	if i := strings.Index(spec, ":"); i >= 0 {
		name := strings.TrimSpace(spec[:i])
		pass := spec[i+1:]
		if err := auth.ValidateUsername(name); err != nil {
			return "", "", err
		}
		if pass == "" {
			pass = randomPassword()
			fmt.Printf("generated password for %s: %s\n", name, pass)
		}
		return name, pass, nil
	}
	if err := auth.ValidateUsername(spec); err != nil {
		return "", "", err
	}
	pass := randomPassword()
	fmt.Printf("generated password for %s: %s\n", spec, pass)
	return spec, pass, nil
}

func randomPassword() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%"
	out := make([]byte, 20)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "ChangeMe!23456789"
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out)
}
