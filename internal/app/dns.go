package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wailstools/internal/dnsx"
	"wailstools/internal/miproxy"
)

// DNS resolution control.
//
// Node hostnames and every destination that must be dialled without the proxy are
// resolved by this resolver instead of the local one. In "doh" mode there is no
// fallback to the local resolver — that is the whole point.
//
// The configuration lives in its own file (like proxy-chain.json and
// direct-rules.json) so a whole-settings save from the UI cannot wipe it.

// dnsConfig is the on-disk shape of dns.json.
type dnsConfig struct {
	Mode    string   `json:"mode"`
	Servers []string `json:"servers"`
}

func (a *App) dnsFile() string { return filepath.Join(a.rootDir, "dns.json") }

// loadDNSConfig reads dns.json, migrating the legacy settings fields once.
func (a *App) loadDNSConfig() dnsConfig {
	var cfg dnsConfig
	if data, err := os.ReadFile(a.dnsFile()); err == nil && len(data) > 0 {
		if json.Unmarshal(data, &cfg) == nil {
			if cfg.Mode == "" {
				cfg.Mode = string(dnsx.ModeSystem)
			}
			return cfg
		}
	}
	legacy := a.settings.Get().ProxyRuntime
	cfg = dnsConfig{Mode: legacy.DNSMode, Servers: legacy.DNSServers}
	if cfg.Mode == "" {
		cfg.Mode = string(dnsx.ModeSystem)
	}
	if len(cfg.Servers) > 0 || legacy.DNSMode != "" {
		_ = a.saveDNSConfig(cfg)
	}
	return cfg
}

func (a *App) saveDNSConfig(cfg dnsConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.dnsFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.dnsFile())
}

// dnsResolver builds (once) the resolver from dns.json.
func (a *App) dnsResolver() *dnsx.Resolver {
	a.dnsOnce.Do(func() {
		cfg := a.loadDNSConfig()
		a.dnsRes = dnsx.New(dnsMode(cfg.Mode), parseDNSServers(cfg.Servers), a.p2pLogf)
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

func parseDNSServers(raw []string) []dnsx.Server {
	out := make([]dnsx.Server, 0, len(raw))
	for _, item := range raw {
		srv, err := dnsx.ParseServer(item)
		if err != nil {
			continue
		}
		out = append(out, srv)
	}
	return out
}

// applyDNSResolver installs the resolver into the proxy dial path.
func (a *App) applyDNSResolver() {
	res := a.dnsRes
	if res == nil || res.Mode() != dnsx.ModeDoH {
		miproxy.HostResolver = nil
		return
	}
	miproxy.HostResolver = res.Lookup
}

// reloadDNS rebuilds the active resolver from dns.json.
func (a *App) reloadDNS() error {
	cfg := a.loadDNSConfig()
	res := a.dnsResolver()
	res.SetServers(dnsMode(cfg.Mode), parseDNSServers(cfg.Servers))
	a.applyDNSResolver()
	a.p2pLogf("dns: reloaded mode=%s servers=%d", res.Mode(), len(cfg.Servers))
	return nil
}

// DNSStatus reports the current configuration.
func (a *App) DNSStatus() (map[string]any, error) {
	res := a.dnsResolver()
	cfg := a.loadDNSConfig()
	servers := make([]map[string]any, 0, len(cfg.Servers))
	for _, raw := range cfg.Servers {
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
	return map[string]any{"mode": string(res.Mode()), "servers": servers}, nil
}

// DNSSetMode switches between the system resolver and DoH.
func (a *App) DNSSetMode(mode string) (map[string]any, error) {
	if mode != string(dnsx.ModeSystem) && mode != string(dnsx.ModeDoH) {
		return nil, errors.New("mode 只能是 system 或 doh")
	}
	cfg := a.loadDNSConfig()
	cfg.Mode = mode
	if err := a.saveDNSConfig(cfg); err != nil {
		return nil, err
	}
	if err := a.reloadDNS(); err != nil {
		return nil, err
	}
	return a.DNSStatus()
}

// DNSAddServer appends an sdns:// stamp or https:// DoH URL.
func (a *App) DNSAddServer(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("请输入 sdns:// 或 https:// 地址")
	}
	if _, err := dnsx.ParseServer(raw); err != nil {
		return nil, err
	}
	cfg := a.loadDNSConfig()
	for _, existing := range cfg.Servers {
		if strings.TrimSpace(existing) == raw {
			cfg.Mode = string(dnsx.ModeDoH)
			if err := a.saveDNSConfig(cfg); err != nil {
				return nil, err
			}
			_ = a.reloadDNS()
			return a.DNSStatus()
		}
	}
	cfg.Servers = append(cfg.Servers, raw)
	cfg.Mode = string(dnsx.ModeDoH)
	if err := a.saveDNSConfig(cfg); err != nil {
		return nil, err
	}
	if err := a.reloadDNS(); err != nil {
		return nil, err
	}
	a.p2pLogf("dns: added %s (total %d)", raw, len(cfg.Servers))
	return a.DNSStatus()
}

// DNSRemoveServer removes one entry (exact match on the stored value).
func (a *App) DNSRemoveServer(raw string) (map[string]any, error) {
	cfg := a.loadDNSConfig()
	out := make([]string, 0, len(cfg.Servers))
	for _, item := range cfg.Servers {
		if strings.TrimSpace(item) != strings.TrimSpace(raw) {
			out = append(out, item)
		}
	}
	cfg.Servers = out
	if err := a.saveDNSConfig(cfg); err != nil {
		return nil, err
	}
	if err := a.reloadDNS(); err != nil {
		return nil, err
	}
	return a.DNSStatus()
}

// DNSTest queries a server (without saving it) so the user can verify a stamp.
func (a *App) DNSTest(raw string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
