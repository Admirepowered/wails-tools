package subscription

import (
	"strings"

	"wailstools/internal/miproxy"
)

// Parse extracts nodes from a subscription body using the miproxy link parser,
// which understands vmess/vless/trojan/ss and plain http/socks5 entries.
func Parse(body string) Source {
	src := Source{}
	body = strings.TrimPrefix(strings.TrimSpace(body), "\ufeff")
	if body == "" {
		return src
	}
	src.Nodes = miproxy.ParseSubscription(body)
	return src
}

// ParseNodes is a convenience wrapper returning only the nodes.
func ParseNodes(body string) []Node { return Parse(body).Nodes }

// ParseUserInfo parses the "subscription-userinfo" header.
func ParseUserInfo(header string) Info {
	var info Info
	if strings.TrimSpace(header) == "" {
		return info
	}
	for _, part := range strings.Split(header, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		value, err := parseInt(strings.TrimSpace(kv[1]))
		if err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(kv[0])) {
		case "upload":
			info.Upload = value
		case "download":
			info.Download = value
		case "total":
			info.Total = value
		case "expire":
			info.Expire = value
		}
	}
	return info
}

// MergeInfo keeps non-zero values from the incoming info.
func MergeInfo(base, incoming Info) Info {
	if incoming.Upload != 0 {
		base.Upload = incoming.Upload
	}
	if incoming.Download != 0 {
		base.Download = incoming.Download
	}
	if incoming.Total != 0 {
		base.Total = incoming.Total
	}
	if incoming.Expire != 0 {
		base.Expire = incoming.Expire
	}
	return base
}

func parseInt(s string) (int64, error) {
	var v int64
	if s == "" {
		return 0, nil
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errInvalidNumber
		}
		v = v*10 + int64(r-'0')
	}
	return v, nil
}

var errInvalidNumber = &numberError{}

type numberError struct{}

func (*numberError) Error() string { return "invalid number" }
