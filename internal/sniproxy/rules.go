// Package sniproxy implements a local SNI proxy: clients connect through it and
// the proxy re-originates TLS to a pinned/resolved IP while keeping the real
// SNI and Host. Domains listed in the rules are handled locally (optionally
// with a MITM certificate signed by a locally generated root CA); everything
// else is forwarded through the app's existing upstream proxy.
package sniproxy

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// Rule is one parsed domain entry.
type Rule struct {
	// Host is the lower-case domain without a leading dot.
	Host string
	// Suffix means the entry also matches any sub-domain.
	Suffix bool
	// IPs are the pinned addresses (may be empty).
	IPs []string
	// Via is another domain/alias whose IPs are reused.
	Via string
	// Raw keeps the original host for the name/alias table.
	Raw bool
	// Passthrough forces a raw tunnel instead of MITM. The client's own
	// handshake (including its SNI) is forwarded unchanged.
	Passthrough bool
}

// RuleSet is the compiled allowlist.
type RuleSet struct {
	exact  map[string]Rule
	suffix []Rule
	// names are plain "alias = ip" definitions that are not domains.
	names map[string][]string
}

// Match reports whether the host is covered by the rules.
func (rs *RuleSet) Match(host string) (Rule, bool) {
	if rs == nil {
		return Rule{}, false
	}
	host = normalizeHost(host)
	if r, ok := rs.exact[host]; ok {
		return r.resolveAlias(rs), true
	}
	for _, r := range rs.suffix {
		if host == r.Host || strings.HasSuffix(host, "."+r.Host) {
			return r.resolveAlias(rs), true
		}
	}
	return Rule{}, false
}

func (r Rule) resolveAlias(rs *RuleSet) Rule {
	if len(r.IPs) > 0 || r.Via == "" {
		return r
	}
	via := normalizeHost(r.Via)
	if ips, ok := rs.names[via]; ok && len(ips) > 0 {
		r.IPs = append([]string(nil), ips...)
		return r
	}
	if entry, ok := rs.exact[via]; ok && len(entry.IPs) > 0 {
		r.IPs = append([]string(nil), entry.IPs...)
		return r
	}
	for _, entry := range rs.suffix {
		if entry.Host == via && len(entry.IPs) > 0 {
			r.IPs = append([]string(nil), entry.IPs...)
			return r
		}
	}
	return r
}

// ParseRules compiles the text rule list.
//
// Formats:
//
//	github.com                      domain, resolve through DoH
//	.githubassets.com               suffix match
//	api.github.com=140.82.112.5     domain with a pinned IP
//	.twitter.com=[tw1]              reuse another entry's IP
//	tw1=104.244.42.13               plain alias (not a domain itself)
//	!example.com                    listed but never MITM (raw tunnel)
func ParseRules(text string) *RuleSet {
	rs := &RuleSet{exact: map[string]Rule{}, names: map[string][]string{}}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, "#"); i > 0 {
			line = strings.TrimSpace(line[:i])
		}
		passthrough := strings.HasPrefix(line, "!")
		line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
		if line == "" {
			continue
		}

		name := line
		value := ""
		hasValue := false
		if i := strings.Index(line, "="); i >= 0 {
			name = strings.TrimSpace(line[:i])
			value = strings.TrimSpace(line[i+1:])
			hasValue = true
		}
		if name == "" {
			continue
		}
		host, suffix := splitHost(name)

		var ips []string
		via := ""
		if hasValue {
			for _, part := range strings.Split(value, ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
					via = strings.TrimSpace(strings.Trim(part, "[]"))
					continue
				}
				if net.ParseIP(part) != nil {
					ips = append(ips, part)
					continue
				}
				via = part
			}
		}

		// A bare "name = ip" line without a dot is an alias table entry.
		if hasValue && suffix == false && !strings.Contains(host, ".") && via == "" {
			rs.names[host] = ips
			rs.exact[host] = Rule{Host: host, IPs: ips, Raw: true, Passthrough: passthrough}
			continue
		}
		if hasValue && via != "" && suffix == false && !strings.Contains(host, ".") {
			rs.names[host] = nil
		}

		// Domains always match their sub-domains as well (a leading dot is
		// accepted for compatibility but does not change the behaviour).
		rule := Rule{Host: host, Suffix: suffix || strings.Contains(host, "."), IPs: ips, Via: via, Passthrough: passthrough}
		rs.suffix = append(rs.suffix, rule)
	}
	sort.SliceStable(rs.suffix, func(i, j int) bool { return len(rs.suffix[i].Host) > len(rs.suffix[j].Host) })
	return rs
}

