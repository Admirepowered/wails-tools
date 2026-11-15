package app

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"

	"wailstools/internal/proxyx"
	"wailstools/internal/settings"
	"wailstools/internal/sniproxy"
)

// sniState owns the running local SNI proxy.
type sniState struct {
	server   *sniproxy.Server
	resolver *sniproxy.Resolver
	ca       *sniproxy.CA
	dir      string
}

// sniDir resolves the SNI proxy data directory (CA + DNS cache).
func (a *App) sniDir() (string, error) {
	dir := filepath.Join(a.rootDir, "sniproxy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// effectiveSNIRules returns the configured rules or the built-in defaults.
func effectiveSNIRules(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return sniproxy.DefaultRules
	}
	return raw
}

// restartSNIProxy applies the current settings: stop the old listener, start a
// new one when enabled.
func (a *App) restartSNIProxy() error {
	if a.sni != nil && a.sni.server != nil {
		_ = a.sni.server.Close()
	}
	state := &sniState{}
	cfg := a.settings.Get().SNIProxy
	if !cfg.Enabled {
		a.sni = state
		return nil
	}
	dir, err := a.sniDir()
	if err != nil {
		return err
	}
	state.dir = dir
	logf := func(format string, args ...any) {
		if a.log != nil {
			a.log.Printf(format, args...)
		}
	}
	state.resolver = sniproxy.NewResolver(dir, logf)
	ca, err := sniproxy.LoadOrCreateCA(dir)
	if err != nil {
		return err
	}
	state.ca = ca

	var upstream func(ctx context.Context, network, addr string) (net.Conn, error)
	if cfg.UseExistingProxy {
		rule := a.settings.Get().Proxy.Resolve(settings.ScopeAll)
		if dialer, derr := proxyx.New(rule); derr == nil && dialer != nil {
			upstream = func(ctx context.Context, network, addr string) (net.Conn, error) {
				if dialer.Bypassed(addr) {
					return (&net.Dialer{Timeout: 15e9}).DialContext(ctx, network, addr)
				}
				return dialer.DialContext(ctx, network, addr)
			}
			logf("sniproxy: upstream uses %s://%s", rule.Mode, rule.URL)
		} else {
			logf("sniproxy: no upstream proxy configured, dialing directly")
		}
	}
	if upstream != nil {
		state.resolver.SetUpstream(upstream)
	}
	server, err := sniproxy.Start(sniproxy.Options{
		Addr:     sniAddrOrDefault(cfg.Addr),
		Rules:    sniproxy.ParseRules(effectiveSNIRules(cfg.Rules)),
		Resolver: state.resolver,
		CA:       ca,
		Mitm:     cfg.Mitm,
		Upstream: upstream,
		Logf:     logf,
	})
	if err != nil {
		a.sni = state
		return err
	}
	state.server = server
	a.sni = state
	a.emit("sniproxy:state", a.SNIProxyStatus())
	return nil
}

// SNIProxyStatus reports the listener state and counters.
func (a *App) SNIProxyStatus() map[string]any {
	cfg := a.settings.Get().SNIProxy
	out := map[string]any{
		"enabled": cfg.Enabled, "addr": cfg.Addr, "mitm": cfg.Mitm,
		"useExistingProxy": cfg.UseExistingProxy, "running": false,
	}
	if a.sni != nil && a.sni.server != nil {
		stats := a.sni.server.Stats()
		out["running"] = true
		out["listen"] = a.sni.server.Addr()
		out["stats"] = map[string]any{
			"active": stats.Active, "total": stats.Total, "mitm": stats.Mitm,
			"passthrough": stats.Passthrough, "upstream": stats.Upstream,
			"failed": stats.Failed, "lastError": stats.LastError,
		}
	}
	if a.sni != nil && a.sni.resolver != nil {
		out["cachedDomains"] = a.sni.resolver.CacheSize()
	}
	if a.sni != nil && a.sni.ca != nil {
		out["caPath"] = a.sni.ca.CertPath()
	}
	return out
}

// GetSNIProxySettings returns the saved settings plus the effective rule text.
func (a *App) GetSNIProxySettings() map[string]any {
	cfg := a.settings.Get().SNIProxy
	rules := cfg.Rules
	if strings.TrimSpace(rules) == "" {
		rules = sniproxy.DefaultRules
	}
	return map[string]any{
		"enabled": cfg.Enabled, "addr": cfg.Addr, "mitm": cfg.Mitm,
		"useExistingProxy": cfg.UseExistingProxy, "rules": rules,
		"rulesAreDefault": strings.TrimSpace(cfg.Rules) == "",
	}
}

// SaveSNIProxySettings persists the settings and restarts the listener.
func (a *App) SaveSNIProxySettings(next settings.SNIProxySettings) (map[string]any, error) {
	next.Addr = strings.TrimSpace(next.Addr)
	if next.Addr == "" {
		next.Addr = "127.0.0.1:17891"
	}
	if _, _, err := net.SplitHostPort(next.Addr); err != nil {
		return nil, errors.New("监听地址格式应为 host:port，例如 127.0.0.1:17891")
	}
	if strings.TrimSpace(next.Rules) == "" {
		next.Rules = sniproxy.DefaultRules
	}
	if _, err := a.settings.Update(func(s *settings.Settings) error {
		s.SNIProxy = next
		return nil
	}); err != nil {
		return nil, err
	}
	if err := a.restartSNIProxy(); err != nil {
		return a.SNIProxyStatus(), err
	}
	return a.SNIProxyStatus(), nil
}

// InstallSNIProxyCA trusts the generated root certificate (current user).
func (a *App) InstallSNIProxyCA() (map[string]any, error) {
	if a.sni == nil || a.sni.ca == nil {
		return nil, errors.New("请先开启 SNI 代理以生成根证书")
	}
	path, err := a.sni.ca.InstallRoot()
	out := map[string]any{"caPath": path}
	if err != nil {
		return out, err
	}
	out["installed"] = true
	return out, nil
}

// SNIProxyCAPath returns where the root certificate lives.
func (a *App) SNIProxyCAPath() (string, error) {
	if a.sni != nil && a.sni.ca != nil {
		return a.sni.ca.CertPath(), nil
	}
	dir, err := a.sniDir()
	if err != nil {
		return "", err
	}
	ca, err := sniproxy.LoadOrCreateCA(dir)
	if err != nil {
		return "", err
	}
	return ca.CertPath(), nil
}

// OpenSNIProxyFolder reveals the certificate folder.
func (a *App) OpenSNIProxyFolder() error {
	dir, err := a.sniDir()
	if err != nil {
		return err
	}
	return reveal(dir)
}

// ResetSNIProxyRules restores the built-in default rule list.
func (a *App) ResetSNIProxyRules() (map[string]any, error) {
	if _, err := a.settings.Update(func(s *settings.Settings) error {
		s.SNIProxy.Rules = sniproxy.DefaultRules
		return nil
	}); err != nil {
		return nil, err
	}
	return a.GetSNIProxySettings(), a.restartSNIProxy()
}

// startSNIProxyOnBoot applies the saved state at startup.
func (a *App) startSNIProxyOnBoot() {
	if err := a.restartSNIProxy(); err != nil && a.log != nil {
		a.log.Printf("sniproxy start: %v", err)
	}
}
func sniAddrOrDefault(addr string) string {
	if strings.TrimSpace(addr) == "" {
		return "127.0.0.1:17891"
	}
	return strings.TrimSpace(addr)
}
