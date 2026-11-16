package api

import (
	"encoding/json"
	"io"
	"net"
	"sync"
	"time"

	"wailstools/internal/wire"
)

// Server-side bridge ("中转").
//
// Two devices of the same account that cannot punch a direct path can ask the
// sync server to splice their streams:
//
//	target device  --park-->  [server]  <--open-- initiator
//
// Protocol on the stream (one byte, then raw bytes):
//
//	0x01 paired  / 0x00 target not parked
//
// The parked handler only waits; the initiator handler owns the byte copying and
// closes both streams when either direction ends.

const (
	bridgePaired   = 0x01
	bridgeRejected = 0x00
	// bridgeParkTTL bounds how long a parked stream may wait for a peer.
	bridgeParkTTL = 10 * time.Minute
)

type bridgeSlot struct {
	conn   net.Conn
	paired chan net.Conn
	done   chan struct{}
	once   sync.Once
}

func (s *bridgeSlot) finish() { s.once.Do(func() { close(s.done) }) }

type bridgeHub struct {
	mu   sync.Mutex
	slot map[string]*bridgeSlot // key: userID + "\x00" + device
}

func newBridgeHub() *bridgeHub { return &bridgeHub{slot: map[string]*bridgeSlot{}} }

func bridgeKey(userID, device string) string { return userID + "\x00" + device }

// park stores the target device's stream, replacing any stale registration.
func (h *bridgeHub) park(userID, device string, conn net.Conn) *bridgeSlot {
	s := &bridgeSlot{conn: conn, paired: make(chan net.Conn, 1), done: make(chan struct{})}
	h.mu.Lock()
	if old, ok := h.slot[bridgeKey(userID, device)]; ok {
		old.finish()
		_ = old.conn.Close()
	}
	h.slot[bridgeKey(userID, device)] = s
	h.mu.Unlock()
	return s
}

// take removes and returns the parked stream for a target device.
func (h *bridgeHub) take(userID, device string) (*bridgeSlot, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.slot[bridgeKey(userID, device)]
	if ok {
		delete(h.slot, bridgeKey(userID, device))
	}
	return s, ok
}

func (h *bridgeHub) drop(userID, device string, s *bridgeSlot) {
	h.mu.Lock()
	if cur, ok := h.slot[bridgeKey(userID, device)]; ok && cur == s {
		delete(h.slot, bridgeKey(userID, device))
	}
	h.mu.Unlock()
}

// bridgePipe implements the streaming method p2p.bridge.
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

	// ---- target side: park until an initiator shows up -------------------
	if req.Register {
		if req.Device == "" {
			return wire.Errorf("bad_request", "device is required")
		}
		slot := hub.park(uid, req.Device, conn)
		defer hub.drop(uid, req.Device, slot)
		select {
		case peer := <-slot.paired:
			// The initiator handler owns the copy loop; just wait for it.
			_ = peer
			<-slot.done
			return nil
		case <-slot.done:
			return nil
		case <-time.After(bridgeParkTTL):
			return nil
		}
	}

	// ---- initiator side ------------------------------------------------
	if req.Target == "" {
		return wire.Errorf("bad_request", "target is required")
	}
	slot, ok := hub.take(uid, req.Target)
	if !ok {
		_, _ = conn.Write([]byte{bridgeRejected})
		return wire.Errorf("offline", "对方未开启中转")
	}
	if _, err := conn.Write([]byte{bridgePaired}); err != nil {
		slot.finish()
		return err
	}
	if _, err := slot.conn.Write([]byte{bridgePaired}); err != nil {
		slot.finish()
		return err
	}
	slot.paired <- conn

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(slot.conn, conn)
		closeWriteConn(slot.conn)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, slot.conn)
		closeWriteConn(conn)
	}()
	wg.Wait()
	_ = slot.conn.Close()
	slot.finish()
	return nil
}

// closeWriteConn half-closes when the transport supports it.
func closeWriteConn(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// relayBridgeStatus reports whether bridging is available.
func (c *callCtx) relayBridgeStatus() (any, error) {
	if _, err := c.userID(); err != nil {
		return nil, err
	}
	return map[string]any{"enabled": true, "path": "server-bridge"}, nil
}
