package subscription

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// Parse extracts nodes and metadata from a subscription body. It understands
// plain URL lists, base64-wrapped lists, Clash YAML and SIP008/Clash JSON.
func Parse(body string) Source {
	src := Source{}
	body = strings.TrimPrefix(strings.TrimSpace(body), "\ufeff")
	if body == "" {
		return src
	}

	// JSON documents first.
	if strings.HasPrefix(body, "{") || strings.HasPrefix(body, "[") {
		if nodes, ok := parseJSON(body); ok {
			src.Nodes = nodes
			return src
		}
	}
	// Clash YAML.
	if strings.Contains(body, "proxies:") || strings.Contains(body, "- {") {
		if nodes, ok := parseClashYAML(body); ok {
			src.Nodes = nodes
			return src
		}
	}
	// Plain or base64 list.
	if nodes := parseURLList(body); len(nodes) > 0 {
		src.Nodes = nodes
		return src
	}
	if decoded, ok := decodeBase64(body); ok {
		if nodes := parseURLList(decoded); len(nodes) > 0 {
			src.Nodes = nodes
			return src
		}
		if nodes, ok := parseClashYAML(decoded); ok {
			src.Nodes = nodes
		}
	}
	return src
}

// ParseNodes is a convenience wrapper returning only the nodes.
func ParseNodes(body string) []Node { return Parse(body).Nodes }

func parseURLList(body string) []Node {
	var nodes []Node
	seen := map[string]bool{}
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") || strings.HasPrefix(line, ";") {
			continue
		}
		node, ok := parseNodeURL(line)
		if !ok {
			continue
		}
		key := node.Scheme + "|" + node.Host + "|" + strconv.Itoa(node.Port) + "|" + node.Username
		if seen[key] {
			continue
		}
		seen[key] = true
		nodes = append(nodes, node)
	}
	return nodes
}

func parseNodeURL(line string) (Node, bool) {
	// Split off a trailing "#name" fragment before parsing.
	name := ""
	if i := strings.Index(line, "#"); i >= 0 {
		name = strings.TrimSpace(line[i+1:])
		line = line[:i]
	}
	u, err := url.Parse(line)
	if err != nil || u.Host == "" {
		return Node{}, false
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https":
		scheme = "http"
	case "socks", "socks5", "socks5h":
		scheme = "socks5"
	default:
		return Node{}, false
	}
	host := u.Hostname()
	port := 0
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	if port == 0 {
		if scheme == "http" {
			port = 8080
		} else {
			port = 1080
		}
	}
	if host == "" {
		return Node{}, false
	}
	node := Node{Name: name, Scheme: scheme, Host: host, Port: port}
	if u.User != nil {
		node.Username = u.User.Username()
		node.Password, _ = u.User.Password()
	}
	if node.Name == "" {
		node.Name = host + ":" + strconv.Itoa(port)
	}
	if decoded, err := url.PathUnescape(node.Name); err == nil {
		node.Name = decoded
	}
	return node, true
}

func decodeBase64(body string) (string, bool) {
	compact := strings.Join(strings.Fields(body), "")
	if len(compact) < 8 {
		return "", false
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		if decoded, err := enc.DecodeString(compact); err == nil {
			text := string(decoded)
			if strings.Contains(text, "://") || strings.Contains(text, "proxies:") {
				return text, true
			}
		}
	}
	return "", false
}

type jsonProxy struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	// SIP008 fields
	Remarks    string `json:"remarks"`
	ServerPort int    `json:"server_port"`
}

func parseJSON(body string) ([]Node, bool) {
	var doc struct {
		Proxies []jsonProxy `json:"proxies"`
		Servers []jsonProxy `json:"servers"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		// Some providers return a bare array of proxies.
		var list []jsonProxy
		if err2 := json.Unmarshal([]byte(body), &list); err2 != nil {
			return nil, false
		}
		doc.Proxies = list
	}
	items := append(doc.Proxies, doc.Servers...)
	var nodes []Node
	seen := map[string]bool{}
	for _, item := range items {
		node, ok := nodeFromProxy(item)
		if !ok {
			continue
		}
		key := node.Scheme + "|" + node.Host + "|" + strconv.Itoa(node.Port)
		if seen[key] {
			continue
		}
		seen[key] = true
		nodes = append(nodes, node)
	}
	return nodes, len(nodes) > 0
}

func nodeFromProxy(item jsonProxy) (Node, bool) {
	kind := strings.ToLower(strings.TrimSpace(item.Type))
	scheme := ""
	switch kind {
	case "http", "https":
		scheme = "http"
	case "socks5", "socks5h", "socks":
		scheme = "socks5"
	case "":
		// SIP008 servers carry no type but are shadowsocks; unusable here.
		return Node{}, false
	default:
		return Node{}, false
	}
	host := strings.TrimSpace(item.Server)
	if host == "" {
		return Node{}, false
	}
	port := item.Port
	if port == 0 {
		port = item.ServerPort
	}
	if port == 0 {
		if scheme == "http" {
			port = 8080
		} else {
			port = 1080
		}
	}
	name := item.Name
	if name == "" {
		name = item.Remarks
	}
	if name == "" {
		name = host + ":" + strconv.Itoa(port)
	}
	return Node{Name: name, Scheme: scheme, Host: host, Port: port, Username: item.Username, Password: item.Password}, true
}

// parseClashYAML handles the common "proxies:" block in both block and inline
// mapping style. Only HTTP(S) and SOCKS5 entries are kept.
func parseClashYAML(body string) ([]Node, bool) {
	lines := strings.Split(body, "\n")
	inProxies := false
	var nodes []Node
	var current map[string]string
	seen := map[string]bool{}

	flush := func() {
		if current == nil {
			return
		}
		item := jsonProxy{
			Name:     current["name"],
			Type:     current["type"],
			Server:   current["server"],
			Username: current["username"],
			Password: current["password"],
		}
		if p, err := strconv.Atoi(strings.TrimSpace(current["port"])); err == nil {
			item.Port = p
		}
		if node, ok := nodeFromProxy(item); ok {
			key := node.Scheme + "|" + node.Host + "|" + strconv.Itoa(node.Port)
			if !seen[key] {
				seen[key] = true
				nodes = append(nodes, node)
			}
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
		// Any top-level key ends the proxies block.
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(trimmed, "-") {
			break
		}
		if strings.HasPrefix(trimmed, "-") {
			flush()
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if strings.HasPrefix(rest, "{") {
				current = parseInlineMap(rest)
				flush()
				continue
			}
			current = map[string]string{}
			if rest != "" {
				if k, v, ok := splitYAMLKV(rest); ok {
					current[k] = v
				}
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
	return nodes, len(nodes) > 0
}

func parseInlineMap(s string) map[string]string {
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
