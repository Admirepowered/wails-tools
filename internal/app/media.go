package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wailstools/internal/paths"
)

// mediaRegistry maps opaque tokens to absolute file paths so the WebView can
// stream local/cloud/SFTP files without exposing the filesystem.
type mediaRegistry struct {
	mu    sync.Mutex
	items map[string]string
}

func newMediaRegistry() *mediaRegistry {
	return &mediaRegistry{items: map[string]string{}}
}

func (m *mediaRegistry) add(path string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	token := hex.EncodeToString(b[:])
	m.mu.Lock()
	m.items[token] = path
	m.mu.Unlock()
	return token
}

func (m *mediaRegistry) get(token string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, ok := m.items[token]
	return path, ok
}

// MediaHandler serves /media/<token> for registered files with Range support.
func (a *App) MediaHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/media/", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.URL.Path, "/media/")
		if token == "" {
			http.NotFound(w, r)
			return
		}
		if a.media == nil {
			http.Error(w, "media unavailable", http.StatusServiceUnavailable)
			return
		}
		path, ok := a.media.get(token)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if _, err := os.Stat(path); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, path)
	})
	return mux
}

// RegisterMedia exposes a local file to the WebView and returns its URL.
func (a *App) RegisterMedia(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	if a.media == nil {
		return "", errors.New("media registry unavailable")
	}
	return "/media/" + a.media.add(path), nil
}

// PreviewCloudMedia downloads a cloud file to the cache and registers it.
func (a *App) PreviewCloudMedia(remotePath string) (string, error) {
	if a.link == nil || !a.link.Available() {
		return "", errors.New("同步服务器未连接")
	}
	dir, err := paths.Dir("cache", "media")
	if err != nil {
		return "", err
	}
	name := filepath.Base(remotePath)
	target := filepath.Join(dir, name)
	if st, err := os.Stat(target); err == nil && st.Size() > 0 {
		return a.RegisterMedia(target)
	}
	if _, err := a.link.Download(context.Background(), remotePath, target, nil); err != nil {
		return "", err
	}
	return a.RegisterMedia(target)
}

// PreviewSFTPMedia downloads an SFTP file to the cache and registers it.
func (a *App) PreviewSFTPMedia(sessionID, remotePath string) (string, error) {
	dir, err := paths.Dir("cache", "media")
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, filepath.Base(remotePath))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := a.ssh.SFTPDownloadFrom(ctx, sessionID, remotePath, target, 0, nil); err != nil {
		return "", err
	}
	return a.RegisterMedia(target)
}

// MediaKind classifies a path for the viewer.
func MediaKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg", ".ico", ".avif":
		return "image"
	case ".mp4", ".webm", ".mkv", ".mov", ".m4v", ".avi", ".ogv":
		return "video"
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac":
		return "audio"
	case ".txt", ".md", ".log", ".json", ".yaml", ".yml", ".toml", ".ini", ".conf", ".xml", ".csv":
		return "text"
	}
	return ""
}
