package miproxy

import (
	"encoding/base64"
	"sort"
	"strings"
)

// ParseSubscription extracts nodes from a subscription body. It accepts plain
// link lists, base64-wrapped lists (the common V2Board/SSPanel format) and
// mixed content.
func ParseSubscription(body string) []Node {
	body = strings.TrimPrefix(strings.TrimSpace(body), "\ufeff")
	if body == "" {
		return nil
	}
	nodes := parseLinkLines(body)
	if len(nodes) == 0 && strings.Contains(body, "proxies:") {
		nodes = parseClashYAML(body)
	}
	if len(nodes) == 0 {
		if decoded, ok := decodeSubscriptionBase64(body); ok {
			nodes = parseLinkLines(decoded)
			if len(nodes) == 0 && strings.Contains(decoded, "proxies:") {
				nodes = parseClashYAML(decoded)
			}
		}
	}
	// De-duplicate by protocol/server/port/uuid/name so repeated entries (for
	// example the informational pseudo-nodes) collapse.
	seen := map[string]bool{}
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		key := strings.Join([]string{
			string(n.Protocol), n.Server, itoa(n.Port), n.UUID, n.Password, n.Name,
		}, "|")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func parseLinkLines(text string) []Node {
	var out []Node
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if node, ok := ParseLink(line); ok {
			out = append(out, node)
		}
	}
	return out
}

func decodeSubscriptionBase64(body string) (string, bool) {
	compact := strings.Join(strings.Fields(body), "")
	if len(compact) < 16 {
		return "", false
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := enc.DecodeString(compact); err == nil {
			text := string(decoded)
			if strings.Contains(text, "://") {
				return text, true
			}
		}
	}
	return "", false
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
