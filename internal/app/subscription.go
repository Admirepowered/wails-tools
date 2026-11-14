package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"wailstools/internal/miproxy"
	"wailstools/internal/proxyx"
	"wailstools/internal/settings"
	"wailstools/internal/subscription"
)

// ListSubscriptions returns saved subscriptions.
func (a *App) ListSubscriptions() []subscription.Source {
	list := a.subs.List()
	if list == nil {
		return []subscription.Source{}
	}
	return list
}

// SaveSubscription creates or updates a subscription.
func (a *App) SaveSubscription(src subscription.Source) (subscription.Source, error) {
	return a.subs.Save(src)
}

// RefreshSubscriptionViaProxy updates a subscription once through the proxy.
func (a *App) RefreshSubscriptionViaProxy(id string) (subscription.Source, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res, err := a.subs.RefreshWithProxy(ctx, id)
	if err != nil {
		return res.Source, err
	}
	a.emit("subscription:updated", res)
	return res.Source, nil
}

// DeleteSubscription removes a subscription.
func (a *App) DeleteSubscription(id string) error { return a.subs.Delete(id) }

// RefreshSubscription fetches one subscription now.
func (a *App) RefreshSubscription(id string) (subscription.Source, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := a.subs.Refresh(ctx, id)
	return res.Source, err
}

// RefreshAllSubscriptions refreshes every enabled subscription.
func (a *App) RefreshAllSubscriptions() ([]subscription.UpdateResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	results := a.subs.RefreshAll(ctx)
	if results == nil {
		results = []subscription.UpdateResult{}
	}
	return results, nil
}

// ApplySubscriptionNode sets the proxy for the given scopes from a node.
func (a *App) ApplySubscriptionNode(sourceID string, nodeIndex int, scopes []string, all bool) (settings.Settings, error) {
	src, ok := a.subs.Get(sourceID)
	if !ok {
		return settings.Settings{}, errors.New("订阅不存在")
	}
	if nodeIndex < 0 || nodeIndex >= len(src.Nodes) {
		return settings.Settings{}, errors.New("节点不存在")
	}
	node := src.Nodes[nodeIndex]
	rule := settings.ProxyRule{Mode: string(node.Protocol), URL: hostPort(node.Server, node.Port), Username: node.Username, Password: node.Password}
	if rule.Mode == "" || (node.Protocol != miproxy.ProtoHTTP && node.Protocol != miproxy.ProtoSOCKS5) {
		rule.Mode = "http"
	}
	if all {
		return a.settings.Update(func(s *settings.Settings) error {
			s.Proxy.Default = rule
			if s.Proxy.Scopes == nil {
				s.Proxy.Scopes = map[string]settings.ProxyRule{}
			}
			for _, scope := range settings.Scopes {
				s.Proxy.Scopes[scope] = rule.Clone()
			}
			return nil
		})
	}
	if len(scopes) == 0 {
		return settings.Settings{}, errors.New("请选择要应用的作用域")
	}
	return a.settings.Update(func(s *settings.Settings) error {
		if s.Proxy.Scopes == nil {
			s.Proxy.Scopes = map[string]settings.ProxyRule{}
		}
		for _, scope := range scopes {
			s.Proxy.Scopes[scope] = rule.Clone()
		}
		return nil
	})
}

// TestSubscriptionNode verifies a node is reachable through the proxy.
func (a *App) TestSubscriptionNode(sourceID string, nodeIndex int) proxyx.TestResult {
	src, ok := a.subs.Get(sourceID)
	if !ok || nodeIndex < 0 || nodeIndex >= len(src.Nodes) {
		return proxyx.TestResult{Detail: "节点不存在"}
	}
	node := src.Nodes[nodeIndex]
	rule := settings.ProxyRule{Mode: string(node.Protocol), URL: hostPort(node.Server, node.Port), Username: node.Username, Password: node.Password}
	if rule.Mode == "" {
		rule.Mode = "http"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return proxyx.TestProxy(ctx, rule, "http://www.gstatic.com/generate_204")
}

func hostPort(host string, port int) string {
	if port <= 0 {
		port = 8080
	}
	if containsColon(host) {
		return fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

func containsColon(s string) bool {
	for _, r := range s {
		if r == ':' {
			return true
		}
	}
	return false
}
