package p2p

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/hashicorp/yamux"
)

// Server-bridged ("中转") sessions.
//
// A direct punch is not always possible (two symmetric NATs, hardened clouds).
// In that case the sync server splices two streams of the same account:
//
//	target device  --park-->  [server]  <--open-- initiator
//
// Both ends then run a yamux session over that single stream, so the Channel API
// (DialTCP / Call / OpenStream / Serve) is identical to a punched link; only the
// transport — and the latency — differ.
//
// Wire protocol on the bridge stream:
//
//	initiator -> server : {target, device}
//	target    -> server : {register, device}
//	server -> both      : one byte 0x01 (paired) or 0x00 (target not available)
//	then raw bytes, muxed by yamux
const (
	BridgeMethod   = "p2p.bridge"
	bridgePaired   = 0x01
	bridgeRejected = 0x00
	// bridgeHandler is the wire purpose/token suffix; the secret is per account.
	bridgeSecretPrefix = "relay:"
)

// BridgeSecret derives the shared secret for relayed sessions of an account.
// Both devices must pass the same value (a device id here would break auth).
func BridgeSecret(account string) string {
	if account == "" {
		account = "anonymous"
	}
	return bridgeSecretPrefix + account
}

// OpenBridgeFunc opens a streaming method on the sync server.
type OpenBridgeFunc func(ctx context.Context, method string, body any) (net.Conn, func(), error)

// yamuxConfig is shared by both ends: keepalive keeps the bridge alive through
// idle periods, and LogOutput must be set (yamux rejects having neither).
func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.ConnectionWriteTimeout = 30 * time.Second
	cfg.LogOutput = io.Discard
	return cfg
}

// RelayDial connects to a device through the sync-server bridge and returns a
// Channel that behaves like a punched one.
func RelayDial(ctx context.Context, open OpenBridgeFunc, secret, device string) (*Channel, error) {
	if open == nil {
		return nil, errors.New("中转通道不可用")
	}
	conn, release, err := open(ctx, BridgeMethod, map[string]any{"target": device, "device": ""})
	if err != nil {
		return nil, err
	}
	if err := expectPairing(conn, 20*time.Second); err != nil {
		release()
		return nil, err
	}
	sess, err := yamux.Client(conn, yamuxConfig())
	if err != nil {
		release()
		return nil, fmt.Errorf("中转会话建立失败: %w", err)
	}
	ch := NewRelayChannel(WrapYamux(sess), secret, "relay:"+device)
	ch.release = release
	return ch, nil
}

// RelayServe parks a bridge stream for this device and hands a Channel to
// onSession whenever an initiator pairs with it. It re-registers forever.
func RelayServe(ctx context.Context, open OpenBridgeFunc, secret, device string, onSession func(*Channel)) {
	if open == nil {
		return
	}
	for ctx.Err() == nil {
		conn, release, err := open(ctx, BridgeMethod, map[string]any{"register": true, "device": device})
		if err != nil {
			sleepCtx(ctx, 3*time.Second)
			continue
		}
		// The server answers 0x01 once an initiator paired; before that the
		// stream simply stays open.
		if err := expectPairing(conn, 0); err != nil {
			release()
			sleepCtx(ctx, 3*time.Second)
			continue
		}
		sess, err := yamux.Server(conn, yamuxConfig())
		if err != nil {
			release()
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		ch := NewRelayChannel(WrapYamux(sess), secret, "relay")
		ch.release = release
		if onSession != nil {
			onSession(ch)
		}
		<-sess.CloseChan()
		release()
		sleepCtx(ctx, 2*time.Second)
	}
}

// expectPairing reads the single status byte. timeout<=0 means "wait forever"
// (the registering side waits for an initiator).
func expectPairing(conn net.Conn, timeout time.Duration) error {
	if timeout > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
	}
	head := make([]byte, 1)
	if _, err := io.ReadFull(conn, head); err != nil {
		_ = conn.SetReadDeadline(time.Time{})
		return fmt.Errorf("中转握手失败: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	switch head[0] {
	case bridgePaired:
		return nil
	case bridgeRejected:
		return errors.New("对方未开启中转（需要对方在线并登录同一账号）")
	default:
		return errors.New("中转握手响应无效")
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
