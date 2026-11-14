package miproxy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

func urlUnescape(s string) (string, error) {
	return url.PathUnescape(s)
}

func b64decode(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	// Tolerate URL-safe alphabets and missing padding.
	encodings := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	}
	var lastErr error
	for _, enc := range encodings {
		if out, err := enc.DecodeString(s); err == nil {
			return out, nil
		} else {
			lastErr = err
		}
	}
	return nil, lastErr
}

// parseVMessLink decodes the v2ray "vmess://<base64 JSON>" format.
func parseVMessLink(raw string) (Node, bool) {
	body, name := splitFragment(strings.TrimPrefix(raw, "vmess://"))
	payload, err := b64decode(body)
	if err != nil {
		return Node{}, false
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return Node{}, false
	}
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				switch t := v.(type) {
				case string:
					if t != "" {
						return t
					}
				case float64:
					return strconv.Itoa(int(t))
				}
			}
		}
		return ""
	}
	port, _ := strconv.Atoi(get("port"))
	node := Node{
		Name:     firstNonEmpty(get("ps", "remarks"), name),
		Protocol: ProtoVMess,
		Server:   get("add"),
		Port:     port,
		UUID:     get("id"),
		Security: strings.ToLower(get("scy", "security")),
		Network:  normalizeNetwork(get("net")),
		Path:     get("path"),
		Host:     get("host"),
		Raw:      raw,
	}
	node.AlterID, _ = strconv.Atoi(get("aid"))
	tlsField := strings.ToLower(get("tls"))
	node.TLS = tlsField == "tls" || tlsField == "true" || tlsField == "1"
	node.SNI = get("sni")
	if node.Server == "" || node.UUID == "" || node.Port <= 0 {
		return Node{}, false
	}
	if node.Network == NetWS && node.Path == "" {
		node.Path = "/"
	}
	return node, true
}

// parseVLESSLink parses vless://uuid@host:port?params#name.
func parseVLESSLink(raw string) (Node, bool) {
	body, name := splitFragment(strings.TrimPrefix(raw, "vless://"))
	u, err := url.Parse("vless://" + body)
	if err != nil || u.Host == "" {
		return Node{}, false
	}
	port, _ := strconv.Atoi(u.Port())
	q := u.Query()
	node := Node{
		Name:     firstNonEmpty(decodeName(name), u.Fragment),
		Protocol: ProtoVLESS,
		Server:   u.Hostname(),
		Port:     defaultPort(port, ProtoVLESS),
		UUID:     u.User.Username(),
		Network:  normalizeNetwork(q.Get("type")),
		Path:     q.Get("path"),
		Host:     q.Get("host"),
		SNI:      firstNonEmpty(q.Get("sni"), q.Get("peer")),
		Flow:     q.Get("flow"),
		Raw:      raw,
	}
	security := strings.ToLower(q.Get("security"))
	node.TLS = security == "tls" || security == "reality" || security == "xtls"
	if node.Server == "" || node.UUID == "" {
		return Node{}, false
	}
	if node.Network == NetWS && node.Path == "" {
		node.Path = "/"
	}
	return node, true
}

// parseTrojanLink parses trojan://password@host:port?params#name.
func parseTrojanLink(raw string) (Node, bool) {
	body, name := splitFragment(strings.TrimPrefix(raw, "trojan://"))
	u, err := url.Parse("trojan://" + body)
	if err != nil || u.Host == "" {
		return Node{}, false
	}
	port, _ := strconv.Atoi(u.Port())
	q := u.Query()
	node := Node{
		Name:     firstNonEmpty(decodeName(name), u.Fragment),
		Protocol: ProtoTrojan,
		Server:   u.Hostname(),
		Port:     defaultPort(port, ProtoTrojan),
		Password: u.User.Username(),
		Network:  normalizeNetwork(q.Get("type")),
		Path:     q.Get("path"),
		Host:     q.Get("host"),
		SNI:      firstNonEmpty(q.Get("sni"), q.Get("peer")),
		Raw:      raw,
	}
	// Trojan is TLS by definition unless explicitly disabled.
	security := strings.ToLower(q.Get("security"))
	node.TLS = security != "none"
	node.SkipVerify = boolParam(q, "allowInsecure", "skipVerify", "insecure")
	if node.Server == "" || node.Password == "" {
		return Node{}, false
	}
	if node.Network == NetWS && node.Path == "" {
		node.Path = "/"
	}
	return node, true
}

