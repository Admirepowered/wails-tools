package vaultsync

import (
	"errors"
	"testing"

	"wailstools/internal/ftpx"
	"wailstools/internal/sshmgr"
)

func TestDeriveKeyIsDeterministic(t *testing.T) {
	a := DeriveKey("Admire", "102030000")
	b := DeriveKey("admire", "102030000")
	if len(a) != 32 {
		t.Fatalf("key length %d, want 32", len(a))
	}
	if string(a) != string(b) {
		t.Fatal("key must not depend on username casing")
	}
	if string(a) == string(DeriveKey("admire", "102030001")) {
		t.Fatal("different passwords must derive different keys")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	key := DeriveKey("admire", "102030000")
	in := Bundle{
		Username: "admire",
		Hosts: []sshmgr.Host{{
			ID: "host-1", Name: "web", Address: "1.2.3.4", Port: 22,
			User: "root", AuthMode: sshmgr.AuthKey, Password: "s3cr3t",
			Route: sshmgr.RouteDirect, CreatedAt: "2026-01-01T00:00:00Z",
			UpdatedAt: "2026-01-02T00:00:00Z",
		}},
		Keys: []sshmgr.Key{{
			ID: "key-1", Name: "rsa", Type: "rsa", PrivateKey: "-----BEGIN-----",
			PublicKey: "ssh-rsa AAA", Fingerprint: "SHA256:x",
		}},
		FTP:        []ftpx.Profile{{ID: "ftp-1", Name: "backup", Password: "pw"}},
		KnownHosts: map[string]string{"1.2.3.4:22": "SHA256:x"},
	}
	blob, err := Seal(key, in)
	if err != nil {
		t.Fatal(err)
	}
	if blob == "" || blob[:len(blobPrefix)] != blobPrefix {
		t.Fatalf("unexpected blob prefix: %.16q", blob)
	}
	out, err := Open(key, blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Hosts) != 1 || out.Hosts[0].Password != "s3cr3t" {
		t.Fatalf("host password lost: %+v", out.Hosts)
	}
	if len(out.Keys) != 1 || out.Keys[0].PrivateKey == "" {
		t.Fatalf("private key lost: %+v", out.Keys)
	}
	if len(out.FTP) != 1 || out.FTP[0].Password != "pw" {
		t.Fatalf("ftp password lost: %+v", out.FTP)
	}

	if _, err := Open(DeriveKey("admire", "wrong-password"), blob); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("expected ErrWrongPassword, got %v", err)
	}
}

func TestMergePrefersNewerAndKeepsUnion(t *testing.T) {
	local := Bundle{
		Hosts: []sshmgr.Host{
			{ID: "a", Name: "old", UpdatedAt: "2026-01-01T00:00:00Z"},
			{ID: "only-local", Name: "local", UpdatedAt: "2026-01-01T00:00:00Z"},
		},
		Keys: []sshmgr.Key{{ID: "k1", Name: "no-private"}},
		FTP:  []ftpx.Profile{{ID: "f1", Name: "local", UpdatedAt: "2026-01-01T00:00:00Z"}},
	}
	remote := Bundle{
		Hosts: []sshmgr.Host{
			{ID: "a", Name: "new", UpdatedAt: "2026-02-01T00:00:00Z"},
			{ID: "only-remote", Name: "remote", UpdatedAt: "2026-02-01T00:00:00Z"},
		},
		Keys: []sshmgr.Key{{ID: "k1", Name: "with-private", PrivateKey: "PEM"}},
		FTP:  []ftpx.Profile{{ID: "f1", Name: "stale", UpdatedAt: "2025-12-01T00:00:00Z"}},
	}
	got := Merge(local, remote)
	if len(got.Hosts) != 3 {
		t.Fatalf("expected 3 hosts, got %d", len(got.Hosts))
	}
	byID := map[string]string{}
	for _, h := range got.Hosts {
		byID[h.ID] = h.Name
	}
	if byID["a"] != "new" {
		t.Fatalf("newer host should win, got %q", byID["a"])
	}
	if byID["only-local"] != "local" || byID["only-remote"] != "remote" {
		t.Fatalf("union broken: %v", byID)
	}
	if len(got.Keys) != 1 || got.Keys[0].PrivateKey != "PEM" {
		t.Fatalf("key material should prefer the side that has it: %+v", got.Keys)
	}
	if len(got.FTP) != 1 || got.FTP[0].Name != "local" {
		t.Fatalf("stale remote ftp profile must not win: %+v", got.FTP)
	}
}

func TestChangedDetection(t *testing.T) {
	a := Bundle{Hosts: []sshmgr.Host{{ID: "a"}}}
	if Changed(a, a) {
		t.Fatal("identical bundles must compare equal")
	}
	b := Bundle{Hosts: []sshmgr.Host{{ID: "a"}, {ID: "b"}}}
	if !Changed(a, b) {
		t.Fatal("extra host must be detected")
	}
}
