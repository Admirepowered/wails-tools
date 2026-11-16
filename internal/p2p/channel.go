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

// Channel is one authenticated connection to another device (punched QUIC, or
// a yamux session bridged through the sync server).
type Channel struct {
	conn    Session
	secret  string
	peer    string
	relay   bool
	release func()

	mu     sync.Mutex
	closed bool
}

// NewChannel wraps an authenticated QUIC connection.
func NewChannel(conn *quic.Conn, secret, peer string) *Channel {
	return &Channel{conn: WrapQuic(conn), secret: secret, peer: peer}
}

// NewRelayChannel wraps a relayed (server-bridged) session.
func NewRelayChannel(sess Session, secret, peer string) *Channel {
	return &Channel{conn: sess, secret: secret, peer: peer, relay: true}
}

// Relayed reports whether this channel goes through the sync server.
func (c *Channel) Relayed() bool { return c.relay }

// Peer reports the peer's public endpoint.
func (c *Channel) Peer() string { return c.peer }

// Done closes when the underlying QUIC connection goes away (network change,
// peer restart, idle timeout).
func (c *Channel) Done() <-chan struct{} { return c.conn.Done() }

// Close tears the direct path down.
func (c *Channel) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	err := c.conn.CloseWithError(0, "closed")
	if c.release != nil {
		c.release()
	}
	return err
}

// Serve accepts every stream the peer opens and dispatches it through the wire
// handler (p2p.tcp today, file methods later).
func (c *Channel) Serve(ctx context.Context, handler wire.Handler) {
	for {
		stream, err := c.conn.AcceptStream(ctx)
		if err != nil {
			return
		}
		go func(s net.Conn) {
			auth := func(h wire.Hello) (string, error) {
				if h.Token != token(c.secret, "wire") {
					return "", errors.New("p2p: bad channel token")
				}
				return "p2p", nil
			}
			_ = wire.Serve(s, auth, handler)
			_ = s.Close()
		}(stream)
	}
}

// DialTCP asks the peer to connect to target and returns the raw byte stream.
func (c *Channel) DialTCP(ctx context.Context, target string) (net.Conn, error) {
	openCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stream, err := c.conn.OpenStream(openCtx)
	if err != nil {
		return nil, err
	}
	conn := stream
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
	stream, err := c.conn.OpenStream(callCtx)
	if err != nil {
		return err
	}
	defer stream.Close()
	conn := stream
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
	stream, err := c.conn.OpenStream(openCtx)
	if err != nil {
		return nil, err
	}
	conn := stream
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
	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	stream, err := c.conn.OpenStream(pingCtx)
	if err != nil {
		return err
	}
	defer stream.Close()
	client, err := wire.NewClient(stream, wire.Hello{
		Token: token(c.secret, "wire"), Purpose: "p2p",
	})
	if err != nil {
		return err
	}
	var out map[string]any
	return client.Call("p2p.ping", nil, &out)
}

// Alive reports whether the underlying QUIC connection is still usable.
func (c *Channel) Alive() bool {
	select {
	case <-c.conn.Done():
		return false
	default:
		return true
	}
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
