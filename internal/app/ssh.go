package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"wailstools/internal/sshmgr"
)

// ---- hosts ---------------------------------------------------------------

// ListSSHHosts returns saved hosts (secrets stripped).
func (a *App) ListSSHHosts() []sshmgr.Host {
	hosts := a.ssh.Store.ListHosts()
	for i := range hosts {
		hosts[i] = a.ssh.Store.RedactHost(hosts[i])
	}
	return hosts
}

// SaveSSHHost creates or updates a host.
func (a *App) SaveSSHHost(h sshmgr.Host) (sshmgr.Host, error) {
	return a.ssh.Store.SaveHost(h)
}

// DeleteSSHHost removes a host.
func (a *App) DeleteSSHHost(id string) error { return a.ssh.Store.DeleteHost(id) }

// GetSSHHost returns one host.
func (a *App) GetSSHHost(id string) (sshmgr.Host, error) {
	h, ok := a.ssh.Store.GetHost(id)
	if !ok {
		return sshmgr.Host{}, errors.New("ssh host not found")
	}
	return a.ssh.Store.RedactHost(h), nil
}

// ---- keys ----------------------------------------------------------------

// ListSSHKeys returns key metadata.
func (a *App) ListSSHKeys() []sshmgr.Key { return a.ssh.Store.ListKeys() }

// GenerateSSHKey creates an RSA/Ed25519/ECDSA key pair.
func (a *App) GenerateSSHKey(opts sshmgr.GenerateOptions) (sshmgr.Key, error) {
	return a.ssh.Store.GenerateKey(opts)
}

// ImportSSHKey imports an existing private key.
func (a *App) ImportSSHKey(opts sshmgr.ImportOptions) (sshmgr.Key, error) {
	return a.ssh.Store.ImportKey(opts)
}

// DeleteSSHKey removes a key.
func (a *App) DeleteSSHKey(id string) error { return a.ssh.Store.DeleteKey(id) }

// ExportSSHPrivateKey returns the private key PEM for download/backup.
func (a *App) ExportSSHPrivateKey(id string) (string, error) {
	return a.ssh.Store.ExportPrivateKey(id)
}

// SSHPublicKey returns the authorized_keys line for a key.
func (a *App) SSHPublicKey(id string) (string, error) {
	return a.ssh.Store.PublicKeyFor(id)
}

// SaveSSHPublicKeyToFile writes the public key where the user chooses.
func (a *App) SaveSSHPublicKeyToFile(id, target string) (string, error) {
	pub, err := a.ssh.Store.PublicKeyFor(id)
	if err != nil {
		return "", err
	}
	if target == "" {
		return "", errors.New("target path is required")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	return target, os.WriteFile(target, []byte(pub+"\n"), 0o644)
}

// ---- connections ---------------------------------------------------------

// ConnectSSH opens (and pools) a connection to a saved host.
func (a *App) ConnectSSH(hostID string, opts sshmgr.DialOptions) (sshmgr.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c, err := a.ssh.Connect(ctx, hostID, opts)
	if err != nil {
		return sshmgr.Client{}, err
	}
	a.emit("ssh:sessions", a.ssh.Sessions())
	return *c, nil
}

// SSHSessions lists live connections.
func (a *App) SSHSessions() []sshmgr.Client { return a.ssh.Sessions() }

// DisconnectSSH closes a session.
func (a *App) DisconnectSSH(id string) error {
	err := a.ssh.Disconnect(id)
	a.emit("ssh:sessions", a.ssh.Sessions())
	return err
}

// TestSSHHost verifies connectivity and credentials.
func (a *App) TestSSHHost(hostID string, opts sshmgr.DialOptions) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return a.ssh.TestHost(ctx, hostID, opts)
}

