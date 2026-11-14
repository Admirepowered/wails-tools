// Package subscription manages proxy subscriptions: it fetches a URL, parses
// the nodes it advertises (plain lists, base64 lists, Clash YAML and SIP008
// JSON), keeps them up to date and exposes traffic/expiry metadata.
package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"wailstools/internal/miproxy"
	"wailstools/internal/proxyx"
	"wailstools/internal/settings"
)

// Node is one usable proxy endpoint (all supported protocols).
type Node = miproxy.Node

// ProxyURL renders plain http/socks5 nodes as a proxy URL.
func ProxyURL(n Node) string {
	scheme := string(n.Protocol)
	if n.Protocol != miproxy.ProtoHTTP && n.Protocol != miproxy.ProtoSOCKS5 {
		return ""
	}
	host := n.Server
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(n.Port))
	if n.Username != "" {
		return fmt.Sprintf("%s://%s:%s@%s", scheme, n.Username, n.Password, addr)
	}
	return scheme + "://" + addr
}

// Info is the traffic metadata advertised by the provider.
type Info struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
	Total    int64 `json:"total"`
	Expire   int64 `json:"expire"` // unix seconds
}

// Source is a saved subscription.
type Source struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
	// UseProxy fetches this subscription through the proxy.
	UseProxy      bool   `json:"useProxy"`
	IntervalHours int    `json:"intervalHours"`
	LastFetch     string `json:"lastFetch,omitempty"`
	LastError     string `json:"lastError,omitempty"`
	Nodes         []Node `json:"nodes"`
	Info          Info   `json:"info"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

// Store persists subscriptions as a JSON document.
type Store struct {
	mu    sync.Mutex
	path  string
	items []Source
}

// Open loads (or creates) the subscription store.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "subscriptions.json")}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &s.items); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) persist() error {
	data, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns all subscriptions.
func (s *Store) List() []Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Source(nil), s.items...)
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Get returns one subscription.
func (s *Store) Get(id string) (Source, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.items {
		if item.ID == id {
			return item, true
		}
	}
	return Source{}, false
}

// Save inserts or updates a subscription.
func (s *Store) Save(src Source) (Source, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	if src.ID == "" {
		src.ID = "sub-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		src.CreatedAt = now
	}
	if src.CreatedAt == "" {
		src.CreatedAt = now
	}
	src.UpdatedAt = now
	if src.IntervalHours <= 0 {
		src.IntervalHours = 12
	}
	replaced := false
	for i := range s.items {
		if s.items[i].ID == src.ID {
			s.items[i] = src
			replaced = true
			break
		}
	}
	if !replaced {
		s.items = append(s.items, src)
	}
	if err := s.persist(); err != nil {
		return Source{}, err
	}
	return src, nil
}

// Delete removes a subscription.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.items[:0]
	found := false
	for _, item := range s.items {
		if item.ID == id {
			found = true
			continue
		}
		out = append(out, item)
	}
	if !found {
		return errors.New("subscription not found")
	}
	s.items = out
	return s.persist()
}

// Replace swaps the whole subscription list (used by server sync).
func (s *Store) Replace(items []Source) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = items
	return s.persist()
}

// Snapshot returns the raw list (used for syncing).
func (s *Store) Snapshot() []Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Source(nil), s.items...)
}

// UpdateResult describes a refresh.
type UpdateResult struct {
	Source    Source `json:"source"`
	NodeCount int    `json:"nodeCount"`
	Error     string `json:"error,omitempty"`
	FetchedAt string `json:"fetchedAt"`
}

// Manager refreshes subscriptions.
type Manager struct {
	store    *Store
	settings *settings.Store
	logf     func(string, ...any)

	mu       sync.Mutex
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	onUpdate func(UpdateResult)
}

// NewManager wires a manager.
func NewManager(store *Store, settingsStore *settings.Store, logf func(string, ...any)) *Manager {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Manager{store: store, settings: settingsStore, logf: logf}
}

// List returns all saved subscriptions.
func (m *Manager) List() []Source { return m.store.List() }

// Get returns one subscription.
func (m *Manager) Get(id string) (Source, bool) { return m.store.Get(id) }

// Save inserts or updates a subscription.
func (m *Manager) Save(src Source) (Source, error) { return m.store.Save(src) }

// Delete removes a subscription.
func (m *Manager) Delete(id string) error { return m.store.Delete(id) }

// Snapshot returns the raw subscription list.
func (m *Manager) Snapshot() []Source { return m.store.Snapshot() }

// Replace swaps the whole list (server sync).
func (m *Manager) Replace(items []Source) error { return m.store.Replace(items) }

// SetUpdateHandler registers a refresh callback (used for UI events).
func (m *Manager) SetUpdateHandler(fn func(UpdateResult)) {
	m.mu.Lock()
	m.onUpdate = fn
	m.mu.Unlock()
}

// Refresh fetches and parses one subscription.
func (m *Manager) Refresh(ctx context.Context, id string) (UpdateResult, error) {
	src, ok := m.store.Get(id)
	if !ok {
		return UpdateResult{}, errors.New("subscription not found")
	}
	res := m.fetch(ctx, src)
	if res.Error != "" {
		src.LastError = res.Error
		src.LastFetch = res.FetchedAt
		updated, _ := m.store.Save(src)
		return UpdateResult{Source: updated, Error: res.Error, FetchedAt: res.FetchedAt}, errors.New(res.Error)
	}
	src.LastError = ""
	src.LastFetch = res.FetchedAt
	src.Nodes = res.Source.Nodes
	src.Info = res.Source.Info
	updated, err := m.store.Save(src)
	if err != nil {
		return UpdateResult{}, err
	}
	res.Source = updated
	m.emit(res)
	m.logf("subscription %q refreshed: %d nodes", updated.Name, len(updated.Nodes))
	return res, nil
}

func (m *Manager) emit(res UpdateResult) {
	m.mu.Lock()
	cb := m.onUpdate
	m.mu.Unlock()
	if cb != nil {
		cb(res)
	}
}

// RefreshAll refreshes every enabled subscription.
func (m *Manager) RefreshAll(ctx context.Context) []UpdateResult {
	var out []UpdateResult
	for _, src := range m.store.List() {
		if !src.Enabled {
			continue
		}
		res, _ := m.Refresh(ctx, src.ID)
		out = append(out, res)
	}
	return out
}

func (m *Manager) httpClientFor(src Source) (*http.Client, error) {
	rule := settings.ProxyRule{Mode: "off"}
	useProxy := src.UseProxy
	if m.settings != nil && !useProxy && m.settings.Get().Subscriptions.UseProxy {
		useProxy = true
	}
	if m.settings != nil && useProxy {
		rule = m.settings.ProxyFor(settings.ScopeHTTP)
	}
	return proxyx.HTTPClient(rule, 60*time.Second)
}

func (m *Manager) fetch(ctx context.Context, src Source) UpdateResult {
	res := UpdateResult{FetchedAt: time.Now().Format(time.RFC3339)}
	if strings.TrimSpace(src.URL) == "" {
		res.Error = "订阅地址为空"
		return res
	}
	client, err := m.httpClientFor(src)
	if err != nil {
		res.Error = "代理配置无效: " + err.Error()
		return res
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "WailsTools/1.0 (subscription)")
	req.Header.Set("Accept", "*/*")
	resp, err := client.Do(req)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		res.Error = fmt.Sprintf("订阅地址返回 %d", resp.StatusCode)
		return res
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	parsed := Parse(string(body))
	parsed.Info = MergeInfo(parsed.Info, ParseUserInfo(resp.Header.Get("subscription-userinfo")))
	if len(parsed.Nodes) == 0 {
		res.Error = "未解析到可用的 HTTP / SOCKS5 节点"
		return res
	}
	res.Source = parsed
	res.NodeCount = len(parsed.Nodes)
	return res
}

// StartAutoRefresh periodically refreshes subscriptions in the background.
func (m *Manager) StartAutoRefresh(ctx context.Context) {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return
	}
	inner, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-inner.Done():
				return
			case <-ticker.C:
				for _, src := range m.store.List() {
					if !src.Enabled || src.IntervalHours <= 0 {
						continue
					}
					last := parseTime(src.LastFetch)
					if time.Since(last) < time.Duration(src.IntervalHours)*time.Hour {
						continue
					}
					rctx, rcancel := context.WithTimeout(inner, 2*time.Minute)
					_, _ = m.Refresh(rctx, src.ID)
					rcancel()
				}
			}
		}
	}()
}

// StopAutoRefresh stops the background refresher.
func (m *Manager) StopAutoRefresh() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}

func parseTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t
}
