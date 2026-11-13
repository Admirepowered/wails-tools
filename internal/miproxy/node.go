// Package miproxy is a Go re-implementation of the Pulse/miproxy C proxy core:
// outbound protocols (VMess, VLESS, Trojan, Shadowsocks) plus a local mixed
// SOCKS5/HTTP inbound that can be used by this app or by other programs.
package miproxy

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Protocol identifies an outbound protocol.
type Protocol string

// Supported outbound protocols.
const (
	ProtoVMess  Protocol = "vmess"
	ProtoVLESS  Protocol = "vless"
	ProtoTrojan Protocol = "trojan"
	ProtoSS     Protocol = "shadowsocks"
	ProtoHTTP   Protocol = "http"
	ProtoSOCKS5 Protocol = "socks5"
	ProtoDirect Protocol = "direct"
)

// Network identifies the transport layer.
const (
	NetTCP = "tcp"
	NetWS  = "ws"
)

// Node is one proxy endpoint.
type Node struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Protocol Protocol `json:"protocol"`

	Server string `json:"server"`
	Port   int    `json:"port"`

	// VMess / VLESS
	UUID     string `json:"uuid,omitempty"`
	AlterID  int    `json:"alterId,omitempty"`
	Security string `json:"security,omitempty"`
	Flow     string `json:"flow,omitempty"`

	// Transport
	Network string `json:"network,omitempty"` // tcp | ws
	Path    string `json:"path,omitempty"`
	Host    string `json:"host,omitempty"`
	TLS     bool   `json:"tls,omitempty"`
	SNI     string `json:"sni,omitempty"`
	// SkipVerify disables certificate verification (self-signed servers).
	SkipVerify bool `json:"skipVerify,omitempty"`

	// Trojan / Shadowsocks
	Password string `json:"password,omitempty"`
	Method   string `json:"method,omitempty"`

	// HTTP / SOCKS5 upstream
	Username string `json:"username,omitempty"`

	// Bookkeeping
	SourceID string `json:"sourceId,omitempty"`
	Index    int    `json:"index,omitempty"`
	Raw      string `json:"raw,omitempty"`
}

// Addr returns host:port.
func (n Node) Addr() string {
	host := n.Server
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return net.JoinHostPort(host, strconv.Itoa(n.Port))
}

// Label is a short human readable description.
func (n Node) Label() string {
	if n.Name != "" {
		return n.Name
	}
	return fmt.Sprintf("%s %s", n.Protocol, n.Addr())
}

// IsProxy reports whether the node needs the local proxy engine (anything but
// plain http/socks5/direct upstreams).
func (n Node) IsProxy() bool {
	switch n.Protocol {
	case ProtoVMess, ProtoVLESS, ProtoTrojan, ProtoSS:
		return true
	}
	return false
}

// ParseLink parses one subscription entry into a Node.
func ParseLink(line string) (Node, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
		return Node{}, false
	}
	switch {
	case strings.HasPrefix(line, "vmess://"):
		return parseVMessLink(line)
	case strings.HasPrefix(line, "vless://"):
		return parseVLESSLink(line)
	case strings.HasPrefix(line, "trojan://"):
		return parseTrojanLink(line)
	case strings.HasPrefix(line, "ss://"):
		return parseSSLink(line)
	case strings.HasPrefix(line, "http://"), strings.HasPrefix(line, "https://"),
		strings.HasPrefix(line, "socks5://"), strings.HasPrefix(line, "socks5h://"), strings.HasPrefix(line, "socks://"):
		return parseSimpleProxyLink(line)
	}
	return Node{}, false
}

func splitFragment(raw string) (string, string) {
	if i := strings.Index(raw, "#"); i >= 0 {
		return raw[:i], decodeName(raw[i+1:])
	}
	return raw, ""
}

func decodeName(s string) string {
	if s == "" {
		return ""
	}
	if decoded, err := urlUnescape(s); err == nil {
		return decoded
	}
	return s
}

func defaultPort(p int, protocol Protocol) int {
	if p > 0 {
		return p
	}
	switch protocol {
	case ProtoTrojan, ProtoVLESS, ProtoVMess:
		return 443
	}
	return 0
}

func normalizeNetwork(network string) string {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "", "tcp", "raw":
		return NetTCP
	case "ws", "websocket":
		return NetWS
	default:
		return strings.ToLower(strings.TrimSpace(network))
	}
}
