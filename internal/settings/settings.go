// Package settings persists the client configuration (proxies, download
// defaults, server endpoint and UI preferences) in a single JSON document.
package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"wailstools/internal/paths"
)

// Proxy scopes. Each scope can independently opt into the proxy.
const (
	ScopeHTTP     = "http"    // HTTP downloads and the download tools
	ScopeNetdisk  = "netdisk" // cloud-drive (netdisk) providers
	ScopeSSH      = "ssh"     // SSH / SFTP connections
	ScopeFTP      = "ftp"     // FTP connections
	ScopeSync     = "sync"    // client <-> server sync traffic
	ScopeAll      = "all"     // fallback entry
	proxyModeOff  = "off"
	proxyModeHTTP = "http"
	proxyModeSOC5 = "socks5"
)

// Scopes lists the scopes surfaced in the proxy settings page.
var Scopes = []string{ScopeHTTP, ScopeNetdisk, ScopeSSH, ScopeFTP, ScopeSync}

// ProxyRule is a single proxy definition.
type ProxyRule struct {
	Mode     string `json:"mode"` // off | http | socks5
	URL      string `json:"url"`  // host:port or full URL
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// NoProxy lists hosts that must always bypass the proxy.
	NoProxy []string `json:"noProxy,omitempty"`
}

// Enabled reports whether the rule instructs traffic to use a proxy.
func (r ProxyRule) Enabled() bool {
	if r.Mode == "" || r.Mode == proxyModeOff {
		return false
	}
	return strings.TrimSpace(r.URL) != ""
}

// Clone returns a deep copy.
func (r ProxyRule) Clone() ProxyRule {
	out := r
	out.NoProxy = append([]string(nil), r.NoProxy...)
	return out
}

// ProxySettings holds the global rule plus per-scope overrides.
type ProxySettings struct {
	// Mode is "off" or "internal" (route every scope through the local engine).
	Mode    string               `json:"mode"`
	Default ProxyRule            `json:"default"`
	Scopes  map[string]ProxyRule `json:"scopes,omitempty"`
	// FollowSystem additionally consults the OS proxy environment when no rule
	// is configured for a scope.
	FollowSystem bool `json:"followSystem"`
}

// Resolve returns the effective rule for a scope.
func (p ProxySettings) Resolve(scope string) ProxyRule {
	if p.Scopes != nil {
		if rule, ok := p.Scopes[scope]; ok {
			return rule.Clone()
		}
		if rule, ok := p.Scopes[ScopeAll]; ok {
			return rule.Clone()
		}
	}
	return p.Default.Clone()
}

// ProxyChainSettings routes the engine's upstream through the sync server or
// through one or two existing nodes (max two hops).
type ProxyChainSettings struct {
	// Mode is "off", "server" or "nodes".
	Mode string `json:"mode"`
	// NodeIDs lists custom node IDs used as hops (max 2).
	NodeIDs []string `json:"nodeIds,omitempty"`
}

// DirectRuleSettings decides which destinations bypass the proxy.
type DirectRuleSettings struct {
	// UseGeoIP enables country based bypass using geoip.metadb.
	UseGeoIP bool `json:"useGeoIP"`
	// Countries lists ISO country codes that go direct (e.g. CN).
	Countries []string `json:"countries,omitempty"`
	// Rules lists domain suffixes, keywords or CIDRs that go direct.
	Rules []string `json:"rules,omitempty"`
}

// ProxyRuntimeSettings configures the local proxy engine listener.
type ProxyRuntimeSettings struct {
	// External exposes the local proxy on 0.0.0.0 for other devices.
	External bool `json:"external"`
	// Port is the listen port used in external mode.
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// SystemProxy mirrors the local proxy into the OS proxy settings.
	SystemProxy bool `json:"systemProxy"`
}

// DownloadSettings tunes the HTTP download engine.
type DownloadSettings struct {
	Directory       string `json:"directory"`
	MaxConcurrency  int    `json:"maxConcurrency"`
	ConnectionsPer  int    `json:"connectionsPerTask"`
	ChunkSize       int64  `json:"chunkSizeBytes"`
	UserAgent       string `json:"userAgent"`
	SpeedLimitKBps  int    `json:"speedLimitKBps"`
	RetryCount      int    `json:"retryCount"`
	AutoResume      bool   `json:"autoResume"`
	VerifyChecksums bool   `json:"verifyChecksums"`
}

// ServerSettings is the sync server endpoint configuration.
type ServerSettings struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Token         string `json:"token,omitempty"`
	Username      string `json:"username,omitempty"`
	Pin           string `json:"pin,omitempty"`
	PreferUDP     bool   `json:"preferUdp"`
	DisableUDP    bool   `json:"disableUdp"`
	AutoConnect   bool   `json:"autoConnect"`
	LastUsed      string `json:"lastUsedTransport,omitempty"`
	AllowInsecure bool   `json:"allowInsecure"`
}

