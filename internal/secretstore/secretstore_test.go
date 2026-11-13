package secretstore

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSealAndReveal(t *testing.T) {
	v, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sealed, err := v.Seal("super-secret-password")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if !strings.HasPrefix(sealed, prefix) {
		t.Fatalf("unexpected sealed format: %q", sealed)
	}
	if strings.Contains(sealed, "super-secret-password") {
		t.Fatal("plaintext leaked into the sealed value")
	}
	plain, err := v.Reveal(sealed)
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if plain != "super-secret-password" {
		t.Fatalf("round trip mismatch: %q", plain)
	}
	// Sealing twice must produce different ciphertexts (random nonce).
	again, _ := v.Seal("super-secret-password")
	if again == sealed {
		t.Fatal("expected a random nonce per seal")
	}
	// Empty values stay empty.
	if out, err := v.Seal(""); err != nil || out != "" {
		t.Fatalf("seal empty: %q %v", out, err)
	}
	if out, err := v.Reveal(""); err != nil || out != "" {
		t.Fatalf("reveal empty: %q %v", out, err)
	}
}

func TestRevealRejectsTampering(t *testing.T) {
	v, _ := Open(t.TempDir())
	sealed, _ := v.Seal("value")
	tampered := sealed[:len(sealed)-2] + "AA"
	if _, err := v.Reveal(tampered); err == nil {
		t.Fatal("expected tampered ciphertext to fail")
	}
	if _, err := v.Reveal(prefix + "!!!"); err == nil {
		t.Fatal("expected malformed base64 to fail")
	}
}

func TestLegacyPlaintextPassesThrough(t *testing.T) {
	v, _ := Open(t.TempDir())
	out, err := v.Reveal("plain-legacy-value")
	if err != nil || out != "plain-legacy-value" {
		t.Fatalf("legacy passthrough failed: %q %v", out, err)
	}
}

func TestMasterKeyIsPersisted(t *testing.T) {
	dir := t.TempDir()
	v1, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sealed, _ := v1.Seal("value")
	keyPath := filepath.Join(dir, "master.key")
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("master key missing: %v", err)
	}
	// Windows reports synthetic POSIX bits; ACLs govern access there instead.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("master key permissions too open: %v", info.Mode().Perm())
	}
	// A second vault over the same directory must decrypt the value.
	v2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	plain, err := v2.Reveal(sealed)
	if err != nil || plain != "value" {
		t.Fatalf("reopen failed to decrypt: %q %v", plain, err)
	}
}

func TestDifferentDirectoriesCannotDecrypt(t *testing.T) {
	v1, _ := Open(t.TempDir())
	v2, _ := Open(t.TempDir())
	sealed, _ := v1.Seal("value")
	if _, err := v2.Reveal(sealed); err == nil {
		t.Fatal("a different master key must not decrypt")
	}
}
