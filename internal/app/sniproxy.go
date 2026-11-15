package app

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wailstools/internal/miproxy"
	"wailstools/internal/settings"
	"wailstools/internal/sniproxy"
)

// sniState owns the SNI tunnel engine. It has no listener of its own: the
// local proxy asks it first and only falls back to the proxy chain when the
// domain is not covered by the rules.
type sniState struct {
	rules    *sniproxy.RuleSet
	resolver *sniproxy.Resolver
	ca       *sniproxy.CA
	handler  *sniproxy.Handler
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

// sniChainDialer builds the outbound dialer used by the SNI tunnel, honouring
// the same chain/relay settings as the local proxy.
func (a *App) sniChainDialer() *miproxy.Dialer {
	hops, relay := a.buildChain()
	return miproxy.NewChainDialer(hops, relay, a.buildDirectFunc(), 15e9)
}

// fetchServerSNIRules returns the rule list published on the sync server.
func (a *App) fetchServerSNIRules() (string, string, bool) {
	if a.link == nil || !a.link.Available() {
		return "", "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	rules, at, found, err := a.link.GetSNIRules(ctx)
	if err != nil || !found || len(strings.TrimSpace(rules)) < 20 {
		return "", at, false
	}
	return rules, at, true
}

// refreshDefaultSNIRulesIfUnset adopts the server defaults on installs that
// never customized the list.
func (a *App) refreshDefaultSNIRulesIfUnset() {
	rules, at, ok := a.fetchServerSNIRules()
	if !ok {
		return
	}
	if strings.TrimSpace(a.settings.Get().SNIProxy.Rules) != "" {
		return
	}
	if _, err := a.settings.Update(func(s *settings.Settings) error {
		if strings.TrimSpace(s.SNIProxy.Rules) == "" {
			s.SNIProxy.Rules = rules
		}
		return nil
	}); err != nil {
		return
	}
	if a.log != nil {
		a.log.Printf("sniproxy: adopted server default rules (updated %s)", at)
	}
	_ = a.applySNIProxy()
	a.emit("sniproxy:rules", map[string]any{"source": "server", "updatedAt": at})
}

// applySNIProxy rebuilds the SNI engine from settings (no listener).
func (a *App) applySNIProxy() error {
	cfg := a.settings.Get().SNIProxy
	if cfg.Enabled && strings.TrimSpace(cfg.Rules) == "" {
		// Pull the server defaults in the background; the built-in list is used
		// until they arrive.
		go a.refreshDefaultSNIRulesIfUnset()
	}
	if !cfg.Enabled {
		a.setSNI(nil)
		a.emit("sniproxy:state", a.SNIProxyStatus())
		return nil
	}
	dir, err := a.sniDir()
	if err != nil {
		return err
	}
	logf := func(format string, args ...any) {
		if a.log != nil {
			a.log.Printf(format, args...)
		}
	}
	state := &sniState{dir: dir}
	state.resolver = sniproxy.NewResolver(dir, logf)
	ca, err := sniproxy.LoadOrCreateCA(dir)
	if err != nil {
		return err
	}
	state.ca = ca
	state.rules = sniproxy.ParseRules(effectiveSNIRules(cfg.Rules))

	var upstream func(ctx context.Context, network, addr string) (net.Conn, error)
	if cfg.UseExistingProxy {
		dialer := a.sniChainDialer()
		upstream = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(ctx, addr)
		}
		state.resolver.SetUpstream(func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(ctx, addr)
		})
		logf("sniproxy: outbound follows the proxy chain")
	}
	state.handler = sniproxy.NewHandler(sniproxy.Options{
		Rules:    state.rules,
		Resolver: state.resolver,
		CA:       ca,
		Mitm:     cfg.Mitm,
		Upstream: upstream,
		Logf:     logf,
	})
	a.setSNI(state)
	logf("sniproxy: %d rule entries, mitm=%v", len(strings.Split(strings.TrimSpace(effectiveSNIRules(cfg.Rules)), "\n")), cfg.Mitm)
	a.emit("sniproxy:state", a.SNIProxyStatus())
	return nil
}

func (a *App) setSNI(state *sniState) {
	a.sniMu.Lock()
	a.sni = state
	a.sniMu.Unlock()
}

// sniSnapshot returns the current engine (nil when disabled).
func (a *App) sniSnapshot() *sniState {
	a.sniMu.Lock()
	defer a.sniMu.Unlock()
	return a.sni
}

