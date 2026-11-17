package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"wailstools/internal/dnsx"
	"wailstools/internal/miproxy"
	"wailstools/internal/settings"
)

// DNS resolution control.
//
// Node hostnames and any destination that must be dialled without the proxy are
// resolved by this resolver instead of the local one. In "doh" mode there is no
// fallback to the local resolver — that is the whole point.

// dnsResolver builds (once) the resolver from the current settings.
func (a *App) dnsResolver() *dnsx.Resolver {
	a.dnsOnce.Do(func() {
		cfg := a.settings.Get().ProxyRuntime
		servers := parseDNSServers(cfg.DNSServers, a.p2pLogf)
		for _, raw := range cfg.DNSServers {
			if _, err := dnsx.ParseServer(raw); err != nil {
				a.p2pLogf("dns: ignore invalid server %q: %v", raw, err)
			}
		}
		a.dnsRes = dnsx.New(dnsMode(cfg.DNSMode), servers, a.p2pLogf)
		activeResolver = a.dnsRes
		a.applyDNSResolver()
	})
	return a.dnsRes
}

func dnsMode(mode string) dnsx.Mode {
	if strings.EqualFold(strings.TrimSpace(mode), string(dnsx.ModeDoH)) {
		return dnsx.ModeDoH
	}
	return dnsx.ModeSystem
}

func parseDNSServers(raw []string, logf func(string, ...any)) []dnsx.Server {
	out := make([]dnsx.Server, 0, len(raw))
	for _, item := range raw {
		srv, err := dnsx.ParseServer(item)
		if err != nil {
			if logf != nil {
				logf("dns: skip %q: %v", item, err)
			}
			continue
		}
		out = append(out, srv)
	}
	return out
}

// applyDNSResolver installs the resolver into the proxy dial path.
func (a *App) applyDNSResolver() {
	res := a.dnsRes
	if res == nil {
		miproxy.HostResolver = nil
		return
	}
	if res.Mode() == dnsx.ModeDoH {
		miproxy.HostResolver = res.Lookup
		return
	}
	miproxy.HostResolver = nil
}

// reloadDNS re-reads the settings after the user edits them.
func (a *App) reloadDNS() {
	cfg := a.settings.Get().ProxyRuntime
	servers := parseDNSServers(cfg.DNSServers, a.p2pLogf)
	res := a.dnsResolver()
	res.SetServers(dnsMode(cfg.DNSMode), servers)
	a.applyDNSResolver()
}

// DNSStatus reports the current configuration.
func (a *App) DNSStatus() (map[string]any, error) {
	res := a.dnsResolver()
	cfg := a.settings.Get().ProxyRuntime
	servers := []map[string]any{}
	for _, raw := range cfg.DNSServers {
		srv, err := dnsx.ParseServer(raw)
		if err != nil {
			servers = append(servers, map[string]any{"raw": raw, "error": err.Error()})
			continue
		}
		servers = append(servers, map[string]any{
			"raw": srv.Raw, "name": srv.Name, "host": srv.Host,
			"path": srv.Path, "addr": srv.Addr, "endpoint": srv.Endpoint(),
		})
	}
	return map[string]any{
		"mode":    string(res.Mode()),
		"servers": servers,
	}, nil
}

// DNSSetMode switches between the system resolver and DoH.
func (a *App) DNSSetMode(mode string) (map[string]any, error) {
	if mode != string(dnsx.ModeSystem) && mode != string(dnsx.ModeDoH) {
		return nil, errors.New("mode 只能是 system 或 doh")
	}
	if _, err := a.settings.Update(func(s *settings.Settings) error {
		s.ProxyRuntime.DNSMode = mode
		return nil
	}); err != nil {
		return nil, err
	}
	a.reloadDNS()
	a.p2pLogf("dns: mode set to %s", mode)
	return a.DNSStatus()
}

// DNSAddServer appends an sdns:// stamp or https:// DoH URL.
func (a *App) DNSAddServer(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("请输入 sdns:// 或 https:// 地址")
	}
	srv, err := dnsx.ParseServer(raw)
	if err != nil {
		return nil, err
	}
	if _, err := a.settings.Update(func(s *settings.Settings) error {
		for _, existing := range s.ProxyRuntime.DNSServers {
			if strings.TrimSpace(existing) == raw {
				return nil
			}
		}
		s.ProxyRuntime.DNSServers = append(s.ProxyRuntime.DNSServers, raw)
		s.ProxyRuntime.DNSMode = string(dnsx.ModeDoH)
		return nil
	}); err != nil {
		return nil, err
	}
	a.reloadDNS()
	a.p2pLogf("dns: added %s (%s)", srv.Endpoint(), srv.Addr)
	return a.DNSStatus()
}

// DNSRemoveServer removes one entry (exact match on the stored value).
func (a *App) DNSRemoveServer(raw string) (map[string]any, error) {
	if _, err := a.settings.Update(func(s *settings.Settings) error {
		out := make([]string, 0, len(s.ProxyRuntime.DNSServers))
		for _, item := range s.ProxyRuntime.DNSServers {
			if strings.TrimSpace(item) != strings.TrimSpace(raw) {
				out = append(out, item)
			}
		}
		s.ProxyRuntime.DNSServers = out
		return nil
	}); err != nil {
		return nil, err
	}
	a.reloadDNS()
	return a.DNSStatus()
}

// DNSTest queries a server (without saving it) so the user can verify a stamp.
func (a *App) DNSTest(raw string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15e9)
	defer cancel()
	return dnsx.Test(ctx, raw)
}

// DNSLookup resolves a name with the active resolver (diagnostics).
func (a *App) DNSLookup(host string) (map[string]any, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, errors.New("请输入域名")
	}
	res := a.dnsResolver()
	ctx, cancel := context.WithTimeout(context.Background(), 15e9)
	defer cancel()
	start := time.Now()
	ips, err := res.LookupAll(ctx, host)
	if err != nil {
		return map[string]any{"host": host, "mode": string(res.Mode()), "error": err.Error()}, nil
	}
	return map[string]any{
		"host": host, "mode": string(res.Mode()), "ips": ips,
		"ms": time.Since(start).Milliseconds(),
	}, nil
}

// DNSClearCache drops cached answers.
func (a *App) DNSClearCache() (map[string]any, error) {
	a.dnsResolver().ClearCache()
	return a.DNSStatus()
}
