package integration

import (
	"context"
	"net"
	"testing"
	"time"
)

// Encrypted secret bundles: ciphertext is stored per user, isolated between
// accounts and dropped when the password rotates.
func TestVaultBlobLifecycle(t *testing.T) {
	srv := startServer(t, true, true)
	_, tcpAddr := srv.Addrs()
	_, portStr, err := net.SplitHostPort(tcpAddr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port := atoi(portStr)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	alice, _ := newClient(t, port)
	if _, err := alice.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := alice.Register(ctx, "alice", "super-secret-1", "", "Alice"); err != nil {
		t.Fatalf("register: %v", err)
	}

	blob := "wtv1:Y2lwaGVydGV4dA"
	var put map[string]any
	if err := alice.Call(ctx, "vault.put", map[string]any{"kind": "bundle", "data": blob}, &put); err != nil {
		t.Fatalf("vault.put: %v", err)
	}

	var got struct {
		Found bool   `json:"found"`
		Kind  string `json:"kind"`
		Data  string `json:"data"`
	}
	if err := alice.Call(ctx, "vault.get", map[string]any{"kind": "bundle"}, &got); err != nil {
		t.Fatalf("vault.get: %v", err)
	}
	if !got.Found || got.Data != blob || got.Kind != "bundle" {
		t.Fatalf("unexpected blob: %+v", got)
	}

	var list struct {
		Blobs []struct {
			Kind string `json:"kind"`
			Data string `json:"data"`
		} `json:"blobs"`
	}
	if err := alice.Call(ctx, "vault.list", nil, &list); err != nil {
		t.Fatalf("vault.list: %v", err)
	}
	if len(list.Blobs) != 1 || list.Blobs[0].Kind != "bundle" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if list.Blobs[0].Data != "" {
		t.Fatal("vault.list must not return ciphertext")
	}

	// A second account must not see the first one's blob.
	bob, _ := newClient(t, port)
	if _, err := bob.Connect(ctx); err != nil {
		t.Fatalf("bob connect: %v", err)
	}
	if _, err := bob.Register(ctx, "bobby", "super-secret-2", "", "Bob"); err != nil {
		t.Fatalf("bob register: %v", err)
	}
	var bobGot struct {
		Found bool `json:"found"`
	}
	if err := bob.Call(ctx, "vault.get", map[string]any{"kind": "bundle"}, &bobGot); err != nil {
		t.Fatalf("bob vault.get: %v", err)
	}
	if bobGot.Found {
		t.Fatal("vault must be isolated per user")
	}

	// Rotating the password destroys the old ciphertext.
	var changed map[string]any
	if err := alice.Call(ctx, "auth.changePassword", map[string]any{
		"oldPassword": "super-secret-1", "newPassword": "super-secret-3",
	}, &changed); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if n, _ := changed["vaultCleared"].(float64); n < 1 {
		t.Fatalf("expected vault blobs to be dropped, got %+v", changed)
	}
	var after struct {
		Found bool `json:"found"`
	}
	if err := alice.Call(ctx, "vault.get", map[string]any{"kind": "bundle"}, &after); err != nil {
		t.Fatalf("vault.get after change: %v", err)
	}
	if after.Found {
		t.Fatal("ciphertext keyed by the old password must be dropped")
	}
}