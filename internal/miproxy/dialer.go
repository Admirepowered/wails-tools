package miproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// RelayFunc opens a raw TCP stream to target through an external relay (for
// example the WailsTools sync server).
type RelayFunc func(ctx context.Context, target string) (net.Conn, error)

// Dialer opens connections through a node, optionally chained.
type Dialer struct {
	Node Node
	// Chain lists intermediate hops; Node is the final hop. Max two hops total.
	Chain []Node
	// Relay reaches the first hop when set.
	Relay RelayFunc
	// Direct decides whether a host bypasses the proxy entirely.
	Direct func(host string) bool
	// Timeout bounds each dial/handshake.
	Timeout time.Duration
}

// NewDialer builds a dialer for a single node.
func NewDialer(node Node, timeout time.Duration) *Dialer {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Dialer{Node: node, Timeout: timeout}
}

// NewChainDialer builds a dialer that traverses hops in order.
func NewChainDialer(hops []Node, relay RelayFunc, direct func(string) bool, timeout time.Duration) *Dialer {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if len(hops) == 0 {
		return &Dialer{Timeout: timeout, Relay: relay, Direct: direct}
	}
	last := hops[len(hops)-1]
	chain := append([]Node(nil), hops[:len(hops)-1]...)
	return &Dialer{Node: last, Chain: chain, Relay: relay, Direct: direct, Timeout: timeout}
}

// hops returns the full node sequence.
func (d *Dialer) hops() []Node {
	out := make([]Node, 0, len(d.Chain)+1)
	out = append(out, d.Chain...)
	if d.Node.Protocol != "" {
		out = append(out, d.Node)
	}
	return out
}

// Dial connects to target through the configured chain.
func (d *Dialer) Dial(ctx context.Context, target string) (net.Conn, error) {
	hops := d.hops()
	if len(hops) == 0 {
		return (&net.Dialer{Timeout: d.Timeout}).DialContext(ctx, "tcp", target)
	}
	if d.Direct != nil && len(d.Chain) == 0 {
		host, _, err := net.SplitHostPort(target)
		if err == nil && d.Direct(host) {
			return (&net.Dialer{Timeout: d.Timeout}).DialContext(ctx, "tcp", target)
		}
	}
	if hops[0].Protocol == ProtoDirect {
		return (&net.Dialer{Timeout: d.Timeout}).DialContext(ctx, "tcp", target)
	}

	// First hop: plain TCP or through the relay.
	var conn net.Conn
	var err error
	if d.Relay != nil {
		conn, err = d.Relay(ctx, hops[0].Addr())
		if err != nil {
			return nil, fmt.Errorf("relay to %s: %w", hops[0].Addr(), err)
		}
	} else {
		nd := &net.Dialer{Timeout: d.Timeout}
		conn, err = nd.DialContext(ctx, "tcp", hops[0].Addr())
		if err != nil {
			return nil, fmt.Errorf("dial %s: %w", hops[0].Addr(), err)
		}
	}

	stream := conn
	for i, hop := range hops {
		layered, err := d.layer(ctx, stream, hop)
		if err != nil {
			_ = stream.Close()
			return nil, err
		}
		next := target
		if i < len(hops)-1 {
			next = hops[i+1].Addr()
		}
		stream, err = d.handshake(hop, layered, next)
		if err != nil {
			_ = layered.Close()
			return nil, fmt.Errorf("%s hop %d (%s): %w", hop.Protocol, i+1, hop.Addr(), err)
		}
	}
	return stream, nil
}

// layer applies TLS and WebSocket on top of an existing stream.
func (d *Dialer) layer(ctx context.Context, conn net.Conn, n Node) (net.Conn, error) {
	out := conn
	if n.TLS {
		tlsConn := tls.Client(out, buildTLSConfig(n, n.Server))
		hsCtx, cancel := context.WithTimeout(ctx, d.Timeout)
		defer cancel()
		if err := tlsConn.HandshakeContext(hsCtx); err != nil {
			return nil, fmt.Errorf("tls handshake with %s: %w", n.Addr(), err)
		}
		out = tlsConn
	}
	if n.Network == NetWS {
		host := n.Host
		if host == "" {
			host = n.Server
		}
		ws, err := dialWebSocket(out, host, n.Path, nil)
		if err != nil {
			return nil, err
		}
		return ws, nil
	}
	return out, nil
}

func (d *Dialer) handshake(node Node, conn net.Conn, target string) (net.Conn, error) {
	switch node.Protocol {
	case ProtoVMess:
		return newVMessConn(node, conn, target)
	case ProtoVLESS:
		return newVLESSConn(node, conn, target)
	case ProtoTrojan:
		return newTrojanConn(node, conn, target)
	case ProtoSS:
		return newSSConn(node, conn, target)
	case ProtoSOCKS5:
		return newSocks5Upstream(node, conn, target)
	case ProtoHTTP:
		return newHTTPUpstream(node, conn, target)
	case ProtoDirect, "":
		return conn, nil
	default:
		return nil, fmt.Errorf("unsupported protocol %q", node.Protocol)
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

// MatchHost reports whether a host matches a direct rule list (suffix, CIDR or
// plain keyword).
func MatchHost(host string, rules []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	for _, raw := range rules {
		rule := strings.ToLower(strings.TrimSpace(raw))
		if rule == "" || strings.HasPrefix(rule, "#") {
			continue
		}
		if strings.Contains(rule, "/") {
			if ip != nil {
				if _, cidr, err := net.ParseCIDR(rule); err == nil && cidr.Contains(ip) {
					return true
				}
			}
			continue
		}
		rule = strings.TrimPrefix(rule, ".")
		if host == rule || strings.HasSuffix(host, "."+rule) {
			return true
		}
		if ip != nil && ip.Equal(net.ParseIP(rule)) {
			return true
		}
	}
	return false
}