// UISettings captures presentation preferences.
type UISettings struct {
	Theme    string `json:"theme"`
	Language string `json:"language"`
	Density  string `json:"density"`
	// LastLocalDir remembers the last local folder visited by the file manager.
	LastLocalDir string `json:"lastLocalDir,omitempty"`
}

// UpdateSettings configures the auto-update behaviour.
type UpdateSettings struct {
	// Source is auto | server | github.
	Source         string `json:"source"`
	Channel        string `json:"channel"`
	AutoCheck      bool   `json:"autoCheck"`
	AutoInstall    bool   `json:"autoInstall"`
	GitHubRepo     string `json:"githubRepo"`
	GitHubBypass   bool   `json:"githubBypass"`
	GitHubFragment bool   `json:"githubFragment"`
	LastCheckAt    string `json:"lastCheckAt,omitempty"`
}

// ScreenshotSettings configures capture + auto-save.
type ScreenshotSettings struct {
	Directory        string `json:"directory"`
	Pattern          string `json:"pattern"`
	Format           string `json:"format"`
	AutoSave         bool   `json:"autoSave"`
	WatchClipboard   bool   `json:"watchClipboard"`
	CopyToClipboard  bool   `json:"copyToClipboard"`
	OpenAfterCapture bool   `json:"openAfterCapture"`
	// Hotkey triggers a screenshot globally, e.g. "Ctrl+Alt+S".
	Hotkey string `json:"hotkey"`
	// AutoUpload mirrors new screenshots into the cloud store.
	AutoUpload bool `json:"autoUpload"`
}

// StartupSettings controls launch-at-login and the system tray.
type StartupSettings struct {
	Enabled        bool `json:"enabled"`
	StartMinimized bool `json:"startMinimized"`
	// TrayEnabled installs the system tray icon.
	TrayEnabled bool `json:"trayEnabled"`
	// CloseToTray hides the window instead of quitting when it is closed.
	CloseToTray bool `json:"closeToTray"`
}

// Settings is the root document.
type Settings struct {
	Proxy        ProxySettings        `json:"proxy"`
	Downloads    DownloadSettings     `json:"downloads"`
	Server       ServerSettings       `json:"server"`
	UI           UISettings           `json:"ui"`
	ProxyRuntime ProxyRuntimeSettings `json:"proxyRuntime"`
	ProxyChain   ProxyChainSettings   `json:"proxyChain"`
	DirectRules  DirectRuleSettings   `json:"directRules"`
	Update       UpdateSettings       `json:"update"`
	Screenshots  ScreenshotSettings   `json:"screenshots"`
	Startup      StartupSettings      `json:"startup"`
}

// Default returns the built-in configuration.
func Default() Settings {
	dir, _ := paths.Root()
	dl := filepath.Join(dir, "downloads")
	return Settings{
		Proxy: ProxySettings{Mode: "off", Default: ProxyRule{Mode: proxyModeOff}},
		Downloads: DownloadSettings{
			Directory:      dl,
			MaxConcurrency: 3,
			ConnectionsPer: 8,
			ChunkSize:      8 << 20,
			UserAgent:      "WailsTools/1.0",
			RetryCount:     3,
			AutoResume:     true,
		},
		Server: ServerSettings{
			Host:        "s2.admirecn.de",
			Port:        8642,
			PreferUDP:   true,
			AutoConnect: true,
		},
		UI: UISettings{Theme: "dark", Language: "zh-CN", Density: "comfortable"},
		Update: UpdateSettings{
			Source:       "server",
			Channel:      "stable",
			AutoCheck:    true,
			GitHubRepo:   "Admirepowered/wails-tools",
			GitHubBypass: true,
		},
		Screenshots: ScreenshotSettings{
			Directory:  filepath.Join(dir, "screenshots"),
			Pattern:    "shot-{date}-{time}",
			Format:     "png",
			AutoSave:   true,
			Hotkey:     "Ctrl+Alt+S",
			AutoUpload: true,
		},
		ProxyChain:   ProxyChainSettings{Mode: "off"},
		DirectRules:  DirectRuleSettings{UseGeoIP: false},
		Startup:      StartupSettings{TrayEnabled: true, CloseToTray: true},
		ProxyRuntime: ProxyRuntimeSettings{Port: 7890},
	}
}

// Store reads and writes settings with atomic replacement.
type Store struct {
	mu   sync.RWMutex
	path string
	cur  Settings
}

