package integration

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"wailstools/internal/ftpx"
	"wailstools/internal/secretstore"
	"wailstools/internal/sshmgr"
	"wailstools/internal/vaultsync"
)

// End-to-end proof of the secret sync design: device A encrypts its SSH hosts,
// RSA key and FTP profile with a key derived from the account password, uploads
// only ciphertext, and device B (empty stores) decrypts and adopts them.
func TestEncryptedSecretsTravelBetweenDevices(t *testing.T) {
	srv := startServer(t, true, true)
	_, tcpAddr := srv.Addrs()
	_, portStr, err := net.SplitHostPort(tcpAddr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port := atoi(portStr)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const username, password = "alice", "super-secret-1"

	// ---- device A: build local secrets ----------------------------------
	dirA := t.TempDir()
	vaultA, err := secretstore.Open(dirA)
	if err != nil {
		t.Fatalf("vault A: %v", err)
	}
	sshA, err := sshmgr.Open(filepath.Join(dirA, "ssh"))
	if err != nil {
		t.Fatalf("ssh store A: %v", err)
	}
	ftpA, err := ftpx.OpenProfiles(filepath.Join(dirA, "ftp", "profiles.json"), vaultA)
	if err != nil {
		t.Fatalf("ftp store A: %v", err)
	}

	host, err := sshA.SaveHost(sshmgr.Host{
		Name: "web-1", Address: "10.0.0.5", Port: 22, User: "root",
		AuthMode: sshmgr.AuthPassword, Password: "host-password", Route: sshmgr.RouteDirect,
	})
	if err != nil {
		t.Fatalf("save host: %v", err)
	}
	key, err := sshA.GenerateKey(sshmgr.GenerateOptions{Name: "rsa-2048", Type: "rsa", Bits: 2048})
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	profile, err := ftpA.Save(ftpx.Profile{
		Name: "backup", Config: ftpx.Config{Host: "ftp.example.com", Port: 21, User: "u"},
		Password: "ftp-password",
	})
	if err != nil {
		t.Fatalf("save ftp profile: %v", err)
	}

	bundleA := vaultsync.Bundle{
		Username:   username,
		Hosts:      sshA.ExportHosts(),
		Keys:       sshA.ExportKeys(),
		FTP:        ftpA.Export(),
		KnownHosts: map[string]string{"10.0.0.5:22": "SHA256:pin"},
	}
	if len(bundleA.Hosts) != 1 || bundleA.Hosts[0].Password != "host-password" {
		t.Fatalf("export must reveal host password: %+v", bundleA.Hosts)
	}
	if len(bundleA.Keys) != 1 || bundleA.Keys[0].PrivateKey == "" {
		t.Fatalf("export must reveal private key: %+v", bundleA.Keys)
	}

	clientA, _ := newClient(t, port)
	if _, err := clientA.Connect(ctx); err != nil {
		t.Fatalf("connect A: %v", err)
	}
	if _, err := clientA.Register(ctx, username, password, "", "Alice"); err != nil {
		t.Fatalf("register: %v", err)
	}

	keyA := vaultsync.DeriveKey(username, password)
	blob, err := vaultsync.Seal(keyA, bundleA)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if err := clientA.Call(ctx, "vault.put", map[string]any{"kind": vaultsync.KindBundle, "data": blob}, nil); err != nil {
		t.Fatalf("vault.put: %v", err)
	}

	// ---- device B: empty stores, same account ---------------------------
	dirB := t.TempDir()
	sshB, err := sshmgr.Open(filepath.Join(dirB, "ssh"))
	if err != nil {
		t.Fatalf("ssh store B: %v", err)
	}
	ftpB, err := ftpx.OpenProfiles(filepath.Join(dirB, "ftp", "profiles.json"), vaultA)
	if err != nil {
		t.Fatalf("ftp store B: %v", err)
	}

	clientB, _ := newClient(t, port)
	if _, err := clientB.Connect(ctx); err != nil {
		t.Fatalf("connect B: %v", err)
	}
	if _, err := clientB.Login(ctx, username, password); err != nil {
		t.Fatalf("login B: %v", err)
	}

	var got struct {
		Found bool   `json:"found"`
		Data  string `json:"data"`
	}
	if err := clientB.Call(ctx, "vault.get", map[string]any{"kind": vaultsync.KindBundle}, &got); err != nil {
		t.Fatalf("vault.get: %v", err)
	}
	if !got.Found || got.Data == "" {
		t.Fatal("expected the encrypted bundle on the server")
	}
	if !isCiphertext(got.Data) {
		t.Fatal("server payload must not contain plaintext secrets")
	}

	keyB := vaultsync.DeriveKey(username, password)
	remote, err := vaultsync.Open(keyB, got.Data)
	if err != nil {
		t.Fatalf("open on device B: %v", err)
	}
	merged := vaultsync.Merge(vaultsync.Bundle{}, remote)
	if n, err := sshB.ApplyHosts(merged.Hosts); err != nil || n != 1 {
		t.Fatalf("apply hosts: n=%d err=%v", n, err)
	}
	if n, err := sshB.ApplyKeys(merged.Keys); err != nil || n != 1 {
		t.Fatalf("apply keys: n=%d err=%v", n, err)
	}
	if n, err := ftpB.Apply(merged.FTP); err != nil || n != 1 {
		t.Fatalf("apply ftp: n=%d err=%v", n, err)
	}

	// The adopted host password and key must be usable on device B.
	if pw, err := sshB.HostPassword(host.ID); err != nil || pw != "host-password" {
		t.Fatalf("host password on B: %q err=%v", pw, err)
	}
	adopted, ok := sshB.GetKey(key.ID)
	if !ok || adopted.Fingerprint != key.Fingerprint {
		t.Fatalf("key on B: %+v (want fingerprint %s)", adopted, key.Fingerprint)
	}
	if pub, err := sshB.PublicKeyFor(key.ID); err != nil || pub != key.PublicKey {
		t.Fatalf("public key on B: %q err=%v", pub, err)
	}
	if pw, _, err := ftpB.PasswordFor(profile.ID); err != nil || pw != "ftp-password" {
		t.Fatalf("ftp password on B: %q err=%v", pw, err)
	}

	// A wrong password must not silently produce a partial bundle.
	if _, err := vaultsync.Open(vaultsync.DeriveKey(username, "wrong-password"), got.Data); err == nil {
		t.Fatal("wrong password must fail to decrypt")
	}
}

func isCiphertext(blob string) bool {
	if len(blob) < 16 || blob[:5] != "wtv1:" {
		return false
	}
	for _, bad := range []string{"host-password", "ftp-password", "BEGIN OPENSSH PRIVATE KEY", "BEGIN RSA PRIVATE KEY"} {
		if contains(blob, bad) {
			return false
		}
	}
	return true
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}