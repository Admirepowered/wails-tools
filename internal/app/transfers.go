package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"wailstools/internal/ftpx"
	"wailstools/internal/sshmgr"
	"wailstools/internal/transfermgr"
)

// initTransfers wires the transfer manager (called from New).
func (a *App) initTransfers() {
	a.transfers = transfermgr.New(func(item transfermgr.Item) {
		a.emit("transfer:task", item)
	})
}

// ListTransfers returns the transfer history (newest first).
func (a *App) ListTransfers() []transfermgr.Item {
	if a.transfers == nil {
		return []transfermgr.Item{}
	}
	list := a.transfers.List()
	if list == nil {
		return []transfermgr.Item{}
	}
	return list
}

// PauseTransfer pauses a running transfer.
func (a *App) PauseTransfer(id string) error {
	if a.transfers == nil {
		return errors.New("传输管理器未初始化")
	}
	return a.transfers.Pause(id)
}

// ResumeTransfer resumes a paused transfer from its offset.
func (a *App) ResumeTransfer(id string) error {
	if a.transfers == nil {
		return errors.New("传输管理器未初始化")
	}
	return a.transfers.Resume(id)
}

// CancelTransfer aborts a transfer.
func (a *App) CancelTransfer(id string) error {
	if a.transfers == nil {
		return errors.New("传输管理器未初始化")
	}
	return a.transfers.Cancel(id)
}

// RemoveTransfer forgets a finished transfer.
func (a *App) RemoveTransfer(id string) error {
	if a.transfers == nil {
		return errors.New("传输管理器未初始化")
	}
	return a.transfers.Remove(id)
}

// ---- SFTP ----------------------------------------------------------------

// startSFTPDownload registers a resumable download.
func (a *App) startSFTPDownload(sessionID, remotePath, localPath string) (string, error) {
	total := int64(0)
	if st, err := a.ssh.SFTPStat(sessionID, remotePath); err == nil {
		total = st.Size
	}
	key := fmt.Sprintf("%s:%s", sessionID, remotePath)
	id := a.transfers.Start("sftp-download", filepath.Base(remotePath), localPath, total, func(ctx context.Context, offset int64, progress func(int64)) (int64, error) {
		return a.ssh.SFTPDownloadFrom(ctx, sessionID, remotePath, localPath, offset, func(p sshmgr.Progress) {
			progress(p.Done)
			a.emit("transfer:progress", map[string]any{"key": key, "kind": "sftp-download", "progress": p})
		})
	})
	return id, nil
}

// startSFTPUpload registers a resumable upload.
func (a *App) startSFTPUpload(sessionID, localPath, remotePath string) (string, error) {
	total := int64(0)
	if info, err := os.Stat(localPath); err == nil {
		total = info.Size()
	}
	key := fmt.Sprintf("%s:%s", sessionID, remotePath)
	id := a.transfers.Start("sftp-upload", filepath.Base(localPath), remotePath, total, func(ctx context.Context, offset int64, progress func(int64)) (int64, error) {
		return a.ssh.SFTPUploadFrom(ctx, sessionID, localPath, remotePath, offset, func(p sshmgr.Progress) {
			progress(p.Done)
			a.emit("transfer:progress", map[string]any{"key": key, "kind": "sftp-upload", "progress": p})
		})
	})
	return id, nil
}

// ---- FTP -----------------------------------------------------------------

func (a *App) startFTPDownload(sessionID, remotePath, localPath string) (string, error) {
	total := int64(0)
	if c, ok := a.ftp.get(sessionID); ok {
		if entries, err := c.List(filepath.ToSlash(filepath.Dir(remotePath))); err == nil {
			base := filepath.Base(remotePath)
			for _, e := range entries {
				if e.Name == base {
					total = e.Size
				}
			}
		}
	}
	key := fmt.Sprintf("%s:%s", sessionID, remotePath)
	id := a.transfers.Start("ftp-download", filepath.Base(remotePath), localPath, total, func(ctx context.Context, offset int64, progress func(int64)) (int64, error) {
		c, ok := a.ftp.get(sessionID)
		if !ok {
			return offset, errors.New("ftp session not found")
		}
		return c.DownloadFrom(ctx, remotePath, localPath, offset, func(p ftpx.Progress) {
			progress(p.Done)
			a.emit("transfer:progress", map[string]any{"key": key, "kind": "ftp-download", "progress": p})
		})
	})
	return id, nil
}

func (a *App) startFTPUpload(sessionID, localPath, remotePath string) (string, error) {
	total := int64(0)
	if info, err := os.Stat(localPath); err == nil {
		total = info.Size()
	}
	key := fmt.Sprintf("%s:%s", sessionID, remotePath)
	id := a.transfers.Start("ftp-upload", filepath.Base(localPath), remotePath, total, func(ctx context.Context, offset int64, progress func(int64)) (int64, error) {
		c, ok := a.ftp.get(sessionID)
		if !ok {
			return offset, errors.New("ftp session not found")
		}
		return c.UploadFrom(ctx, localPath, remotePath, offset, func(p ftpx.Progress) {
			progress(p.Done)
			a.emit("transfer:progress", map[string]any{"key": key, "kind": "ftp-upload", "progress": p})
		})
	})
	return id, nil
}

// ---- cloud ---------------------------------------------------------------

func (a *App) startCloudUpload(localPath, remotePath string) (string, error) {
	total := int64(0)
	if info, err := os.Stat(localPath); err == nil {
		total = info.Size()
	}
	key := "upload:" + localPath
	id := a.transfers.Start("cloud-upload", filepath.Base(localPath), remotePath, total, func(ctx context.Context, offset int64, progress func(int64)) (int64, error) {
		return a.link.UploadFrom(ctx, localPath, remotePath, offset, func(done, total int64) {
			progress(done)
			a.emit("transfer:progress", map[string]any{"key": key, "kind": "cloud-upload", "done": done, "total": total})
		})
	})
	return id, nil
}

func (a *App) startCloudDownload(remotePath, localPath string) (string, error) {
	total := int64(0)
	if st, err := a.link.StatRemote(context.Background(), remotePath); err == nil {
		total = st
	}
	key := "cloud:" + remotePath
	id := a.transfers.Start("cloud-download", filepath.Base(remotePath), localPath, total, func(ctx context.Context, offset int64, progress func(int64)) (int64, error) {
		return a.link.DownloadFrom(ctx, remotePath, localPath, offset, func(done, total int64) {
			progress(done)
			a.emit("transfer:progress", map[string]any{"key": key, "kind": "cloud-download", "done": done, "total": total})
		})
	})
	return id, nil
}

// ---- helpers -------------------------------------------------------------

func uniqueLocalPath(dir, name string) string {
	target := filepath.Join(dir, name)
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return target
	}
	ext := filepath.Ext(name)
	base := name[:len(name)-len(ext)]
	for i := 1; i < 1000; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, time.Now().UnixNano(), ext))
}
