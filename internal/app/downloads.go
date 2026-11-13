package app

import (
	"context"
	"time"

	"wailstools/internal/dlengine"
)

// AddDownload registers a new HTTP download task.
func (a *App) AddDownload(req dlengine.Request) (dlengine.Task, error) {
	return a.engine.Add(req)
}

// AddDownloadAndStart registers and immediately starts a task.
func (a *App) AddDownloadAndStart(req dlengine.Request) (dlengine.Task, error) {
	t, err := a.engine.Add(req)
	if err != nil {
		return dlengine.Task{}, err
	}
	return t, a.engine.Start(t.ID)
}

// StartDownload starts or resumes a task.
func (a *App) StartDownload(id string) error { return a.engine.Start(id) }

// PauseDownload pauses a task.
func (a *App) PauseDownload(id string) error { return a.engine.Pause(id) }

// CancelDownload cancels a task and cleans partial files.
func (a *App) CancelDownload(id string) error { return a.engine.Cancel(id) }

// RemoveDownload forgets a task; deleteFile removes its output too.
func (a *App) RemoveDownload(id string, deleteFile bool) error {
	return a.engine.Remove(id, deleteFile)
}

// ListDownloads returns all tasks.
func (a *App) ListDownloads() []dlengine.Task { return a.engine.List() }

// ClearFinishedDownloads removes completed/failed records.
func (a *App) ClearFinishedDownloads() { a.engine.ClearFinished() }

// PauseAllDownloads pauses every running task.
func (a *App) PauseAllDownloads() { a.engine.PauseAll() }

// DownloadDefaults reports the effective engine defaults.
func (a *App) DownloadDefaults() map[string]any {
	cfg := a.settings.Get().Downloads
	return map[string]any{
		"directory":     cfg.Directory,
		"connections":   cfg.ConnectionsPer,
		"concurrency":   cfg.MaxConcurrency,
		"chunkSize":     cfg.ChunkSize,
		"userAgent":     cfg.UserAgent,
		"speedLimitKBs": cfg.SpeedLimitKBps,
		"retries":       cfg.RetryCount,
	}
}

// ProbeURL checks whether a URL is downloadable and reports its metadata.
func (a *App) ProbeURL(rawURL string) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.engine.Probe(ctx, rawURL)
}
