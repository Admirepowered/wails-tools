package p2p

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"wailstools/internal/wire"
)

// Method names exposed by the peer over a punched channel. File management is
// reserved for a later release; unknown methods fail with "unknown_method".
const (
	// MethodDialTCP is the streaming method used to open a TCP path on the peer.
	MethodDialTCP = "p2p.tcp"
)

// Channel is one authenticated direct connection to another device.
type Channel struct {
	conn   *quic.Conn
	secret string
	peer   string

	mu     sync.Mutex
	closed bool
}

// NewChannel wraps an authenticated QUIC connection.
func NewChannel(conn *quic.Conn, secret, peer string) *Channel {
	return &Channel{conn: conn, secret: secret, peer: peer}
}

// Peer reports the peer's public endpoint.
func (c *Channel) Peer() string { return c.peer }

// Done closes when the underlying QUIC connection goes away (network change,
// peer restart, idle timeout).
func (c *Channel) Done() <-chan struct{} { return c.conn.Context().Done() }

// Close tears the direct path down.
func (c *Channel) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	return c.conn.CloseWithError(0, "closed")
}

// Serve accepts every stream the peer opens and dispatches it through the wire
// handler (p2p.tcp today, file methods later).
func (c *Channel) Serve(ctx context.Context, handler wire.Handler) {
	for {
		stream, err := c.conn.AcceptStream(ctx)
		if err != nil {
			return
		}
		go func(s *quic.Stream) {
			conn := wrapStream(c.conn, s)
			auth := func(h wire.Hello) (string, error) {
				if h.Token != token(c.secret, "wire") {
					return "", errors.New("p2p: bad channel token")
				}
				return "p2p", nil
			}
			_ = wire.Serve(conn, auth, handler)
			_ = s.Close()
		}(stream)
	}
}

// DialTCP asks the peer to connect to target and returns the raw byte stream.
func (c *Channel) DialTCP(ctx context.Context, target string) (net.Conn, error) {
	openCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stream, err := c.conn.OpenStreamSync(openCtx)
	if err != nil {
		return nil, err
	}
	conn := wrapStream(c.conn, stream)
	client, err := wire.NewClient(conn, wire.Hello{
		Token:   token(c.secret, "wire"),
		App:     "wails-tools",
		Purpose: "p2p",
	})
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if err := client.OpenStream(MethodDialTCP, map[string]any{"target": target}); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return conn, nil
}

// Call performs one request/response RPC on a fresh stream.
func (c *Channel) Call(ctx context.Context, method string, body any, out any) error {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := c.conn.OpenStreamSync(callCtx)
	if err != nil {
		return err
	}
	defer stream.Close()
	conn := wrapStream(c.conn, stream)
	client, err := wire.NewClient(conn, wire.Hello{
		Token: token(c.secret, "wire"), App: "wails-tools", Purpose: "p2p",
	})
	if err != nil {
		return err
	}
	return client.Call(method, body, out)
}

// OpenStream opens a raw byte stream for a streaming method.
func (c *Channel) OpenStream(ctx context.Context, method string, body any) (net.Conn, error) {
	openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := c.conn.OpenStreamSync(openCtx)
	if err != nil {
		return nil, err
	}
	conn := wrapStream(c.conn, stream)
	client, err := wire.NewClient(conn, wire.Hello{
		Token: token(c.secret, "wire"), App: "wails-tools", Purpose: "p2p",
	})
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if err := client.OpenStream(method, body); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return conn, nil
}

// Ping verifies the channel is still alive.
func (c *Channel) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stream, err := c.conn.OpenStreamSync(pingCtx)
	if err != nil {
		return err
	}
	defer stream.Close()
	client, err := wire.NewClient(wrapStream(c.conn, stream), wire.Hello{
		Token: token(c.secret, "wire"), Purpose: "p2p",
	})
	if err != nil {
		return err
	}
	var out map[string]any
	return client.Call("p2p.ping", nil, &out)
}

// streamConn adapts a QUIC stream to net.Conn (the stream itself has no
// addresses, which the wire framing wants for logging).
type streamConn struct {
	*quic.Stream
	local  net.Addr
	remote net.Addr
}

func wrapStream(conn *quic.Conn, s *quic.Stream) *streamConn {
	return &streamConn{Stream: s, local: conn.LocalAddr(), remote: conn.RemoteAddr()}
}

func (s *streamConn) LocalAddr() net.Addr  { return s.local }
func (s *streamConn) RemoteAddr() net.Addr { return s.remote }
