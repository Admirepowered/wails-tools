package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"wailstools/internal/githubx"
	"wailstools/internal/settings"
	"wailstools/internal/updater"
	"wailstools/internal/version"
)

// UpdateStatus is the UI-facing update state.
type UpdateStatus struct {
	Current   string           `json:"current"`
	Latest    string           `json:"latest"`
	Available bool             `json:"available"`
	Source    string           `json:"source"`
	Message   string           `json:"message"`
	CheckedAt string           `json:"checkedAt"`
	Error     string           `json:"error,omitempty"`
	Transport string           `json:"transport,omitempty"`
	Release   *updater.Release `json:"release,omitempty"`
}

// GetUpdateSettings returns the update configuration.
func (a *App) GetUpdateSettings() settings.UpdateSettings {
	return a.settings.Get().Update
}

// SaveUpdateSettings stores the update configuration.
func (a *App) SaveUpdateSettings(next settings.UpdateSettings) (settings.UpdateSettings, error) {
	cfg, err := a.settings.Update(func(s *settings.Settings) error {
		s.Update = next
		return nil
	})
	return cfg.Update, err
}

// CheckUpdate queries the configured sources.
func (a *App) CheckUpdate() (UpdateStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := a.updater.Check(ctx)
	st := UpdateStatus{
		Current:   res.Current,
		Latest:    res.Latest,
		Available: res.Available,
		Source:    res.Source,
		Message:   res.Message,
		CheckedAt: res.CheckedAt,
		Error:     res.Error,
		Transport: updater.DescribeTransport(res.GitHub),
		Release:   res.Release,
	}
	if err != nil {
		return st, err
	}
	return st, nil
}

// DownloadUpdate fetches the given release and verifies its checksum.
func (a *App) DownloadUpdate(rel updater.Release) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	key := "update:" + rel.Version
	path, err := a.updater.Download(ctx, rel, func(done, total int64) {
		a.emit("transfer:progress", map[string]any{"key": key, "kind": "update", "done": done, "total": total})
	})
	if err != nil {
		return "", err
	}
	a.emit("transfer:done", map[string]any{"key": key, "kind": "update", "path": path})
	return path, nil
}

// ApplyUpdate replaces the running build and restarts the app.
func (a *App) ApplyUpdate(stagedPath string) (string, error) {
	if stagedPath == "" {
		return "", errors.New("没有可用的更新文件")
	}
	exe, err := a.updater.Apply(stagedPath)
	if err != nil {
		return "", err
	}
	// Give the new process a moment to start before this one exits.
	go func() {
		time.Sleep(600 * time.Millisecond)
		a.quit()
	}()
	return exe, nil
}

// newGitHubClient builds the SNI-bypassing client from the settings.
func (a *App) newGitHubClient(cfg settings.UpdateSettings) *githubx.Client {
	return githubx.New(githubx.Config{
		Fragment: cfg.GitHubFragment,
		Timeout:  25 * time.Second,
	})
}

// UpdateCurrentVersion returns the running version.
func (a *App) UpdateCurrentVersion() string { return version.Version }

// UploadCurrentBuild publishes the running executable to the sync server so
// other clients can auto-update from it (admin only).
func (a *App) UploadCurrentBuild(channel, notes string, mandatory bool) (map[string]any, error) {
	if !a.link.Available() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := a.link.Connect(ctx); err != nil {
			return nil, err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(exe)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	key := "update-upload:" + version.Version
	res, err := a.link.UploadRelease(ctx, exe, map[string]any{
		"platform":  runtime.GOOS,
		"arch":      runtime.GOARCH,
		"channel":   channel,
		"version":   version.Version,
		"filename":  filepath.Base(exe),
		"size":      info.Size(),
		"notes":     notes,
		"mandatory": mandatory,
	}, func(done, total int64) {
		a.emit("transfer:progress", map[string]any{"key": key, "kind": "update-upload", "done": done, "total": total})
	})
	if err != nil {
		return nil, err
	}
	a.emit("transfer:done", map[string]any{"key": key, "kind": "update-upload", "path": exe, "bytes": info.Size()})
	return res, nil
}

// ListServerReleases lists builds stored on the sync server.
func (a *App) ListServerReleases(platform, channel string) ([]updater.Release, error) {
	if !a.link.Available() {
		return nil, errors.New("同步服务器未连接")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out struct {
		Releases []updater.Release `json:"releases"`
	}
	if err := a.link.Call(ctx, "update.list", map[string]any{
		"platform": platform, "channel": channel,
	}, &out); err != nil {
		return nil, err
	}
	if out.Releases == nil {
		return []updater.Release{}, nil
	}
	return out.Releases, nil
}

// DeleteServerRelease removes a stored build (admin only).
func (a *App) DeleteServerRelease(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return a.link.Call(ctx, "update.delete", map[string]any{"id": id}, nil)
}

// ProbeGitHub checks GitHub reachability and reports the working strategy.
func (a *App) ProbeGitHub() (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cfg := a.settings.Get().Update
	client := a.newGitHubClient(cfg)
	res := client.Probe(ctx)
	return map[string]any{
		"ok":        res.OK,
		"latencyMs": res.LatencyMS,
		"usedIp":    res.UsedIP,
		"fragment":  res.Fragment,
		"error":     res.Error,
		"attempts":  res.Attempts,
		"strategy":  updater.DescribeTransport(&res),
	}, nil
}
