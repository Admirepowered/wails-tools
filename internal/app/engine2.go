package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"wailstools/internal/geodata"
	"wailstools/internal/miproxy"
	"wailstools/internal/paths"
	"wailstools/internal/proxysvc"
	"wailstools/internal/proxyx"
	"wailstools/internal/settings"
)

// ---- engine options built from settings ----------------------------------

// buildDirectFunc combines custom rules with country based geoip bypass.
func (a *App) buildDirectFunc() func(host string) bool {
	cfg := a.settings.Get().DirectRules
	if !cfg.UseGeoIP && len(cfg.Rules) == 0 {
		return nil
	}
	return func(host string) bool {
		if miproxy.MatchHost(host, cfg.Rules) {
			return true
		}
		if !cfg.UseGeoIP || a.geo == nil || len(cfg.Countries) == 0 {
			return false
		}
		ips := resolveHostIPs(host)
		for _, ip := range ips {
			for _, code := range a.geo.LookupCodes(ip) {
				for _, want := range cfg.Countries {
					if equalFold(code, strings.TrimSpace(want)) {
						return true
					}
				}
			}
		}
		return false
	}
}

func resolveHostIPs(host string) []net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'a' && ca <= 'z' {
			ca -= 32
		}
		if cb >= 'a' && cb <= 'z' {
			cb -= 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// buildChain resolves the configured proxy chain.
func (a *App) buildChain() ([]miproxy.Node, miproxy.RelayFunc) {
	cfg := a.settings.Get().ProxyChain
	var hops []miproxy.Node
	if cfg.Mode == "nodes" && a.customNodes != nil {
		for _, id := range cfg.NodeIDs {
			if len(hops) >= 2 {
				break
			}
			for _, n := range a.customNodes.List() {
				if n.ID == id {
					hops = append(hops, n)
					break
				}
			}
		}
	}
	var relay miproxy.RelayFunc
	if cfg.Mode == "server" && a.link != nil && a.link.Available() {
		relay = func(ctx context.Context, target string) (net.Conn, error) {
			return a.link.DialTCP(ctx, target)
		}
	}
	return hops, relay
}

// startEngine launches the local proxy for a node with chain/rules applied.
func (a *App) startEngine(node miproxy.Node) (proxysvc.Status, error) {
	rt := a.settings.Get().ProxyRuntime
	opts := proxysvc.StartOptions{
		External: rt.External,
		Username: rt.Username,
		Password: rt.Password,
		Direct:   a.buildDirectFunc(),
	}
	if rt.External && rt.Port > 0 {
		opts.Port = rt.Port
	}
	opts.Chain, opts.Relay = a.buildChain()
	return a.proxySvc.Start(node, opts)
}

// RestartEngine re-applies chain/rules to the running engine.
func (a *App) RestartEngine() (proxysvc.Status, error) {
	st := a.proxySvc.Status()
	if !st.Running || st.Node == nil {
		return st, nil
	}
	return a.startEngine(*st.Node)
}

// ---- chain / rules settings ---------------------------------------------

// GetProxyChain returns the configured chain.
func (a *App) GetProxyChain() settings.ProxyChainSettings {
	return a.settings.Get().ProxyChain
}

// SaveProxyChain stores the chain and restarts the engine when it is running.
func (a *App) SaveProxyChain(cfg settings.ProxyChainSettings) (settings.ProxyChainSettings, error) {
	if len(cfg.NodeIDs) > 2 {
		cfg.NodeIDs = cfg.NodeIDs[:2]
	}
	saved, err := a.settings.Update(func(s *settings.Settings) error {
		s.ProxyChain = cfg
		return nil
	})
	if err != nil {
		return settings.ProxyChainSettings{}, err
	}
	_, _ = a.RestartEngine()
	return saved.ProxyChain, nil
}

// GetDirectRules returns the direct-rule configuration.
func (a *App) GetDirectRules() settings.DirectRuleSettings {
	return a.settings.Get().DirectRules
}

// SaveDirectRules stores the rules and restarts the engine when running.
func (a *App) SaveDirectRules(cfg settings.DirectRuleSettings) (settings.DirectRuleSettings, error) {
	saved, err := a.settings.Update(func(s *settings.Settings) error {
		s.DirectRules = cfg
		return nil
	})
	if err != nil {
		return settings.DirectRuleSettings{}, err
	}
	_, _ = a.RestartEngine()
	return saved.DirectRules, nil
}

// ---- geoip ---------------------------------------------------------------

// GeoIPStatus reports the local geoip database, falling back to the default
// per-user directory when the configured root has no database.
func (a *App) GeoIPStatus() geodata.Status {
	db := a.geoDB()
	st := db.Status()
	if st.Ready {
		return st
	}
	for _, dir := range a.geoCandidateDirs() {
		if dir == "" || dir == filepath.Dir(db.Path()) {
			continue
		}
		alt := geodata.Open(dir)
		if alt.Status().Ready {
			a.geo = alt
			return alt.Status()
		}
	}
	return st
}

// geoDB returns the geoip database, creating it lazily when missing.
func (a *App) geoDB() *geodata.DB {
	if a.geo != nil {
		return a.geo
	}
	dirs := a.geoCandidateDirs()
	for _, dir := range dirs {
		db := geodata.Open(dir)
		if db.Status().Ready {
			a.geo = db
			a.geo.SetFallbackClients(a.geoFallbackClients()...)
			return db
		}
	}
	if len(dirs) > 0 {
		a.geo = geodata.Open(dirs[0])
		a.geo.SetFallbackClients(a.geoFallbackClients()...)
		return a.geo
	}
	a.geo = geodata.Open(".")
	return a.geo
}

// geoCandidateDirs lists every directory that may hold a geoip database.
func (a *App) geoCandidateDirs() []string {
	dirs := []string{a.rootDir}
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		dirs = append(dirs, filepath.Join(v, "WailsTools"))
	}
	if v := os.Getenv("APPDATA"); v != "" {
		dirs = append(dirs, filepath.Join(v, "WailsTools"))
	}
	if root, err := paths.Root(); err == nil {
		dirs = append(dirs, root)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// DownloadGeoIP fetches geoip.metadb from the MetaCubeX release.
func (a *App) DownloadGeoIP() (geodata.Status, error) {
	db := a.geoDB()
	if db == nil {
		return geodata.Status{}, errors.New("geoip 未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	key := "geoip"
	err := db.Download(ctx, func(done, total int64) {
		a.emit("transfer:progress", map[string]any{"key": key, "kind": "geoip", "done": done, "total": total})
	})
	if err != nil {
		return db.Status(), err
	}
	a.emit("geoip:updated", db.Status())
	return db.Status(), nil
}

// GeoIPCountries lists country codes present in the database.
func (a *App) GeoIPCountries() []string {
	db := a.geoDB()
	if db == nil {
		return []string{}
	}
	list := db.Countries()
	sort.Strings(list)
	return list
}

// ---- per-scope internal proxy -------------------------------------------

// ScopeInternalProxy reports whether a scope currently points at the engine.
func (a *App) ScopeInternalProxy(scope string) bool {
	st := a.proxySvc.Status()
	if !st.Running {
		return false
	}
	rule := a.settings.Get().Proxy.Resolve(scope)
	return rule.Mode == "socks5" && rule.URL == st.Addr
}

// SetScopeInternalProxy points one tool's connections at the engine (or off).
func (a *App) SetScopeInternalProxy(scope string, use bool) (settings.Settings, error) {
	st := a.proxySvc.Status()
	cfg, err := a.settings.Update(func(s *settings.Settings) error {
		if s.Proxy.Scopes == nil {
			s.Proxy.Scopes = map[string]settings.ProxyRule{}
		}
		if !use {
			s.Proxy.Scopes[scope] = settings.ProxyRule{Mode: "off"}
			return nil
		}
		if !st.Running {
			return errors.New("请先在订阅或自定义节点中启用一个节点")
		}
		rule := settings.ProxyRule{Mode: "socks5", URL: st.Addr}
		rt := s.ProxyRuntime
		if rt.External {
			rule.Username = rt.Username
			rule.Password = rt.Password
		}
		s.Proxy.Scopes[scope] = rule
		return nil
	})
	return cfg, err
}

// ---- latency history -----------------------------------------------------

// LatencyEntry is a persisted node test result.
type LatencyEntry struct {
	Key       string `json:"key"`
	LatencyMS int64  `json:"latencyMs"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	At        string `json:"at"`
}

var latencyMu sync.Mutex

func (a *App) latencyPath() string {
	return filepath.Join(a.rootDir, "latency.json")
}

// LoadLatencyResults returns the last known latency per node.
func (a *App) LoadLatencyResults() map[string]LatencyEntry {
	latencyMu.Lock()
	defer latencyMu.Unlock()
	out := map[string]LatencyEntry{}
	data, err := os.ReadFile(a.latencyPath())
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

// SaveLatencyResults merges new latency entries into the history.
func (a *App) SaveLatencyResults(entries []LatencyEntry) error {
	if len(entries) == 0 {
		return nil
	}
	latencyMu.Lock()
	defer latencyMu.Unlock()
	all := map[string]LatencyEntry{}
	if data, err := os.ReadFile(a.latencyPath()); err == nil {
		_ = json.Unmarshal(data, &all)
	}
	for _, e := range entries {
		if e.Key == "" {
			continue
		}
		if e.At == "" {
			e.At = time.Now().Format(time.RFC3339)
		}
		all[e.Key] = e
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.latencyPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.latencyPath())
}

// NodeLatencyKey builds the stable key used for latency history.
func NodeLatencyKey(node miproxy.Node) string {
	return fmt.Sprintf("%s|%s|%d|%s", node.Protocol, node.Server, node.Port, node.Name)
}

// geoFallbackClients returns the HTTP clients tried when the SNI bypass fails:
// the configured proxy first (if any), then a direct connection.
func (a *App) geoFallbackClients() []*http.Client {
	clients := make([]*http.Client, 0, 2)
	rule := settings.ProxyRule{Mode: "off"}
	if a.settings != nil {
		rule = a.settings.ProxyFor(settings.ScopeHTTP)
	}
	if rule.Mode != "" && rule.Mode != "off" {
		if client, err := proxyx.HTTPClient(rule, 90*time.Second); err == nil {
			clients = append(clients, client)
		}
	}
	clients = append(clients, &http.Client{Timeout: 90 * time.Second})
	return clients
}