// sniIntercept is handed to the local proxy: matching TLS tunnels are served by
// the SNI engine (no SNI sent upstream), everything else falls through to the
// proxy chain.
func (a *App) sniIntercept(info miproxy.InterceptInfo) bool {
	state := a.sniSnapshot()
	if state == nil || state.handler == nil || info.Client == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(info.Host))
	port := strings.TrimSpace(info.Port)
	if host == "" || port == "" {
		return false
	}
	if port != "443" && port != "8443" {
		return false
	}
	if _, ok := state.handler.Match(host); !ok {
		return false
	}
	// The interceptor owns the success reply from here on.
	switch info.Kind {
	case "socks5":
		if _, err := info.Client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
			return true
		}
	case "http-connect":
		if _, err := info.Client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
			return true
		}
	}
	conn := info.Client
	if info.Reader != nil {
		conn = &readerConn{Conn: info.Client, r: info.Reader}
	}
	state.handler.HandleTunnel(conn, host, port)
	return true
}

// readerConn overlays bytes buffered by the inbound parser.
type readerConn struct {
	net.Conn
	r io.Reader
}

func (c *readerConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// SNIProxyStatus reports the SNI engine state (it shares the proxy port).
func (a *App) SNIProxyStatus() map[string]any {
	cfg := a.settings.Get().SNIProxy
	out := map[string]any{
		"enabled": cfg.Enabled, "mitm": cfg.Mitm,
		"useExistingProxy": cfg.UseExistingProxy, "running": false,
		"proxyAddr": a.proxySvcStatusAddr(),
	}
	state := a.sniSnapshot()
	if state != nil && state.handler != nil {
		stats := state.handler.Stats()
		out["running"] = true
		out["stats"] = map[string]any{
			"active": stats.Active, "total": stats.Total, "mitm": stats.Mitm,
			"passthrough": stats.Passthrough, "upstream": stats.Upstream,
			"failed": stats.Failed, "lastError": stats.LastError,
		}
		out["cachedDomains"] = state.resolver.CacheSize()
		out["caPath"] = state.ca.CertPath()
	}
	return out
}

// proxySvcStatusAddr is the local proxy address once it is running.
func (a *App) proxySvcStatusAddr() string {
	if a.proxySvc == nil {
		return ""
	}
	st := a.proxySvc.Status()
	if !st.Running {
		return ""
	}
	return st.Addr
}

// GetSNIProxySettings returns the saved settings plus the effective rule text.
func (a *App) GetSNIProxySettings() map[string]any {
	cfg := a.settings.Get().SNIProxy
	rules := cfg.Rules
	if strings.TrimSpace(rules) == "" {
		rules = sniproxy.DefaultRules
	}
	return map[string]any{
		"enabled": cfg.Enabled, "mitm": cfg.Mitm,
		"useExistingProxy": cfg.UseExistingProxy, "rules": rules,
		"rulesAreDefault": strings.TrimSpace(cfg.Rules) == "",
	}
}

// SaveSNIProxySettings persists the settings and rebuilds the engine live.
func (a *App) SaveSNIProxySettings(next settings.SNIProxySettings) (map[string]any, error) {
	if strings.TrimSpace(next.Rules) == "" {
		next.Rules = sniproxy.DefaultRules
	}
	if _, err := a.settings.Update(func(s *settings.Settings) error {
		s.SNIProxy = next
		return nil
	}); err != nil {
		return nil, err
	}
	if err := a.applySNIProxy(); err != nil {
		return a.SNIProxyStatus(), err
	}
	return a.SNIProxyStatus(), nil
}

// InstallSNIProxyCA trusts the generated root certificate (current user).
func (a *App) InstallSNIProxyCA() (map[string]any, error) {
	state := a.sniSnapshot()
	if state == nil || state.ca == nil {
		return nil, errors.New("请先开启 SNI 分流以生成根证书")
	}
	path, err := state.ca.InstallRoot()
	out := map[string]any{"caPath": path}
	if err != nil {
		return out, err
	}
	out["installed"] = true
	return out, nil
}

// SNIProxyCAPath returns where the root certificate lives.
func (a *App) SNIProxyCAPath() (string, error) {
	if state := a.sniSnapshot(); state != nil && state.ca != nil {
		return state.ca.CertPath(), nil
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

// ResetSNIProxyRules restores the defaults: the server-published list first,
// the compiled-in list as a fallback.
func (a *App) ResetSNIProxyRules() (map[string]any, error) {
	rules, updatedAt, fromServer := a.fetchServerSNIRules()
	source := "server"
	if !fromServer {
		rules = sniproxy.DefaultRules
		source = "builtin"
	}
	if _, err := a.settings.Update(func(s *settings.Settings) error {
		s.SNIProxy.Rules = rules
		return nil
	}); err != nil {
		return nil, err
	}
	if err := a.applySNIProxy(); err != nil {
		return nil, err
	}
	out := a.GetSNIProxySettings()
	out["source"] = source
	out["updatedAt"] = updatedAt
	return out, nil
}

// startSNIProxyOnBoot applies the saved state at startup.
func (a *App) startSNIProxyOnBoot() {
	if err := a.applySNIProxy(); err != nil && a.log != nil {
		a.log.Printf("sniproxy start: %v", err)
	}
}
