package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"wailstools/internal/serverlink"
)

// OfflineAddDownload queues a magnet link on the sync server.
func (a *App) OfflineAddDownload(magnet string) (map[string]any, error) {
	magnet = strings.TrimSpace(magnet)
	if magnet == "" {
		return nil, errors.New("请输入磁力链")
	}
	c, err := a.requireLink()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return c.OfflineAdd(ctx, magnet)
}

// OfflineListDownloads returns the server-side offline tasks.
func (a *App) OfflineListDownloads() (map[string]any, error) {
	c, err := a.requireLink()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return c.OfflineList(ctx)
}

// OfflineDeleteDownload removes a task; purge also deletes its files.
func (a *App) OfflineDeleteDownload(id string, purge bool) (map[string]any, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("缺少任务 ID")
	}
	c, err := a.requireLink()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return c.OfflineDelete(ctx, id, purge)
}

// requireLink returns the sync-server link, connecting it when needed.
func (a *App) requireLink() (*serverlink.Client, error) {
	if a.link == nil {
		return nil, errors.New("未配置同步服务器")
	}
	if !a.link.Available() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := a.link.Connect(ctx); err != nil {
			return nil, err
		}
	}
	return a.link, nil
}
