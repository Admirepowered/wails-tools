package miproxy

import (
	"strconv"
	"strings"
)

// parseClashYAML extracts nodes from a Clash-style "proxies:" block. Only the
// protocols this engine implements are kept (vmess/vless/trojan/ss/http/socks5).
func parseClashYAML(body string) []Node {
	lines := strings.Split(body, "\n")
	inProxies := false
	var nodes []Node
	var current map[string]string

	flush := func() {
		if current == nil {
			return
		}
		if node, ok := nodeFromClashMap(current); ok {
			nodes = append(nodes, node)
		}
		current = nil
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !inProxies {
			if strings.HasPrefix(trimmed, "proxies:") {
				inProxies = true
			}
			continue
		}
		// A top-level key ends the proxies block.
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(trimmed, "-") {
			break
		}
		if strings.HasPrefix(trimmed, "-") {
			flush()
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if strings.HasPrefix(rest, "{") {
				current = parseClashInlineMap(rest)
				flush()
				continue
			}
			current = map[string]string{}
			if k, v, ok := splitYAMLKV(rest); ok {
				current[k] = v
			}
			continue
		}
		if current != nil {
			if k, v, ok := splitYAMLKV(trimmed); ok {
				current[k] = v
			}
		}
	}
	flush()
	return nodes
}

func nodeFromClashMap(m map[string]string) (Node, bool) {
	server := strings.TrimSpace(m["server"])
	if server == "" {
		return Node{}, false
	}
	port, _ := strconv.Atoi(strings.TrimSpace(m["port"]))
	if port <= 0 {
		return Node{}, false
	}
	name := strings.TrimSpace(m["name"])
	if name == "" {
		name = server
	}
	network := normalizeNetwork(m["network"])
	node := Node{Name: name, Server: server, Port: port, Network: network}

	kind := strings.ToLower(strings.TrimSpace(m["type"]))
	switch kind {
	case "http", "https":
		node.Protocol = ProtoHTTP
		node.Username = m["username"]
		node.Password = m["password"]
	case "socks5", "socks5h", "socks":
		node.Protocol = ProtoSOCKS5
		node.Username = m["username"]
		node.Password = m["password"]
	case "vmess":
		node.Protocol = ProtoVMess
		node.UUID = strings.TrimSpace(m["uuid"])
		node.AlterID, _ = strconv.Atoi(strings.TrimSpace(m["alterId"]))
		node.Security = strings.ToLower(strings.TrimSpace(m["cipher"]))
		node.TLS = boolish(m["tls"])
	case "vless":
		node.Protocol = ProtoVLESS
		node.UUID = strings.TrimSpace(m["uuid"])
		node.Flow = strings.TrimSpace(m["flow"])
		node.TLS = boolish(m["tls"])
	case "trojan":
		node.Protocol = ProtoTrojan
		node.Password = m["password"]
		node.TLS = true
	case "ss", "shadowsocks":
		node.Protocol = ProtoSS
		node.Method = strings.ToLower(strings.TrimSpace(m["cipher"]))
		node.Password = m["password"]
	default:
		return Node{}, false
	}

	if wsOpts, ok := m["ws-opts"]; ok && wsOpts != "" {
		_ = wsOpts
	}
	path := strings.TrimSpace(m["ws-path"])
	if path == "" {
		path = strings.TrimSpace(m["path"])
	}
	if path != "" {
		node.Path = path
		node.Network = NetWS
	}
	host := strings.TrimSpace(m["ws-host"])
	if host == "" {
		host = strings.TrimSpace(m["host"])
	}
	node.Host = host
	node.SNI = strings.TrimSpace(m["servername"])
	if node.Network == NetWS && node.Path == "" {
		node.Path = "/"
	}
	return node, true
}

func boolish(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "tls", "1", "yes":
		return true
	}
	return false
}

func parseClashInlineMap(s string) map[string]string {
	out := map[string]string{}
	s = strings.TrimPrefix(strings.TrimSpace(s), "{")
	s = strings.TrimSuffix(s, "}")
	for _, part := range splitTopLevelCommas(s) {
		if k, v, ok := splitYAMLKV(part); ok {
			out[k] = v
		}
	}
	return out
}

func splitTopLevelCommas(s string) []string {
	var out []string
	depth := 0
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, b.String())
				b.Reset()
				continue
			}
		}
		b.WriteRune(r)
	}
	if strings.TrimSpace(b.String()) != "" {
		out = append(out, b.String())
	}
	return out
}

func splitYAMLKV(s string) (string, string, bool) {
	idx := strings.Index(s, ":")
	if idx <= 0 {
		return "", "", false
	}
	key := strings.ToLower(strings.TrimSpace(s[:idx]))
	value := strings.TrimSpace(s[idx+1:])
	value = strings.Trim(value, `"'`)
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}
