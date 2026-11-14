package api

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"wailstools/internal/wire"
	"wailstools/server/internal/store"
)

// ---- subscriptions -------------------------------------------------------

// subsPut stores the caller's subscription document (last write wins).
func (c *callCtx) subsPut(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req struct {
		Subscriptions json.RawMessage `json:"subscriptions"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	if len(req.Subscriptions) == 0 {
		return nil, wire.Errorf("bad_request", "subscriptions payload is required")
	}
	updatedAt, err := c.api.DB.PutSubscriptions(uid, req.Subscriptions)
	if err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	return map[string]any{"ok": true, "updatedAt": updatedAt}, nil
}

// subsGet returns the caller's subscription document.
func (c *callCtx) subsGet() (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	doc, found := c.api.DB.GetSubscriptions(uid)
	if !found {
		return map[string]any{"found": false, "subscriptions": []any{}, "updatedAt": 0}, nil
	}
	return map[string]any{"found": true, "subscriptions": doc.Payload, "updatedAt": doc.UpdatedAt}, nil
}

// ---- online archive extraction ------------------------------------------

type extractReq struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

// filesExtract unpacks an archive stored in the user's cloud space.
func (c *callCtx) filesExtract(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req extractReq
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	clean, err := safeRel(req.Path)
	if err != nil {
		return nil, err
	}
	if clean == "" {
		return nil, wire.Errorf("bad_request", "path is required")
	}
	src, err := c.absPath(uid, clean)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(src)
	if err != nil || info.IsDir() {
		return nil, wire.Errorf("not_found", "archive not found")
	}

	base := strings.TrimSuffix(path.Base(clean), path.Ext(clean))
	if strings.HasSuffix(strings.ToLower(clean), ".tar.gz") || strings.HasSuffix(strings.ToLower(clean), ".tgz") {
		base = strings.TrimSuffix(strings.TrimSuffix(path.Base(clean), ".gz"), ".tar")
	}
	if base == "" {
		base = "extracted"
	}
	targetRel := req.Target
	if targetRel == "" {
		targetRel = path.Join(path.Dir(clean), base)
	}
	targetClean, err := safeRel(targetRel)
	if err != nil {
		return nil, err
	}
	dest, err := c.absPath(uid, targetClean)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}

	lower := strings.ToLower(clean)
	var extracted int
	switch {
	case strings.HasSuffix(lower, ".zip"):
		extracted, err = extractZip(src, dest)
	case strings.HasSuffix(lower, ".tar"):
		extracted, err = extractTar(src, dest, false)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		extracted, err = extractTar(src, dest, true)
	default:
		return nil, wire.Errorf("unsupported", "仅支持 zip / tar / tar.gz 解压")
	}
	if err != nil {
		return nil, wire.Errorf("internal", "解压失败: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_ = c.api.DB.PutFileMeta(store.FileMeta{Path: "/" + targetClean, UserID: uid, IsDir: true, UpdatedAt: now, CreatedAt: now})
	c.api.DB.Audit(store.AuditEntry{UserID: uid, Action: "extract", Detail: clean})
	return map[string]any{"ok": true, "dir": "/" + targetClean, "entries": extracted}, nil
}

// safeJoin prevents archive entries from escaping the destination directory.
func safeJoin(dest, name string) (string, error) {
	clean := path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || strings.Contains(clean, "..") {
		return "", errors.New("unsafe archive entry")
	}
	full := filepath.Join(dest, filepath.FromSlash(clean))
	if !strings.HasPrefix(filepath.Clean(full), filepath.Clean(dest)) {
		return "", errors.New("unsafe archive entry")
	}
	return full, nil
}

func extractZip(src, dest string) (int, error) {
	reader, err := zip.OpenReader(src)
	if err != nil {
		return 0, err
	}
	defer reader.Close()
	count := 0
	for _, f := range reader.File {
		full, err := safeJoin(dest, f.Name)
		if err != nil {
			continue
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(full, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return count, err
		}
		rc, err := f.Open()
		if err != nil {
			return count, err
		}
		out, err := os.Create(full)
		if err != nil {
			_ = rc.Close()
			return count, err
		}
		if _, err := io.Copy(out, io.LimitReader(rc, 512<<20)); err != nil {
			_ = out.Close()
			_ = rc.Close()
			return count, err
		}
		_ = out.Close()
		_ = rc.Close()
		count++
	}
	return count, nil
}

func extractTar(src, dest string, gzipped bool) (int, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var reader io.Reader = f
	if gzipped {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return 0, err
		}
		defer gz.Close()
		reader = gz
	}
	tr := tar.NewReader(reader)
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, err
		}
		full, err := safeJoin(dest, hdr.Name)
		if err != nil {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(full, 0o755)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return count, err
			}
			out, err := os.Create(full)
			if err != nil {
				return count, err
			}
			if _, err := io.Copy(out, io.LimitReader(tr, 512<<20)); err != nil {
				_ = out.Close()
				return count, err
			}
			_ = out.Close()
			count++
		}
	}
	return count, nil
}
