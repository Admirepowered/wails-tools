package p2p

import (
	"context"
	"net"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/quic-go/quic-go"
)

// Session is the stream multiplexer a Channel runs on: a punched QUIC
// connection, or a yamux session bridged through the sync server (relay mode).
type Session interface {
	OpenStream(ctx context.Context) (net.Conn, error)
	AcceptStream(ctx context.Context) (net.Conn, error)
	CloseWithError(code uint64, msg string) error
	Done() <-chan struct{}
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}

// QuicSession adapts a QUIC connection.
type QuicSession struct{ conn *quic.Conn }

// WrapQuic exposes a QUIC connection as a Session.
func WrapQuic(conn *quic.Conn) *QuicSession { return &QuicSession{conn: conn} }

// Conn returns the underlying QUIC connection.
func (s *QuicSession) Conn() *quic.Conn { return s.conn }

func (s *QuicSession) OpenStream(ctx context.Context) (net.Conn, error) {
	stream, err := s.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return wrapStream(s.conn, stream), nil
}

func (s *QuicSession) AcceptStream(ctx context.Context) (net.Conn, error) {
	stream, err := s.conn.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return wrapStream(s.conn, stream), nil
}

func (s *QuicSession) CloseWithError(code uint64, msg string) error {
	return s.conn.CloseWithError(quic.ApplicationErrorCode(code), msg)
}

func (s *QuicSession) Done() <-chan struct{}       { return s.conn.Context().Done() }
func (s *QuicSession) LocalAddr() net.Addr         { return s.conn.LocalAddr() }
func (s *QuicSession) RemoteAddr() net.Addr        { return s.conn.RemoteAddr() }

// YamuxSession adapts a yamux session carried over a relayed stream.
type YamuxSession struct{ sess *yamux.Session }

// WrapYamux exposes a yamux session as a Session.
func WrapYamux(sess *yamux.Session) *YamuxSession { return &YamuxSession{sess: sess} }

func (s *YamuxSession) OpenStream(ctx context.Context) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := s.sess.OpenStream()
		ch <- result{conn, err}
	}()
	select {
	case r := <-ch:
		return r.conn, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *YamuxSession) AcceptStream(ctx context.Context) (net.Conn, error) {
	stream, err := s.sess.AcceptStreamWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return stream, nil
}

func (s *YamuxSession) CloseWithError(code uint64, msg string) error { return s.sess.Close() }
func (s *YamuxSession) Done() <-chan struct{}                       { return s.sess.CloseChan() }
func (s *YamuxSession) LocalAddr() net.Addr                         { return s.sess.LocalAddr() }
func (s *YamuxSession) RemoteAddr() net.Addr                        { return s.sess.RemoteAddr() }

// NewYamuxClient builds the dialing side of a relayed session.
func NewYamuxClient(conn net.Conn) (*yamux.Session, error) {
	cfg := yamuxConfig()
	return yamux.Client(conn, cfg)
}

// NewYamuxServer builds the listening side of a relayed session.
func NewYamuxServer(conn net.Conn) (*yamux.Session, error) {
	cfg := yamuxConfig()
	return yamux.Server(conn, cfg)
}

// yamuxConfig keeps a relayed session alive through idle periods and NAT
// timeouts (the relay carries a keepalive frame every 10 seconds).
func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.ConnectionWriteTimeout = 30 * time.Second
	cfg.LogOutput = nil
	return cfg
}
