package app

import (
	"encoding/json"
	"os"
	"path/filepath"

	"wailstools/internal/settings"
)

// 代理链路与直连规则独立落盘，避免被整份 settings 覆盖。
func (a *App) chainFile() string { return filepath.Join(a.rootDir, "proxy-chain.json") }
func (a *App) rulesFile() string { return filepath.Join(a.rootDir, "direct-rules.json") }

func (a *App) loadChainFile() (settings.ProxyChainSettings, bool) {
	var cfg settings.ProxyChainSettings
	data, err := os.ReadFile(a.chainFile())
	if err != nil || len(data) == 0 {
		return cfg, false
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, false
	}
	return cfg, true
}

func (a *App) saveChainFile(cfg settings.ProxyChainSettings) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.chainFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.chainFile())
}

func (a *App) loadRulesFile() (settings.DirectRuleSettings, bool) {
	var cfg settings.DirectRuleSettings
	data, err := os.ReadFile(a.rulesFile())
	if err != nil || len(data) == 0 {
		return cfg, false
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, false
	}
	return cfg, true
}

func (a *App) saveRulesFile(cfg settings.DirectRuleSettings) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.rulesFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.rulesFile())
}

// chainOrDefault returns the persisted chain, falling back to settings.
func (a *App) chainOrDefault() settings.ProxyChainSettings {
	if cfg, ok := a.loadChainFile(); ok {
		return cfg
	}
	return a.settings.Get().ProxyChain
}

// rulesOrDefault returns the persisted direct rules, falling back to settings.
func (a *App) rulesOrDefault() settings.DirectRuleSettings {
	if cfg, ok := a.loadRulesFile(); ok {
		return cfg
	}
	return a.settings.Get().DirectRules
}
