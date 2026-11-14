package integration

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"wailstools/internal/serverlink"
	"wailstools/internal/vaultsync"
)

// A brand-new device that logs into an existing account must receive the notes,
// the subscription/custom-node document and the encrypted secret bundle.
func TestNewDeviceRestoresEverything(t *testing.T) {
	srv := startServer(t, true, true)
	_, tcpAddr := srv.Addrs()
	_, portStr, err := net.SplitHostPort(tcpAddr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port := atoi(portStr)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	const username, password = "alice", "super-secret-1"

	// ---- device A: create data ------------------------------------------
	deviceA, _ := newClient(t, port)
	if _, err := deviceA.Connect(ctx); err != nil {
		t.Fatalf("connect A: %v", err)
	}
	if _, err := deviceA.Register(ctx, username, password, "", "Alice"); err != nil {
		t.Fatalf("register: %v", err)
	}

	now := time.Now().UnixMilli()
	if _, _, _, err := deviceA.NotesSync(ctx, 0, []serverlink.Note{{
		ID: "note-1", Title: "新设备也要看到", Content: "hello", UpdatedAt: now, CreatedAt: now,
	}}); err != nil {
		t.Fatalf("notes sync A: %v", err)
	}

	key := vaultsync.DeriveKey(username, password)
	blob, err := vaultsync.Seal(key, vaultsync.Bundle{
		Username: username,
		KnownHosts: map[string]string{
			"10.0.0.5:22": "SHA256:pin",
		},
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if err := deviceA.Call(ctx, "vault.put", map[string]any{
		"kind": vaultsync.KindBundle, "data": blob,
	}, nil); err != nil {
		t.Fatalf("vault.put: %v", err)
	}

	doc := map[string]any{
		"version": 2,
		"sources": []map[string]any{{"id": "sub-1", "name": "机场", "url": "https://example.com/sub"}},
		"nodes": []map[string]any{{
			"id": "custom-1", "name": "落地", "server": "1.2.3.4", "port": 443,
			"protocol": "vless", "uuid": "11111111-2222-3333-4444-555555555555",
		}},
	}
	payload, _ := json.Marshal(doc)
	if _, err := deviceA.PutSubscriptions(ctx, payload); err != nil {
		t.Fatalf("put subscriptions: %v", err)
	}

	// ---- device B: fresh install, same account --------------------------
	deviceB, _ := newClient(t, port)
	if _, err := deviceB.Connect(ctx); err != nil {
		t.Fatalf("connect B: %v", err)
	}
	if _, err := deviceB.Login(ctx, username, password); err != nil {
		t.Fatalf("login B: %v", err)
	}

	raw, _, found, err := deviceB.GetSubscriptions(ctx)
	if err != nil {
		t.Fatalf("get subscriptions: %v", err)
	}
	if !found {
		t.Fatal("subscription document missing on the new device")
	}
	var got struct {
		Sources []struct {
			ID string `json:"id"`
		} `json:"sources"`
		Nodes []struct {
			ID   string `json:"id"`
			Port int    `json:"port"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("document shape: %v", err)
	}
	if len(got.Sources) != 1 || got.Sources[0].ID != "sub-1" {
		t.Fatalf("subscriptions not restored: %+v", got.Sources)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].Port != 443 {
		t.Fatalf("custom nodes not restored: %+v", got.Nodes)
	}

	var secret struct {
		Found bool   `json:"found"`
		Data  string `json:"data"`
	}
	if err := deviceB.Call(ctx, "vault.get", map[string]any{"kind": vaultsync.KindBundle}, &secret); err != nil {
		t.Fatalf("vault.get: %v", err)
	}
	if !secret.Found || secret.Data == "" {
		t.Fatal("secret bundle missing on the new device")
	}
	bundle, err := vaultsync.Open(vaultsync.DeriveKey(username, password), secret.Data)
	if err != nil {
		t.Fatalf("decrypt on B: %v", err)
	}
	if bundle.KnownHosts["10.0.0.5:22"] != "SHA256:pin" {
		t.Fatalf("bundle content lost: %+v", bundle.KnownHosts)
	}

	notes, _, _, err := deviceB.NotesSync(ctx, 0, nil)
	if err != nil {
		t.Fatalf("notes sync B: %v", err)
	}
	if len(notes) != 1 || notes[0].Title != "新设备也要看到" {
		t.Fatalf("notes not restored on the new device: %+v", notes)
	}
}
