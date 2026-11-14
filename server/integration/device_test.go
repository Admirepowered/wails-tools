package integration

import (
	"context"
	"net"
	"testing"
	"time"
)

// The same physical machine must occupy exactly one device slot even if the
// client's stored installation id changed (reinstall, cleared settings).
func TestDeviceDedupeByMachine(t *testing.T) {
	srv := startServer(t, true, true)
	_, tcpAddr := srv.Addrs()
	_, portStr, err := net.SplitHostPort(tcpAddr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port := atoi(portStr)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	client, _ := newClient(t, port)
	if _, err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := client.Register(ctx, "alice", "super-secret-1", "", "Alice"); err != nil {
		t.Fatalf("register: %v", err)
	}

	login := func(deviceID, name, machine string) {
		var out map[string]any
		if err := client.Call(ctx, "auth.login", map[string]any{
			"username": "alice", "password": "super-secret-1",
			"deviceId": deviceID, "deviceName": name, "machineId": machine,
		}, &out); err != nil {
			t.Fatalf("login %s: %v", deviceID, err)
		}
	}
	devices := func() []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Machine string `json:"machine"`
	} {
		var out struct {
			Devices []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Machine string `json:"machine"`
			} `json:"devices"`
		}
		if err := client.Call(ctx, "admin.devices.self", nil, &out); err != nil {
			t.Fatalf("devices: %v", err)
		}
		return out.Devices
	}

	login("dev-a", "DESKTOP-1", "machine-1")
	if got := devices(); len(got) != 1 || got[0].ID != "dev-a" {
		t.Fatalf("after first login: %+v", got)
	}

	// New install id, same machine: must replace, not duplicate.
	login("dev-b", "DESKTOP-1", "machine-1")
	if got := devices(); len(got) != 1 || got[0].ID != "dev-b" || got[0].Machine != "machine-1" {
		t.Fatalf("duplicate device not collapsed: %+v", got)
	}

	// A different machine adds a second device.
	login("dev-c", "DESKTOP-2", "machine-2")
	if got := devices(); len(got) != 2 {
		t.Fatalf("expected 2 devices, got %+v", got)
	}

	// Logging in again from the first machine keeps the count stable.
	login("dev-d", "DESKTOP-1", "machine-1")
	if got := devices(); len(got) != 2 {
		t.Fatalf("expected 2 devices after re-login, got %+v", got)
	}
}