// parseSSLink parses both ss://base64(method:password)@host:port and the fully
// base64 encoded form.
func parseSSLink(raw string) (Node, bool) {
	body, name := splitFragment(strings.TrimPrefix(raw, "ss://"))
	body = strings.TrimSuffix(body, "/")

	var userInfo, hostPort string
	if strings.Contains(body, "@") {
		idx := strings.LastIndex(body, "@")
		userInfo = body[:idx]
		hostPort = body[idx+1:]
		if decoded, err := b64decode(userInfo); err == nil {
			userInfo = string(decoded)
		} else {
			if unescaped, err := urlUnescape(userInfo); err == nil {
				userInfo = unescaped
			}
		}
	} else {
		decoded, err := b64decode(body)
		if err != nil {
			return Node{}, false
		}
		text := string(decoded)
		idx := strings.LastIndex(text, "@")
		if idx < 0 {
			return Node{}, false
		}
		userInfo = text[:idx]
		hostPort = text[idx+1:]
	}
	host, portStr, err := splitHostPortLoose(hostPort)
	if err != nil {
		return Node{}, false
	}
	port, _ := strconv.Atoi(portStr)
	sep := strings.Index(userInfo, ":")
	if sep < 0 {
		return Node{}, false
	}
	node := Node{
		Name:     firstNonEmpty(decodeName(name), host),
		Protocol: ProtoSS,
		Server:   host,
		Port:     port,
		Method:   strings.ToLower(strings.TrimSpace(userInfo[:sep])),
		Password: userInfo[sep+1:],
		Network:  NetTCP,
		Raw:      raw,
	}
	if node.Server == "" || node.Method == "" || node.Port <= 0 {
		return Node{}, false
	}
	return node, true
}

// parseSimpleProxyLink handles plain http/https/socks5 upstreams.
func parseSimpleProxyLink(raw string) (Node, bool) {
	body, name := splitFragment(raw)
	u, err := url.Parse(body)
	if err != nil || u.Host == "" {
		return Node{}, false
	}
	port, _ := strconv.Atoi(u.Port())
	protocol := ProtoHTTP
	switch strings.ToLower(u.Scheme) {
	case "socks", "socks5", "socks5h":
		protocol = ProtoSOCKS5
	}
	node := Node{
		Name:     firstNonEmpty(decodeName(name), u.Host),
		Protocol: protocol,
		Server:   u.Hostname(),
		Port:     port,
		Network:  NetTCP,
		Raw:      raw,
	}
	if u.User != nil {
		node.Username = u.User.Username()
		node.Password, _ = u.User.Password()
	}
	if node.Port <= 0 {
		if protocol == ProtoHTTP {
			node.Port = 8080
		} else {
			node.Port = 1080
		}
	}
	return node, node.Server != ""
}

func splitHostPortLoose(s string) (string, string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "//")
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return "", "", errors.New("bad ipv6 host")
		}
		host := s[1:end]
		rest := strings.TrimPrefix(s[end+1:], ":")
		return host, rest, nil
	}
	idx := strings.LastIndex(s, ":")
	if idx < 0 {
		return s, "", nil
	}
	return s[:idx], s[idx+1:], nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// boolParam reports whether any of the named query params is truthy.
func boolParam(q url.Values, names ...string) bool {
	for _, n := range names {
		switch strings.ToLower(strings.TrimSpace(q.Get(n))) {
		case "1", "true", "yes", "tls":
			return true
		}
	}
	return false
}
