package app

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"wailstools/internal/wire"
)

// File management over a punched channel ("p2p.fs.*").
//
// The peer exposes a small, path-confined file API rooted at the download
// directory so the two machines can push/pull files without the sync server.

// p2pFsRoot is the directory shared with the peer.
func (a *App) p2pFsRoot() string {
	dir := a.settings.Get().Downloads.Directory
	if dir == "" {
		dir = a.rootDir
	}
	return dir
}

// p2pFsPath resolves a peer-supplied path. Absolute paths (including Windows
// drive letters such as D:\Media) are accepted only for trusted devices, i.e.
// the account's own machines; relative paths always resolve inside the shared
// download directory.
func (a *App) p2pFsPath(rel string, trusted bool) (string, error) {
	rel = strings.TrimSpace(rel)
	root := a.p2pFsRoot()
	if rel == "" || rel == "/" || rel == "\\" {
		return root, nil
	}
	if filepath.IsAbs(rel) || (len(rel) >= 2 && rel[1] == ':' && isDriveLetter(rel[0])) {
		if !trusted {
			return "", errors.New("对方设备不在本账号下，只能访问共享目录")
		}
		return filepath.Clean(rel), nil
	}
	clean := filepath.Clean("/" + strings.ReplaceAll(rel, "\\", "/"))
	full := filepath.Join(root, filepath.FromSlash(clean))
	if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
		return "", errors.New("路径越界")
	}
	return full, nil
}

// isDriveLetter reports whether c looks like a Windows drive letter.
func isDriveLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

type p2pFsEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Dir      bool   `json:"dir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

func (a *App) p2pFsList(rel string, trusted bool) ([]p2pFsEntry, error) {
	full, err := a.p2pFsPath(rel, trusted)
	if err != nil {
		return nil, err
	}
	items, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	out := make([]p2pFsEntry, 0, len(items))
	for _, it := range items {
		info, ierr := it.Info()
		if ierr != nil {
			continue
		}
		child := filepath.ToSlash(filepath.Join(rel, it.Name()))
		out = append(out, p2pFsEntry{
			Name: it.Name(), Path: child, Dir: it.IsDir(), Size: info.Size(),
			Modified: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// ---- peer side (serving) -------------------------------------------------

func (h *p2pWireHandler) handleFs(method string, body json.RawMessage) (any, error) {
	var req struct {
		Path string `json:"path"`
	}
	if len(body) > 0 {
		if err := wire.Decode(body, &req); err != nil {
			return nil, wire.Errorf("bad_request", "invalid body: %v", err)
		}
	}
	switch method {
	case "p2p.fs.list":
		list, err := h.app.p2pFsList(req.Path, h.trust)
		if err != nil {
			return nil, wire.Errorf("fs", "%v", err)
		}
		return map[string]any{"path": req.Path, "root": h.app.p2pFsRoot(), "entries": list}, nil
	case "p2p.fs.mkdir":
		full, err := h.app.p2pFsPath(req.Path, h.trust)
		if err != nil {
			return nil, wire.Errorf("fs", "%v", err)
		}
		if err := os.MkdirAll(full, 0o755); err != nil {
			return nil, wire.Errorf("fs", "%v", err)
		}
		return map[string]any{"ok": true}, nil
	case "p2p.fs.remove":
		full, err := h.app.p2pFsPath(req.Path, h.trust)
		if err != nil {
			return nil, wire.Errorf("fs", "%v", err)
		}
		if err := os.RemoveAll(full); err != nil {
			return nil, wire.Errorf("fs", "%v", err)
		}
		return map[string]any{"ok": true}, nil
	}
	return nil, wire.Errorf("unknown_method", "unsupported fs method %q", method)
}

// pipeFs serves the streaming file ops.
func (h *p2pWireHandler) pipeFs(method string, body json.RawMessage, conn net.Conn) error {
	var req struct {
		Path string `json:"path"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return wire.Errorf("bad_request", "invalid body: %v", err)
	}
	full, err := h.app.p2pFsPath(req.Path, h.trust)
	if err != nil {
		return wire.Errorf("forbidden", "%v", err)
	}
	switch method {
	case "p2p.fs.read":
		f, err := os.Open(full)
		if err != nil {
			return wire.Errorf("fs", "%v", err)
		}
		defer f.Close()
		_, err = io.Copy(conn, f)
		return err
	case "p2p.fs.write":
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return wire.Errorf("fs", "%v", err)
		}
		f, err := os.Create(full)
		if err != nil {
			return wire.Errorf("fs", "%v", err)
		}
		defer f.Close()
		_, err = io.Copy(f, conn)
		return err
	}
	return wire.Errorf("unknown_method", "unsupported fs stream %q", method)
}
