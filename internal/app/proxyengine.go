package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"wailstools/internal/miproxy"
	"wailstools/internal/proxysvc"
	"wailstools/internal/settings"
	"wailstools/internal/sysproxy"
)

// Proxy mode values exposed to the UI.
const (
	ProxyModeOff      = "off"
	ProxyModeInternal = "internal"
)

// NodeTestResult is the latency of one node.
type NodeTestResult struct {
	Index     int    `json:"index"`
	LatencyMS int64  `json:"latencyMs"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
}

// SubscriptionNodes returns the parsed protocol nodes of a subscription.
func (a *App) SubscriptionNodes(sourceID string) ([]miproxy.Node, error) {
	src, ok := a.subs.Get(sourceID)
	if !ok {
		return nil, errors.New("订阅不存在")
	}
	if src.Nodes == nil {
		return []miproxy.Node{}, nil
	}
	return src.Nodes, nil
}

// ActivateProxyNode switches the active node. All proxy scopes are pointed at
// it, so no per-connection configuration is required.
func (a *App) ActivateProxyNode(node miproxy.Node) (map[string]any, error) {
	rt := a.settings.Get().ProxyRuntime
	var rule settings.ProxyRule
	mode := "engine"

	if node.Protocol == miproxy.ProtoHTTP || node.Protocol == miproxy.ProtoSOCKS5 || node.Protocol == "" {
		// Plain upstreams do not need the local engine.
		mode = "upstream"
		rule = settings.ProxyRule{
			Mode:     string(node.Protocol),
			URL:      hostPortOf(node.Server, node.Port),
			Username: node.Username,
			Password: node.Password,
		}
		if rule.Mode == "" {
			rule.Mode = "http"
		}
		if st := a.proxySvc.Status(); st.Running {
			_ = a.proxySvc.Stop()
			_ = a.disableSystemProxy()
		}
	} else {
		st, err := a.startEngine(node)
		if err != nil {
			return nil, err
		}
		port := portOfAddr(st.Addr)
		rule = settings.ProxyRule{Mode: "socks5", URL: hostPortOf("127.0.0.1", port)}
		if rt.External {
			rule.Username = rt.Username
			rule.Password = rt.Password
		}
		if rt.SystemProxy && rt.External {
			if err := sysproxy.Enable(hostPortOf("127.0.0.1", port)); err != nil {
				a.emit("proxy:error", "系统代理设置失败: "+err.Error())
			}
		}
	}

	// Switching nodes only changes the engine; per-tool proxy scopes are left
	// untouched so each tool decides whether to use the internal proxy.
	st := a.proxySvc.Status()
	a.emit("proxy:status", st)
	return map[string]any{"mode": mode, "status": st, "rule": rule, "node": node}, nil
}

// applyInternalMode stores the rule for every scope and marks the mode.
func (a *App) applyInternalMode(rule settings.ProxyRule) (settings.Settings, error) {
	return a.settings.Update(func(s *settings.Settings) error {
		s.Proxy.Mode = ProxyModeInternal
		s.Proxy.Default = rule
		if s.Proxy.Scopes == nil {
			s.Proxy.Scopes = map[string]settings.ProxyRule{}
		}
		for _, sc := range settings.Scopes {
			if sc == settings.ScopeSync {
				// The sync link keeps its own QUIC/TCP transport.
				s.Proxy.Scopes[sc] = settings.ProxyRule{Mode: "off"}
				continue
			}
			s.Proxy.Scopes[sc] = rule.Clone()
		}
		return nil
	})
}

// SetProxyMode switches between "off" (direct) and "internal" (route through
// the active node without further configuration).
func (a *App) SetProxyMode(mode string) (settings.Settings, error) {
	switch mode {
	case ProxyModeOff:
		return a.DeactivateProxy()
	case ProxyModeInternal:
		st := a.proxySvc.Status()
		if !st.Running {
			return settings.Settings{}, errors.New("请先在订阅或自定义节点中启用一个节点")
		}
		rule := settings.ProxyRule{Mode: "socks5", URL: st.Addr}
		rt := a.settings.Get().ProxyRuntime
		if rt.External {
			rule.Username = rt.Username
			rule.Password = rt.Password
		}
		return a.applyInternalMode(rule)
	default:
		return settings.Settings{}, fmt.Errorf("未知代理模式 %q", mode)
	}
}

// DeactivateProxy stops the engine, restores direct connections and turns the
// system proxy off.
func (a *App) DeactivateProxy() (settings.Settings, error) {
	_ = a.proxySvc.Stop()
	_ = a.disableSystemProxy()
	cfg, err := a.settings.Update(func(s *settings.Settings) error {
		s.Proxy.Mode = ProxyModeOff
		off := settings.ProxyRule{Mode: "off"}
		s.Proxy.Default = off
		s.Proxy.Scopes = map[string]settings.ProxyRule{}
		for _, sc := range settings.Scopes {
			s.Proxy.Scopes[sc] = off.Clone()
		}
		s.ProxyRuntime.SystemProxy = false
		return nil
	})
	if err == nil {
		a.emit("proxy:status", a.proxySvc.Status())
	}
	return cfg, err
}

// ProxyRuntimeStatus reports the running local proxy.
func (a *App) ProxyRuntimeStatus() proxysvc.Status { return a.proxySvc.Status() }

// ProxyEngineAvailable reports whether the engine supports this platform.
func (a *App) ProxyEngineAvailable() bool { return true }

// TestProxyNode measures a single node against the standard probe URL.
func (a *App) TestProxyNode(node miproxy.Node) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	ms, err := proxysvc.Probe(ctx, node, proxysvc.ProbeURL)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "latencyMs": ms}
}

// TestAllNodes measures every node concurrently (bounded) and returns the
// results in input order.
func (a *App) TestAllNodes(nodes []miproxy.Node) []NodeTestResult {
	results := make([]NodeTestResult, len(nodes))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i := range nodes {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			ms, err := proxysvc.Probe(ctx, nodes[idx], proxysvc.ProbeURL)
			res := NodeTestResult{Index: idx, LatencyMS: ms, OK: err == nil}
			if err != nil {
				res.Error = err.Error()
			}
			results[idx] = res
		}(i)
	}
	wg.Wait()
	return results
}

// SetSystemProxy mirrors the local proxy into the OS settings. External mode
// must be enabled first, as requested by the UI flow.
func (a *App) SetSystemProxy(enabled bool) (map[string]any, error) {
	rt := a.settings.Get().ProxyRuntime
	if enabled {
		if !rt.External {
			return nil, errors.New("请先打开「外部可用」再开启系统代理")
		}
		st := a.proxySvc.Status()
		if !st.Running {
			return nil, errors.New("请先启用一个节点")
		}
		if !sysproxy.Supported() {
			return nil, sysproxy.ErrUnsupported
		}
		if err := sysproxy.Enable(st.Addr); err != nil {
			return nil, err
		}
	} else {
		if err := a.disableSystemProxy(); err != nil {
			return nil, err
		}
	}
	cfg, err := a.settings.Update(func(s *settings.Settings) error {
		s.ProxyRuntime.SystemProxy = enabled
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"enabled": enabled, "state": sysproxy.Current(), "settings": cfg}, nil
}

// SystemProxyState reports the OS proxy configuration.
func (a *App) SystemProxyState() map[string]any {
	state := sysproxy.Current()
	return map[string]any{
		"enabled":   state.Enabled,
		"server":    state.Server,
		"supported": state.Supported,
		"setting":   a.settings.Get().ProxyRuntime.SystemProxy,
	}
}

func (a *App) disableSystemProxy() error {
	if !sysproxy.Supported() {
		return nil
	}
	return sysproxy.Disable()
}

// ListCustomNodes returns user-defined nodes.
func (a *App) ListCustomNodes() []miproxy.Node {
	list := a.customNodes.List()
	if list == nil {
		return []miproxy.Node{}
	}
	return list
}

// SaveCustomNode creates or updates a user-defined node.
func (a *App) SaveCustomNode(node miproxy.Node) (miproxy.Node, error) {
	return a.customNodes.Save(node)
}

// DeleteCustomNode removes a user-defined node.
func (a *App) DeleteCustomNode(id string) error { return a.customNodes.Delete(id) }

// ImportNodeLinks parses share links pasted by the user and stores them.
func (a *App) ImportNodeLinks(text string) ([]miproxy.Node, error) {
	return a.customNodes.ImportLinks(text)
}

// ParseNodeLinks previews share links without storing them.
func (a *App) ParseNodeLinks(text string) []miproxy.Node {
	nodes := miproxy.ParseSubscription(text)
	if nodes == nil {
		return []miproxy.Node{}
	}
	return nodes
}

// GetProxyRuntimeSettings returns the engine bind configuration.
func (a *App) GetProxyRuntimeSettings() settings.ProxyRuntimeSettings {
	return a.settings.Get().ProxyRuntime
}

// SaveProxyRuntimeSettings stores the engine bind configuration.
func (a *App) SaveProxyRuntimeSettings(next settings.ProxyRuntimeSettings) (settings.ProxyRuntimeSettings, error) {
	// Turning external mode off must also drop the system proxy.
	if !next.External && next.SystemProxy {
		next.SystemProxy = false
		_ = a.disableSystemProxy()
	}
	cfg, err := a.settings.Update(func(s *settings.Settings) error {
		s.ProxyRuntime = next
		return nil
	})
	return cfg.ProxyRuntime, err
}

// ActiveProxyNode returns the node currently served by the engine.
func (a *App) ActiveProxyNode() map[string]any {
	st := a.proxySvc.Status()
	mode := a.settings.Get().Proxy.Mode
	out := map[string]any{"mode": mode, "running": st.Running}
	if st.Running && st.Node != nil {
		out["node"] = st.Node
		out["addr"] = st.Addr
		out["external"] = st.External
	}
	return out
}

func hostPortOf(host string, port int) string {
	if port <= 0 {
		port = 1080
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

func portOfAddr(addr string) int {
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		return 0
	}
	var port int
	fmt.Sscanf(addr[idx+1:], "%d", &port)
	return port
}
