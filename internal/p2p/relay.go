package p2p

import (
	"context"
	"errors"
	"io"
	"net"
	"time"
)

// Relayed (bridged) sessions.
//
// When a direct punch is impossible the sync server splices two streams of the
// same account: the target device parks a stream (register), the initiator opens
// one and asks for the target. Both then run a yamux session over that stream, so
// the Channel API is identical — only the transport (and latency) differs.

// OpenBridgeFunc opens a streaming method on the sync server.
type OpenBridgeFunc func(ctx context.Context, method string, body any) (net.Conn, func(), error)

const bridgeMethod = "p2p.bridge"

// RelayDial connects to a device through the sync-server bridge.
func RelayDial(ctx context.Context, open OpenBridgeFunc, secret, device string) (*Channel, error) {
	if open == nil {
		return nil, errors.New("中转通道不可用")
	}
	conn, release, err := open(ctx, bridgeMethod, map[string]any{"target": device})
	if err != nil {
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	head := make([]byte, 1)
	if _, err := io.ReadFull(conn, head); err != nil {
		release()
		return nil, errors.New("中转握手失败: " + err.Error())
	}
	_ = conn.SetReadDeadline(time.Time{})
	if head[0] != 0x01 {
		release()
		return nil, errors.New("对方未开启中转监听（需要对方在线且已登录）")
	}
	sess, err := NewYamuxClient(conn)
	if err != nil {
		release()
		return nil, err
	}
	return NewRelayChannel(WrapYamux(sess), secret, "relay:"+device), nil
}

// RelayServe parks a bridge stream for this device and serves V3 requests on it.
// It re-registers automatically after the bridge is torn down.
func RelayServe(ctx context.Context, open OpenBridgeFunc, secret, device string, onSession func(*Channel)) {
	if open == nil {
		return
	}
	for ctx.Err() == nil {
		conn, release, err := open(ctx, bridgeMethod, map[string]any{"register": true, "device": device})
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		head := make([]byte, 1)
		if _, err := io.ReadFull(conn, head); err != nil {
			_ = conn.SetReadDeadline(time.Time{})
			release()
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		_ = conn.SetReadDeadline(time.Time{})
		sess, err := NewYamuxServer(conn)
		if err != nil {
			release()
			continue
		}
		channel := NewRelayChannel(WrapYamux(sess), secret, "relay")
		if onSession != nil {
			onSession(channel)
		}
		// Serve until the bridge goes away.
		<-sess.CloseChan()
		release()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
