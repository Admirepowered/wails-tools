package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"wailstools/internal/miproxy"
	"wailstools/internal/proxysvc"
	"wailstools/internal/settings"
)

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

// ActivateProxyNode starts the local proxy engine for a node and points the
// selected proxy scopes at it.
func (a *App) ActivateProxyNode(node miproxy.Node, scopes []string, all bool, external bool, username, password string) (map[string]any, error) {
	if node.Protocol == miproxy.ProtoHTTP || node.Protocol == miproxy.ProtoSOCKS5 || node.Protocol == "" {
		// Plain upstreams do not need the local engine.
		rule := settings.ProxyRule{
			Mode:     string(node.Protocol),
			URL:      hostPortOf(node.Server, node.Port),
			Username: node.Username,
			Password: node.Password,
		}
		if rule.Mode == "" {
			rule.Mode = "http"
		}
		cfg, err := a.applyProxyRule(rule, scopes, all)
		if err != nil {
			return nil, err
		}
		return map[string]any{"mode": "upstream", "rule": rule, "settings": cfg, "node": node}, nil
	}

	opts := proxysvc.StartOptions{External: external, Username: username, Password: password}
	if external {
		if rt := a.settings.Get().ProxyRuntime; rt.Port > 0 {
			opts.Port = rt.Port
		}
	}
	st, err := a.proxySvc.Start(node, opts)
	if err != nil {
		return nil, err
	}
	port := portOfAddr(st.Addr)
	rule := settings.ProxyRule{Mode: "socks5", URL: hostPortOf("127.0.0.1", port)}
	if external {
		rule.Username = username
		rule.Password = password
	}
	cfg, err := a.applyProxyRule(rule, scopes, all)
	if err != nil {
		return nil, err
	}
	a.emit("proxy:status", st)
	return map[string]any{"mode": "engine", "status": st, "rule": rule, "settings": cfg, "node": node}, nil
}

func (a *App) applyProxyRule(rule settings.ProxyRule, scopes []string, all bool) (settings.Settings, error) {
	return a.settings.Update(func(s *settings.Settings) error {
		if s.Proxy.Scopes == nil {
			s.Proxy.Scopes = map[string]settings.ProxyRule{}
		}
		targets := scopes
		if all {
			targets = nil
			for _, sc := range settings.Scopes {
				if sc == settings.ScopeSync {
					continue // the sync link uses QUIC/TCP directly
				}
				targets = append(targets, sc)
			}
		}
		if len(targets) == 0 {
			return errors.New("请选择要应用的作用域")
		}
		for _, sc := range targets {
			s.Proxy.Scopes[sc] = rule.Clone()
		}
		return nil
	})
}

// DeactivateProxy stops the engine and clears the applied proxy scopes.
func (a *App) DeactivateProxy() (settings.Settings, error) {
	_ = a.proxySvc.Stop()
	cfg, err := a.settings.Update(func(s *settings.Settings) error {
		off := settings.ProxyRule{Mode: "off"}
		s.Proxy.Default = off
		s.Proxy.Scopes = map[string]settings.ProxyRule{}
		for _, sc := range settings.Scopes {
			s.Proxy.Scopes[sc] = off.Clone()
		}
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

// TestProxyNode measures reachability of a node without activating it.
func (a *App) TestProxyNode(node miproxy.Node) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ms, err := proxysvc.Probe(ctx, node, "")
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "latencyMs": ms}
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
	cfg, err := a.settings.Update(func(s *settings.Settings) error {
		s.ProxyRuntime = next
		return nil
	})
	return cfg.ProxyRuntime, err
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