// RunSSHCommand executes a command on a live session.
func (a *App) RunSSHCommand(sessionID, command string, timeoutSec int) (map[string]any, error) {
	ctx := context.Background()
	timeout := time.Duration(timeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return a.ssh.RunCommand(ctx, sessionID, command, timeout)
}

// ---- sftp ----------------------------------------------------------------

// SFTPList lists a remote directory.
func (a *App) SFTPList(sessionID, dir string) ([]sshmgr.FileEntry, error) {
	return a.ssh.SFTPList(sessionID, dir)
}

// SFTPHome resolves the remote home directory.
func (a *App) SFTPHome(sessionID string) (string, error) { return a.ssh.HomeDir(sessionID) }

// SFTPMkdir creates a remote directory.
func (a *App) SFTPMkdir(sessionID, dir string) error { return a.ssh.SFTPMkdir(sessionID, dir) }

// SFTPRemove deletes a remote file or directory.
func (a *App) SFTPRemove(sessionID, path string, recursive bool) error {
	return a.ssh.SFTPRemove(sessionID, path, recursive)
}

// SFTPRename renames a remote path.
func (a *App) SFTPRename(sessionID, from, to string) error {
	return a.ssh.SFTPRename(sessionID, from, to)
}

// SFTPStat stats a remote path.
func (a *App) SFTPStat(sessionID, path string) (sshmgr.FileEntry, error) {
	return a.ssh.SFTPStat(sessionID, path)
}

// SFTPReadFile returns a text preview of a remote file.
func (a *App) SFTPReadFile(sessionID, path string) (string, error) {
	return a.ssh.SFTPReadFile(sessionID, path, 256<<10)
}

// SFTPWriteFile overwrites a remote file.
func (a *App) SFTPWriteFile(sessionID, path, content string) error {
	return a.ssh.SFTPWriteFile(sessionID, path, content)
}

// SFTPDownload downloads a remote file, emitting progress events.
func (a *App) SFTPDownload(sessionID, remotePath, localPath string) (map[string]any, error) {
	if localPath == "" {
		localPath = filepath.Join(a.downloadDir(), filepath.Base(remotePath))
	}
	ctx := context.Background()
	key := fmt.Sprintf("%s:%s", sessionID, remotePath)
	n, err := a.ssh.SFTPDownload(ctx, sessionID, remotePath, localPath, func(p sshmgr.Progress) {
		a.emit("transfer:progress", map[string]any{"key": key, "kind": "sftp-download", "progress": p})
	})
	if err != nil {
		return nil, err
	}
	a.emit("transfer:done", map[string]any{"key": key, "kind": "sftp-download", "path": localPath, "bytes": n})
	return map[string]any{"bytes": n, "path": localPath}, nil
}

// SFTPUpload uploads a local file to the remote host.
func (a *App) SFTPUpload(sessionID, localPath, remotePath string) (map[string]any, error) {
	if remotePath == "" {
		remotePath = filepath.ToSlash(filepath.Base(localPath))
	}
	ctx := context.Background()
	key := fmt.Sprintf("%s:%s", sessionID, remotePath)
	n, err := a.ssh.SFTPUpload(ctx, sessionID, localPath, remotePath, func(p sshmgr.Progress) {
		a.emit("transfer:progress", map[string]any{"key": key, "kind": "sftp-upload", "progress": p})
	})
	if err != nil {
		return nil, err
	}
	a.emit("transfer:done", map[string]any{"key": key, "kind": "sftp-upload", "path": remotePath, "bytes": n})
	return map[string]any{"bytes": n, "path": remotePath}, nil
}

// ---- interactive terminal ------------------------------------------------

// OpenTerminal starts a PTY shell and streams output through events.
func (a *App) OpenTerminal(sessionID string, cols, rows int) (string, error) {
	id := fmt.Sprintf("term-%d", time.Now().UnixNano())
	term, err := a.ssh.OpenTerminal(sessionID, sshmgr.TerminalOptions{
		Cols: cols,
		Rows: rows,
		OnData: func(b []byte) {
			a.emit("ssh:terminal:data", map[string]any{"id": id, "data": string(b)})
		},
		OnClose: func(err error) {
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			a.emit("ssh:terminal:closed", map[string]any{"id": id, "error": msg})
			a.mu.Lock()
			delete(a.terms, id)
			a.mu.Unlock()
		},
	})
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.terms[id] = term
	a.mu.Unlock()
	return id, nil
}

// WriteTerminal sends keystrokes to a PTY.
func (a *App) WriteTerminal(id, data string) error {
	a.mu.Lock()
	term := a.terms[id]
	a.mu.Unlock()
	if term == nil {
		return errors.New("terminal not found")
	}
	return term.Write([]byte(data))
}

// ResizeTerminal updates the PTY window size.
func (a *App) ResizeTerminal(id string, cols, rows int) error {
	a.mu.Lock()
	term := a.terms[id]
	a.mu.Unlock()
	if term == nil {
		return errors.New("terminal not found")
	}
	return term.Resize(cols, rows)
}

// CloseTerminal ends a PTY session.
func (a *App) CloseTerminal(id string) error {
	a.mu.Lock()
	term := a.terms[id]
	delete(a.terms, id)
	a.mu.Unlock()
	if term == nil {
		return nil
	}
	return term.Close()
}

// KnownHosts returns the TOFU host key fingerprints.
func (a *App) KnownHosts() map[string]string { return a.ssh.Store.KnownHosts() }

// TrustHostKey pins a host key fingerprint manually.
func (a *App) TrustHostKey(hostAddr, fingerprint string) error {
	return a.ssh.Store.SetKnownHostFingerprint(hostAddr, fingerprint)
}

func (a *App) downloadDir() string { return a.settings.Get().Downloads.Directory }
