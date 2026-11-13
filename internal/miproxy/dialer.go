package miproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"
)

// Dialer opens connections through a node.
type Dialer struct {
	Node Node
	// Timeout bounds the TCP connect + handshake.
	Timeout time.Duration
}

// NewDialer builds a dialer for a node.
func NewDialer(node Node, timeout time.Duration) *Dialer {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Dialer{Node: node, Timeout: timeout}
}

// Dial connects to target through the node and returns a stream to it.
func (d *Dialer) Dial(ctx context.Context, target string) (net.Conn, error) {
	if d.Node.Protocol == ProtoDirect || d.Node.Protocol == "" {
		nd := &net.Dialer{Timeout: d.Timeout}
		return nd.DialContext(ctx, "tcp", target)
	}
	conn, err := d.dialTransport(ctx)
	if err != nil {
		return nil, err
	}
	stream, err := d.handshake(conn, target)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return stream, nil
}

// dialTransport opens the raw TCP/TLS/WebSocket connection to the node.
func (d *Dialer) dialTransport(ctx context.Context) (net.Conn, error) {
	n := d.Node
	netDialer := &net.Dialer{Timeout: d.Timeout}
	raw, err := netDialer.DialContext(ctx, "tcp", n.Addr())
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", n.Addr(), err)
	}
	if tc, ok := raw.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
	}

	conn := raw
	if n.TLS {
		tlsConn := tls.Client(raw, buildTLSConfig(n, n.Server))
		hsCtx, cancel := context.WithTimeout(ctx, d.Timeout)
		defer cancel()
		if err := tlsConn.HandshakeContext(hsCtx); err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("tls handshake: %w", err)
		}
		conn = tlsConn
	}
	if n.Network == NetWS {
		host := n.Host
		if host == "" {
			host = n.Server
		}
		ws, err := dialWebSocket(conn, host, n.Path, nil)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return ws, nil
	}
	return conn, nil
}

func (d *Dialer) handshake(conn net.Conn, target string) (net.Conn, error) {
	switch d.Node.Protocol {
	case ProtoVMess:
		return newVMessConn(d.Node, conn, target)
	case ProtoVLESS:
		return newVLESSConn(d.Node, conn, target)
	case ProtoTrojan:
		return newTrojanConn(d.Node, conn, target)
	case ProtoSS:
		return newSSConn(d.Node, conn, target)
	case ProtoSOCKS5:
		return newSocks5Upstream(d.Node, conn, target)
	case ProtoHTTP:
		return newHTTPUpstream(d.Node, conn, target)
	case ProtoDirect, "":
		return conn, nil
	default:
		return nil, fmt.Errorf("unsupported protocol %q", d.Node.Protocol)
	}
}

// Probe verifies that a node can reach the internet by fetching a tiny URL.
func (d *Dialer) Probe(ctx context.Context, probeAddr string) (time.Duration, error) {
	if probeAddr == "" {
		probeAddr = "www.gstatic.com:80"
	}
	start := time.Now()
	conn, err := d.Dial(ctx, probeAddr)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	host, _, _ := net.SplitHostPort(probeAddr)
	req := fmt.Sprintf("HEAD /generate_204 HTTP/1.1\r\nHost: %s\r\nUser-Agent: WailsTools/1.0\r\nConnection: close\r\n\r\n", host)
	_ = conn.SetDeadline(time.Now().Add(12 * time.Second))
	if _, err := conn.Write([]byte(req)); err != nil {
		return 0, err
	}
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	if err != nil {
		return 0, err
	}
	if n < 12 || string(buf[:5]) != "HTTP/" {
		return 0, errors.New("probe: unexpected response")
	}
	return time.Since(start), nil
}
