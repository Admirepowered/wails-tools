package integration

import (
	"context"
	"testing"
	"time"
)

// TestChannelRPC checks that the non-streaming RPC path (used by the heartbeat)
// works over a punched channel.
func TestChannelRPC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	chA, chB := punchPair(t, ctx)
	defer chA.Close()
	defer chB.Close()

	go chB.Serve(context.Background(), &relayHandler{fsRoot: t.TempDir()})
	time.Sleep(200 * time.Millisecond)

	if err := chA.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	// A streaming call must work as well (file transfer uses it).
	var out map[string]any
	if err := chA.Call(ctx, "p2p.ping", nil, &out); err != nil {
		t.Fatalf("rpc ping: %v", err)
	}
	if out["ok"] != true {
		t.Fatalf("unexpected ping reply: %+v", out)
	}
}
