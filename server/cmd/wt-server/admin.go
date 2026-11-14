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

// parseCreateSpec accepts "user", "user:password" or "user:password:role".
func parseCreateSpec(spec string) (string, string, string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", "", errors.New("empty user spec")
	}
	parts := strings.SplitN(spec, ":", 3)
	name := strings.TrimSpace(parts[0])
	pass := ""
	if len(parts) > 1 {
		pass = parts[1]
	}
	role := "admin"
	if len(parts) == 3 && strings.TrimSpace(parts[2]) != "" {
		role = strings.ToLower(strings.TrimSpace(parts[2]))
	}
	if role != "admin" && role != "user" {
		return "", "", "", errors.New("role must be admin or user")
	}
	if err := auth.ValidateUsername(name); err != nil {
		return "", "", "", err
	}
	if pass == "" {
		pass = randomPassword()
		fmt.Printf("generated password for %s: %s\n", name, pass)
	}
	return name, pass, role, nil
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