// NewStore loads (or creates) the settings file.
func NewStore(path string) (*Store, error) {
	if path == "" {
		p, err := paths.Join("settings.json")
		if err != nil {
			return nil, err
		}
		path = p
	}
	s := &Store{path: path, cur: Default()}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.persist(s.cur)
	}
	if err != nil {
		return err
	}
	cur := Default()
	if err := json.Unmarshal(data, &cur); err != nil {
		return err
	}
	cur.normalize()
	s.cur = cur
	return nil
}

func (c *Settings) normalize() {
	def := Default()
	if c.Downloads.Directory == "" {
		c.Downloads.Directory = def.Downloads.Directory
	}
	if c.Downloads.MaxConcurrency <= 0 {
		c.Downloads.MaxConcurrency = def.Downloads.MaxConcurrency
	}
	if c.Downloads.ConnectionsPer <= 0 {
		c.Downloads.ConnectionsPer = def.Downloads.ConnectionsPer
	}
	if c.Downloads.ChunkSize <= 0 {
		c.Downloads.ChunkSize = def.Downloads.ChunkSize
	}
	if c.Downloads.UserAgent == "" {
		c.Downloads.UserAgent = def.Downloads.UserAgent
	}
	if c.UI.Theme == "" {
		c.UI.Theme = def.UI.Theme
	}
	if c.UI.Language == "" {
		c.UI.Language = def.UI.Language
	}
	if c.Server.Host == "" {
		c.Server.Host = def.Server.Host
	}
	if c.Server.Port == 0 {
		c.Server.Port = def.Server.Port
	}
	if c.Proxy.Default.Mode == "" {
		c.Proxy.Default.Mode = proxyModeOff
	}
	if c.Proxy.Mode == "" {
		c.Proxy.Mode = "off"
	}
	if c.Update.Source == "" {
		c.Update.Source = "server"
	}
	if c.Update.Channel == "" {
		c.Update.Channel = "stable"
	}
	if c.Update.GitHubRepo == "" {
		c.Update.GitHubRepo = "Admirepowered/wails-tools"
	}
	if c.Screenshots.Directory == "" {
		c.Screenshots.Directory = filepath.Join(def.Downloads.Directory, "..", "screenshots")
	}
	if c.Screenshots.Pattern == "" {
		c.Screenshots.Pattern = "shot-{date}-{time}"
	}
	if c.Screenshots.Format == "" {
		c.Screenshots.Format = "png"
	}
	if c.Proxy.Scopes != nil {
		for k, v := range c.Proxy.Scopes {
			if v.Mode == "" {
				v.Mode = proxyModeOff
				c.Proxy.Scopes[k] = v
			}
		}
	}
}

// Get returns a copy of the current settings.
func (s *Store) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.cur
	out.Proxy = cloneProxy(s.cur.Proxy)
	return out
}

func cloneProxy(p ProxySettings) ProxySettings {
	out := p
	out.Default = p.Default.Clone()
	if p.Scopes != nil {
		out.Scopes = make(map[string]ProxyRule, len(p.Scopes))
		for k, v := range p.Scopes {
			out.Scopes[k] = v.Clone()
		}
	}
	return out
}

// Update applies fn to the current settings and persists the result.
func (s *Store) Update(fn func(*Settings) error) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cur
	next.Proxy = cloneProxy(s.cur.Proxy)
	if err := fn(&next); err != nil {
		return s.cur, err
	}
	next.normalize()
	if err := s.persist(next); err != nil {
		return s.cur, err
	}
	s.cur = next
	return next, nil
}

// Save replaces the entire document.
func (s *Store) Save(next Settings) (Settings, error) {
	return s.Update(func(cur *Settings) error {
		*cur = next
		return nil
	})
}

// ProxyFor returns the effective proxy rule for a scope.
func (s *Store) ProxyFor(scope string) ProxyRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur.Proxy.Resolve(scope)
}

// SetScopeProxy updates one scope's rule.
func (s *Store) SetScopeProxy(scope string, rule ProxyRule) (Settings, error) {
	return s.Update(func(cur *Settings) error {
		if cur.Proxy.Scopes == nil {
			cur.Proxy.Scopes = map[string]ProxyRule{}
		}
		if rule.Mode == "" {
			rule.Mode = proxyModeOff
		}
		cur.Proxy.Scopes[scope] = rule
		return nil
	})
}

// ClearScopeProxy removes the override for a scope, falling back to default.
func (s *Store) ClearScopeProxy(scope string) (Settings, error) {
	return s.Update(func(cur *Settings) error {
		delete(cur.Proxy.Scopes, scope)
		return nil
	})
}

func (s *Store) persist(next Settings) error {
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Path exposes the backing file location.
func (s *Store) Path() string { return s.path }
