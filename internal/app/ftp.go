package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sync"
	"time"

	"wailstools/internal/ftpx"
	"wailstools/internal/paths"
	"wailstools/internal/secretstore"
	"wailstools/internal/settings"
)

// ftpHub owns FTP profiles and live sessions.
type ftpHub struct {
	store    *ftpx.ProfileStore
	settings *settings.Store

	mu       sync.Mutex
	sessions map[string]*ftpx.Client
	seq      int
}

func newFTPHub(store *settings.Store, vault *secretstore.Vault) *ftpHub {
	profPath, err := paths.Join("ftp", "profiles.json")
	if err != nil {
		profPath = "ftp-profiles.json"
	}
	ps, err := ftpx.OpenProfiles(profPath, vault)
	if err != nil {
		ps = &ftpx.ProfileStore{}
	}
	return &ftpHub{store: ps, settings: store, sessions: map[string]*ftpx.Client{}}
}

func (h *ftpHub) dialer() *ftpx.Dialer {
	return ftpx.NewDialer(h.settings.ProxyFor(settings.ScopeFTP))
}

func (h *ftpHub) closeAll() {
	h.mu.Lock()
	all := h.sessions
	h.sessions = map[string]*ftpx.Client{}
	h.mu.Unlock()
	for _, c := range all {
		_ = c.Close()
	}
}

func (h *ftpHub) get(id string) (*ftpx.Client, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.sessions[id]
	return c, ok
}

// ---- profiles ------------------------------------------------------------

// ListFTPProfiles returns saved FTP connections.
func (a *App) ListFTPProfiles() []ftpx.Profile { return a.ftp.store.List() }

// SaveFTPProfile creates or updates an FTP profile.
func (a *App) SaveFTPProfile(p ftpx.Profile) (ftpx.Profile, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if p.CreatedAt == "" {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	return a.ftp.store.Save(p)
}

// DeleteFTPProfile removes a profile.
func (a *App) DeleteFTPProfile(id string) error { return a.ftp.store.Delete(id) }

// ---- sessions ------------------------------------------------------------

// FTPSession describes a live FTP connection.
type FTPSession struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Config   ftpx.Config `json:"config"`
	OpenedAt string      `json:"openedAt"`
	Path     string      `json:"path"`
}

// ConnectFTP opens a connection for a saved profile.
func (a *App) ConnectFTP(profileID string) (FTPSession, error) {
	var cfg ftpx.Config
	name := "ftp"
	pass := ""
	for _, p := range a.ftp.store.List() {
		if p.ID == profileID {
			cfg = p.Config
			name = p.Name
			break
		}
	}
	if cfg.Host == "" {
		return FTPSession{}, errors.New("ftp profile not found")
	}
	sealedPass, user, err := a.ftp.store.PasswordFor(profileID)
	if err == nil {
		pass = sealedPass
		cfg.User = user
	}
	cfg.Password = pass
	return a.connectFTPWith(cfg, name, profileID)
}

// ConnectFTPDirect opens an ad-hoc connection (not saved).
func (a *App) ConnectFTPDirect(cfg ftpx.Config) (FTPSession, error) {
	name := cfg.Name
	if name == "" {
		name = cfg.Host
	}
	return a.connectFTPWith(cfg, name, "")
}

func (a *App) connectFTPWith(cfg ftpx.Config, name, profileID string) (FTPSession, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	c, err := a.ftp.dialer().Connect(ctx, cfg, "")
	if err != nil {
		return FTPSession{}, err
	}
	a.ftp.mu.Lock()
	a.ftp.seq++
	id := fmt.Sprintf("ftp-%d", a.ftp.seq)
	a.ftp.sessions[id] = c
	a.ftp.mu.Unlock()
	wd, _ := c.Pwd()
	sess := FTPSession{ID: id, Name: name, Config: maskConfig(cfg), OpenedAt: time.Now().Format(time.RFC3339), Path: wd}
	a.emit("ftp:sessions", a.FTPSessions())
	return sess, nil
}

func maskConfig(c ftpx.Config) ftpx.Config {
	c.Password = ""
	return c
}

// FTPSessions lists live connections.
func (a *App) FTPSessions() []FTPSession {
	a.ftp.mu.Lock()
	defer a.ftp.mu.Unlock()
	out := make([]FTPSession, 0, len(a.ftp.sessions))
	for id, c := range a.ftp.sessions {
		wd, _ := c.Pwd()
		out = append(out, FTPSession{ID: id, Name: c.Config().Host, Config: maskConfig(c.Config()), Path: wd})
	}
	return out
}

// DisconnectFTP closes a session.
func (a *App) DisconnectFTP(id string) error {
	a.ftp.mu.Lock()
	c, ok := a.ftp.sessions[id]
	if ok {
		delete(a.ftp.sessions, id)
	}
	a.ftp.mu.Unlock()
	if !ok {
		return errors.New("ftp session not found")
	}
	err := c.Close()
	a.emit("ftp:sessions", a.FTPSessions())
	return err
}

// FTPList lists a remote directory.
func (a *App) FTPList(sessionID, dir string) (map[string]any, error) {
	c, ok := a.ftp.get(sessionID)
	if !ok {
		return nil, errors.New("ftp session not found")
	}
	entries, err := c.List(dir)
	if err != nil {
		return nil, err
	}
	wd, _ := c.Pwd()
	parent := path.Dir(wd)
	if parent == wd {
		parent = ""
	}
	return map[string]any{"path": wd, "parent": parent, "entries": entries}, nil
}

// FTPMkdir creates a remote directory.
func (a *App) FTPMkdir(sessionID, dir string) error {
	c, ok := a.ftp.get(sessionID)
	if !ok {
		return errors.New("ftp session not found")
	}
	return c.Mkdir(dir)
}

// FTPRemove deletes a remote path.
func (a *App) FTPRemove(sessionID, target string, isDir bool) error {
	c, ok := a.ftp.get(sessionID)
	if !ok {
		return errors.New("ftp session not found")
	}
	if isDir {
		return c.RemoveDir(target)
	}
	return c.Remove(target)
}

// FTPRename renames a remote path.
func (a *App) FTPRename(sessionID, from, to string) error {
	c, ok := a.ftp.get(sessionID)
	if !ok {
		return errors.New("ftp session not found")
	}
	return c.Rename(from, to)
}

// FTPDownload starts a resumable FTP download (pause via PauseTransfer).
func (a *App) FTPDownload(sessionID, remotePath, localPath string) (map[string]any, error) {
	if _, ok := a.ftp.get(sessionID); !ok {
		return nil, errors.New("ftp session not found")
	}
	if localPath == "" {
		localPath = uniqueLocalPath(a.downloadDir(), path.Base(remotePath))
	}
	id, err := a.startFTPDownload(sessionID, remotePath, localPath)
	if err != nil {
		return nil, err
	}
	return map[string]any{"transferId": id, "path": localPath}, nil
}

// FTPUpload starts a resumable FTP upload.
func (a *App) FTPUpload(sessionID, localPath, remotePath string) (map[string]any, error) {
	if _, ok := a.ftp.get(sessionID); !ok {
		return nil, errors.New("ftp session not found")
	}
	if remotePath == "" {
		remotePath = path.Base(filepath.ToSlash(localPath))
	}
	id, err := a.startFTPUpload(sessionID, localPath, remotePath)
	if err != nil {
		return nil, err
	}
	return map[string]any{"transferId": id, "path": remotePath}, nil
}
