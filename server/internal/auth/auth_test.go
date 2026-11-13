package auth

import (
	"strings"
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(hash, "argon2id$v=19$m=") {
		t.Fatalf("unexpected hash format: %q", hash)
	}
	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Fatalf("verify correct password: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword("wrong password", hash)
	if err != nil || ok {
		t.Fatalf("verify wrong password: ok=%v err=%v", ok, err)
	}
	// Salts must differ.
	other, _ := HashPassword("correct horse battery staple")
	if other == hash {
		t.Fatal("hash must be salted")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	if _, err := VerifyPassword("x", "not-a-hash"); err == nil {
		t.Fatal("expected malformed hash error")
	}
	if _, err := VerifyPassword("x", "bcrypt$v=19$m=1,t=1,p=1$aa$bb"); err == nil {
		t.Fatal("expected unsupported algorithm error")
	}
}

func TestTokensAreUniqueAndHashed(t *testing.T) {
	t1, h1, err := NewToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	t2, h2, _ := NewToken()
	if t1 == t2 || h1 == h2 {
		t.Fatal("tokens must be unique")
	}
	if h1 != HashToken(t1) {
		t.Fatal("hash mismatch")
	}
	if len(t1) < 40 {
		t.Fatalf("token too short: %d", len(t1))
	}
	if h1 == t1 {
		t.Fatal("stored hash must not equal the token")
	}
}

func TestTokenExpiry(t *testing.T) {
	exp := TokenExpiry(2 * time.Hour)
	ts, err := time.Parse(time.RFC3339, exp)
	if err != nil {
		t.Fatalf("parse expiry: %v", err)
	}
	if time.Until(ts) < 90*time.Minute {
		t.Fatalf("expiry too soon: %s", exp)
	}
}

func TestValidateUsername(t *testing.T) {
	valid := []string{"alice", "bob_1", "user.name", "a-b-c", "AbC123"}
	for _, name := range valid {
		if err := ValidateUsername(name); err != nil {
			t.Fatalf("%q should be valid: %v", name, err)
		}
	}
	invalid := []string{"", "ab", strings.Repeat("x", 33), "has space", "bad!char", "中文"}
	for _, name := range invalid {
		if err := ValidateUsername(name); err == nil {
			t.Fatalf("%q should be invalid", name)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("short"); err == nil {
		t.Fatal("short password should fail")
	}
	if err := ValidatePassword("long-enough-1"); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
	if err := ValidatePassword(strings.Repeat("x", 300)); err == nil {
		t.Fatal("overlong password should fail")
	}
}

func TestSignHMAC(t *testing.T) {
	a := SignHMAC([]byte("secret"), "payload")
	b := SignHMAC([]byte("secret"), "payload")
	c := SignHMAC([]byte("other"), "payload")
	if a != b {
		t.Fatal("HMAC must be deterministic")
	}
	if a == c {
		t.Fatal("HMAC must depend on the secret")
	}
}
