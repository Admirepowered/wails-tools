package api

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"wailstools/internal/wire"
)

// Relay bridging: when two devices cannot punch a direct path, one of them can
// ask the sync server to bridge the two devices. The server holds a persistent
// stream from the *target* device and splices it with the initiator's stream, so
// every stream a client opens travels through the server (higher latency).

type bridgeEntry struct {
	userID string
	device string
	conn   net.Conn
	paired chan net.Conn
	done   chan struct{}
	once   sync.Once
}

func (e *bridgeEntry) finish() { e.once.Do(func() { close(e.done) }) }

type bridgeHub struct {
	mu      sync.Mutex
	pending map[string]*bridgeEntry // userID + "\x00" + device
}

func newBridgeHub() *bridgeHub { return &bridgeHub{pending: map[string]*bridgeEntry{}} }

func bridgeKey(userID, device string) string { return userID + "\x00" + device }

// register parks the target device's stream until an initiator asks for it.
func (h *bridgeHub) register(userID, device string, conn net.Conn) *bridgeEntry {
	entry := &bridgeEntry{userID: userID, device: device, conn: conn, paired: make(chan net.Conn, 1), done: make(chan struct{})}
	h.mu.Lock()
	// Replace an older registration for the same device (stale connection).
	if old, ok := h.pending[bridgeKey(userID, device)]; ok {
		old.finish()
		_ = old.conn.Close()
	}
	h.pending[bridgeKey(userID, device)] = entry
	h.mu.Unlock()
	return entry
}

func (h *bridgeHub) lookup(userID, device string) (*bridgeEntry, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.pending[bridgeKey(userID, device)]
	if ok {
		delete(h.pending, bridgeKey(userID, device))
	}
	return entry, ok
}

func (h *bridgeHub) remove(entry *bridgeEntry) {
	h.mu.Lock()
	if cur, ok := h.pending[bridgeKey(entry.userID, entry.device)]; ok && cur == entry {
		delete(h.pending, bridgeKey(entry.userID, entry.device))
	}
	h.mu.Unlock()
}

// bridge handles the p2p.bridge streaming method.
func (c *callCtx) bridgePipe(body json.RawMessage, conn net.Conn) error {
	uid, err := c.userID()
	if err != nil {
		return err
	}
	var req struct {
		Register bool   `json:"register"`
		Device   string `json:"device"`
		Target   string `json:"target"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return wire.Errorf("bad_request", "invalid body: %v", err)
	}
	hub := c.api.Bridge()
	if req.Register {
		if req.Device == "" {
			return wire.Errorf("bad_request", "device is required")
		}
		entry := hub.register(uid, req.Device, conn)
		defer hub.remove(entry)
		// Park until paired (or the client goes away).
		select {
		case initiator := <-entry.paired:
			// The initiator goroutine owns the copying; just wait for it.
			_, _ = conn.Write([]byte{0x01})
			<-entry.done
			_ = initiator
			return nil
		case <-entry.done:
			return nil
		case <-time.After(10 * time.Minute):
			return nil
		}
	}
	if req.Target == "" {
		return wire.Errorf("bad_request", "target is required")
	}
	entry, ok := hub.lookup(uid, req.Target)
	if !ok {
		_, _ = conn.Write([]byte{0x00})
		return wire.Errorf("offline", "对方设备未开启中转监听")
	}
	_, _ = conn.Write([]byte{0x01})
	entry.paired <- conn
	// Splice both directions and release the parked side afterwards.
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(entry.conn, conn)
		_ = entry.conn.Close()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, entry.conn)
		_ = conn.Close()
		done <- struct{}{}
	}()
	<-done
	entry.finish()
	return nil
}

// RelayStatus reports whether the bridge facility is available.
func (c *callCtx) relayBridgeStatus() (any, error) {
	if _, err := c.userID(); err != nil {
		return nil, err
	}
	return map[string]any{"enabled": true, "path": "server-bridge"}, nil
}

var errNoBridge = errors.New("bridge unavailable")