func splitHost(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	suffix := strings.HasPrefix(name, ".")
	return normalizeHost(name), suffix
}

func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, ".")
	host = strings.TrimSuffix(host, ".")
	return host
}

// HostOf strips the port and default port from an authority.
func HostOf(hostport string) (string, string) {
	hostport = strings.TrimSpace(hostport)
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return normalizeHost(hostport), "443"
	}
	if port == "" {
		port = "443"
	}
	return normalizeHost(host), port
}

// DefaultRules is the built-in allowlist: GitHub, Wikipedia, HuggingFace,
// Twitter/X, Pixiv and a few commonly blocked sites.
const DefaultRules = `# SNI 代理规则
# 域名 / .子域 / 域名 = 固定IP（可多个，逗号分隔）/ 别名 = [别名]
# 不带 IP 的条目用 DoH 解析并缓存 3 天；! 前缀表示只换 IP、不做 MITM

# ---github---
github.com
.github.com
githubassets.com
.githubassets.com
github.githubassets.com
.github.io
api.github.com
raw.githubusercontent.com=185.199.108.133,185.199.109.133,185.199.110.133,185.199.111.133
objects.githubusercontent.com=185.199.108.133,185.199.109.133
release-assets.githubusercontent.com=185.199.108.133,185.199.109.133
avatars.githubusercontent.com=185.199.108.133
githubusercontent.com=185.199.108.133,185.199.109.133

# --- Wikipedia ---
wiki=208.80.153.224
wikipedia.com=208.80.153.224
.wikipedia.com=[wiki]
wikipedia.org=[wiki]
.wikipedia.org=[wiki]
wikinews.org=[wiki]
.wikinews.org=[wiki]
wikidata.org=[wiki]
.wikidata.org=[wiki]
wiktionary.org=[wiki]
.wiktionary.org=[wiki]
wikiquote.org=[wiki]
.wikiquote.org=[wiki]
wikibooks.org=[wiki]
.wikibooks.org=[wiki]
wikisource.org=[wiki]
.wikisource.org=[wiki]
wikiversity.org=[wiki]
.wikiversity.org=[wiki]
wikivoyage.org=[wiki]
.wikivoyage.org=[wiki]
mediawiki.org=[wiki]
.mediawiki.org=[wiki]
wikimedia.org=[wiki]
.wikimedia.org=[wiki]
upload.wikimedia.org=198.35.26.112,208.80.153.240
maps.wikimedia.org=198.35.26.112,208.80.154.240

# --- HuggingFace ---
huggingface.co=18.239.50.49
.huggingface.co
cas-bridge.xethub.hf.co

# --- Twitter / X ---
tw1=104.244.42.13
x.com=[tw1]
.x.com=[tw1]
abs.twimg.com=93.184.220.70
abs-0.twimg.com
pbs.twimg.com=151.101.244.159

# --- Pixiv ---
pi=210.140.139.133
pixiv.net=[pi]
.pixiv.net=[pi]
.pximg.net=[pi]
# --- Discord ---
# 只放"能靠无 SNI 直连"的站点；CDN 域名（dl2.discordapp.net / cdn.discordapp.com
# 等）依赖 Cloudflare 按 SNI 选 zone，写进来会 403，交给代理链路即可。
discord.com=162.159.128.233
.discord.com=162.159.128.233

# Others
dropboxapi.com
vercel.app=76.76.21.98
.nodeseek.com
nslookup.io
.nslookup.io
.openai.com
chatgpt.com
.chatgpt.com
steamcommunity.com
`

// Validate checks a host before it is used as a certificate name.
func Validate(host string) error {
	host = normalizeHost(host)
	if host == "" {
		return fmt.Errorf("域名不能为空")
	}
	for _, r := range host {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			continue
		}
		return fmt.Errorf("域名包含非法字符: %s", host)
	}
	return nil
}
