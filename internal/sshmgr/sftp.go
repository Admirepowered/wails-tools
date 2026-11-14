package sshmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

// FileEntry describes one remote directory entry.
type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	IsLink  bool   `json:"isLink"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"modTime"`
}

// Progress reports transfer progress.
type Progress struct {
	Done     int64   `json:"done"`
	Total    int64   `json:"total"`
	Percent  float64 `json:"percent"`
	SpeedBps float64 `json:"speedBps"`
	Path     string  `json:"path"`
	Bytes    int64   `json:"bytesDelta"`
}

func (m *Manager) sftpFor(sessionID string) (*sftp.Client, func(), error) {
	c, ok := m.Client(sessionID)
	if !ok || c.SSH == nil {
		return nil, nil, errors.New("ssh session not found")
	}
	cli, err := sftp.NewClient(c.SSH, sftp.UseConcurrentWrites(true), sftp.UseConcurrentReads(true))
	if err != nil {
		return nil, nil, err
	}
	return cli, func() { _ = cli.Close() }, nil
}

// SFTPList lists a remote directory.
func (m *Manager) SFTPList(sessionID, dir string) ([]FileEntry, error) {
	cli, closeFn, err := m.sftpFor(sessionID)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	if strings.TrimSpace(dir) == "" {
		dir, err = cli.Getwd()
		if err != nil {
			dir = "/"
		}
	}
	infos, err := cli.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(infos))
	for _, fi := range infos {
		e := FileEntry{
			Name:    fi.Name(),
			Path:    path.Join(dir, fi.Name()),
			IsDir:   fi.IsDir(),
			IsLink:  fi.Mode()&os.ModeSymlink != 0,
			Size:    fi.Size(),
			Mode:    fi.Mode().String(),
			ModTime: fi.ModTime().UTC().Format(time.RFC3339),
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// SFTPStat stats a remote path.
func (m *Manager) SFTPStat(sessionID, remotePath string) (FileEntry, error) {
	cli, closeFn, err := m.sftpFor(sessionID)
	if err != nil {
		return FileEntry{}, err
	}
	defer closeFn()
	fi, err := cli.Stat(remotePath)
	if err != nil {
		return FileEntry{}, err
	}
	return FileEntry{
		Name:    fi.Name(),
		Path:    remotePath,
		IsDir:   fi.IsDir(),
		Size:    fi.Size(),
		Mode:    fi.Mode().String(),
		ModTime: fi.ModTime().UTC().Format(time.RFC3339),
	}, nil
}

// SFTPMkdir creates a remote directory (recursively).
func (m *Manager) SFTPMkdir(sessionID, remotePath string) error {
	cli, closeFn, err := m.sftpFor(sessionID)
	if err != nil {
		return err
	}
	defer closeFn()
	return cli.MkdirAll(remotePath)
}

// SFTPRemove deletes a file or directory.
func (m *Manager) SFTPRemove(sessionID, remotePath string, recursive bool) error {
	cli, closeFn, err := m.sftpFor(sessionID)
	if err != nil {
		return err
	}
	defer closeFn()
	if !recursive {
		fi, err := cli.Stat(remotePath)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return cli.RemoveDirectory(remotePath)
		}
		return cli.Remove(remotePath)
	}
	var walk func(p string) error
	walk = func(p string) error {
		entries, err := cli.ReadDir(p)
		if err != nil {
			return err
		}
		for _, e := range entries {
			child := path.Join(p, e.Name())
			if e.IsDir() {
				if err := walk(child); err != nil {
					return err
				}
				continue
			}
			if err := cli.Remove(child); err != nil {
				return err
			}
		}
		return cli.RemoveDirectory(p)
	}
	return walk(remotePath)
}

// SFTPRename renames a remote path.
func (m *Manager) SFTPRename(sessionID, from, to string) error {
	cli, closeFn, err := m.sftpFor(sessionID)
	if err != nil {
		return err
	}
	defer closeFn()
	return cli.Rename(from, to)
}

// SFTPDownload copies a remote file to localPath, reporting progress.
func (m *Manager) SFTPDownload(ctx context.Context, sessionID, remotePath, localPath string, onProgress func(Progress)) (int64, error) {
	cli, closeFn, err := m.sftpFor(sessionID)
	if err != nil {
		return 0, err
	}
	defer closeFn()

	fi, err := cli.Stat(remotePath)
	if err != nil {
		return 0, err
	}
	if fi.IsDir() {
		return 0, errors.New("remote path is a directory; enable recursive download")
	}
	rf, err := cli.Open(remotePath)
	if err != nil {
		return 0, err
	}
	defer rf.Close()

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return 0, err
	}
	lf, err := os.Create(localPath)
	if err != nil {
		return 0, err
	}
	defer lf.Close()

	total := fi.Size()
	var done int64
	start := time.Now()
	last := start
	buf := make([]byte, 512*1024)
	for {
		select {
		case <-ctx.Done():
			return done, ctx.Err()
		default:
		}
		n, rerr := rf.Read(buf)
		if n > 0 {
			if _, werr := lf.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			now := time.Now()
			if onProgress != nil && now.Sub(last) > 120*time.Millisecond {
				last = now
				onProgress(buildProgress(remotePath, done, total, start))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return done, rerr
		}
	}
	if onProgress != nil {
		onProgress(buildProgress(remotePath, done, total, start))
	}
	return done, lf.Sync()
}

func buildProgress(p string, done, total int64, start time.Time) Progress {
	elapsed := time.Since(start).Seconds()
	pr := Progress{Done: done, Total: total, Path: p}
	if total > 0 {
		pr.Percent = float64(done) / float64(total) * 100
	}
	if elapsed > 0 {
		pr.SpeedBps = float64(done) / elapsed
	}
	return pr
}

// SFTPUpload copies a local file to the remote host.
func (m *Manager) SFTPUpload(ctx context.Context, sessionID, localPath, remotePath string, onProgress func(Progress)) (int64, error) {
	cli, closeFn, err := m.sftpFor(sessionID)
	if err != nil {
		return 0, err
	}
	defer closeFn()

	lf, err := os.Open(localPath)
	if err != nil {
		return 0, err
	}
	defer lf.Close()
	st, err := lf.Stat()
	if err != nil {
		return 0, err
	}
	if st.IsDir() {
		return 0, errors.New("local path is a directory")
	}
	if dir := path.Dir(remotePath); dir != "" && dir != "." {
		_ = cli.MkdirAll(dir)
	}
	rf, err := cli.Create(remotePath)
	if err != nil {
		return 0, err
	}
	defer rf.Close()

	total := st.Size()
	var done int64
	start := time.Now()
	last := start
	buf := make([]byte, 512*1024)
	for {
		select {
		case <-ctx.Done():
			return done, ctx.Err()
		default:
		}
		n, rerr := lf.Read(buf)
		if n > 0 {
			if _, werr := rf.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			now := time.Now()
			if onProgress != nil && now.Sub(last) > 120*time.Millisecond {
				last = now
				onProgress(buildProgress(remotePath, done, total, start))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return done, rerr
		}
	}
	if onProgress != nil {
		onProgress(buildProgress(remotePath, done, total, start))
	}
	return done, nil
}

// SFTPReadFile returns a bounded file preview.
func (m *Manager) SFTPReadFile(sessionID, remotePath string, maxBytes int64) (string, error) {
	cli, closeFn, err := m.sftpFor(sessionID)
	if err != nil {
		return "", err
	}
	defer closeFn()
	f, err := cli.Open(remotePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if maxBytes <= 0 {
		maxBytes = 256 << 10
	}
	buf := make([]byte, maxBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", err
	}
	return string(buf[:n]), nil
}

// SFTPWriteFile overwrites a remote file with the given content.
func (m *Manager) SFTPWriteFile(sessionID, remotePath, content string) error {
	cli, closeFn, err := m.sftpFor(sessionID)
	if err != nil {
		return err
	}
	defer closeFn()
	if dir := path.Dir(remotePath); dir != "" && dir != "." {
		_ = cli.MkdirAll(dir)
	}
	f, err := cli.Create(remotePath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write([]byte(content))
	return err
}

// HomeDir resolves the remote home directory.
func (m *Manager) HomeDir(sessionID string) (string, error) {
	cli, closeFn, err := m.sftpFor(sessionID)
	if err != nil {
		return "", err
	}
	defer closeFn()
	wd, err := cli.Getwd()
	if err != nil {
		return "/", nil
	}
	return wd, nil
}

// CheckFTPPathMatches is a helper used by the UI to normalize separators.
func CheckFTPPathMatches(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

var _ = fmt.Sprintf

// SFTPDownloadFrom resumes a remote download at offset.
func (m *Manager) SFTPDownloadFrom(ctx context.Context, sessionID, remotePath, localPath string, offset int64, onProgress func(Progress)) (int64, error) {
	cli, closeFn, err := m.sftpFor(sessionID)
	if err != nil {
		return 0, err
	}
	defer closeFn()

	fi, err := cli.Stat(remotePath)
	if err != nil {
		return offset, err
	}
	if fi.IsDir() {
		return offset, errors.New("remote path is a directory")
	}
	rf, err := cli.Open(remotePath)
	if err != nil {
		return offset, err
	}
	defer rf.Close()
	if offset > 0 {
		if _, err := rf.Seek(offset, io.SeekStart); err != nil {
			return offset, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return offset, err
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	lf, err := os.OpenFile(localPath, flags, 0o644)
	if err != nil {
		return offset, err
	}
	defer lf.Close()

	total := fi.Size()
	done := offset
	start := time.Now()
	last := start
	buf := make([]byte, 512*1024)
	for {
		select {
		case <-ctx.Done():
			return done, ctx.Err()
		default:
		}
		n, rerr := rf.Read(buf)
		if n > 0 {
			if _, werr := lf.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			now := time.Now()
			if onProgress != nil && now.Sub(last) > 120*time.Millisecond {
				last = now
				onProgress(buildProgress(remotePath, done, total, start))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return done, rerr
		}
	}
	if onProgress != nil {
		onProgress(buildProgress(remotePath, done, total, start))
	}
	return done, lf.Sync()
}

// SFTPUploadFrom resumes a remote upload at offset.
func (m *Manager) SFTPUploadFrom(ctx context.Context, sessionID, localPath, remotePath string, offset int64, onProgress func(Progress)) (int64, error) {
	cli, closeFn, err := m.sftpFor(sessionID)
	if err != nil {
		return 0, err
	}
	defer closeFn()

	lf, err := os.Open(localPath)
	if err != nil {
		return offset, err
	}
	defer lf.Close()
	st, err := lf.Stat()
	if err != nil {
		return offset, err
	}
	if offset > 0 {
		if _, err := lf.Seek(offset, io.SeekStart); err != nil {
			return offset, err
		}
	}
	if dir := path.Dir(remotePath); dir != "" && dir != "." {
		_ = cli.MkdirAll(dir)
	}
	rf, err := cli.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return offset, err
	}
	defer rf.Close()
	if offset > 0 {
		if _, err := rf.Seek(offset, io.SeekStart); err != nil {
			return offset, err
		}
	}

	total := st.Size()
	done := offset
	start := time.Now()
	last := start
	buf := make([]byte, 512*1024)
	for {
		select {
		case <-ctx.Done():
			return done, ctx.Err()
		default:
		}
		n, rerr := lf.Read(buf)
		if n > 0 {
			if _, werr := rf.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			now := time.Now()
			if onProgress != nil && now.Sub(last) > 120*time.Millisecond {
				last = now
				onProgress(buildProgress(remotePath, done, total, start))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return done, rerr
		}
	}
	if onProgress != nil {
		onProgress(buildProgress(remotePath, done, total, start))
	}
	return done, nil
}
