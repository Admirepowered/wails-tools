package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"wailstools/internal/wire"
	"wailstools/server/internal/store"
)

// ---- update distribution -------------------------------------------------

type updateListReq struct {
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	Channel  string `json:"channel"`
}

func (c *callCtx) updateList(body json.RawMessage) (any, error) {
	var req updateListReq
	_ = wire.Decode(body, &req)
	list, err := c.api.DB.ListReleases(req.Platform, req.Channel)
	if err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	if req.Arch != "" {
		filtered := list[:0]
		for _, r := range list {
			if strings.EqualFold(r.Arch, req.Arch) {
				filtered = append(filtered, r)
			}
		}
		list = filtered
	}
	if list == nil {
		list = []store.Release{}
	}
	return map[string]any{"releases": list, "serverTime": time.Now().UnixMilli()}, nil
}

func (c *callCtx) updateLatest(body json.RawMessage) (any, error) {
	var req updateListReq
	_ = wire.Decode(body, &req)
	list, err := c.api.DB.ListReleases(req.Platform, req.Channel)
	if err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	best := store.Release{}
	found := false
	for _, r := range list {
		if req.Arch != "" && !strings.EqualFold(r.Arch, req.Arch) {
			continue
		}
		if !found || compareVersions(r.Version, best.Version) > 0 {
			best, found = r, true
		}
	}
	if !found {
		return map[string]any{"release": nil, "found": false}, nil
	}
	return map[string]any{"release": best, "found": true}, nil
}

// compareVersions compares dotted numeric versions ("1.2.10" > "1.2.9").
func compareVersions(a, b string) int {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var va, vb int
		if i < len(pa) {
			va, _ = strconv.Atoi(strings.TrimSpace(pa[i]))
		}
		if i < len(pb) {
			vb, _ = strconv.Atoi(strings.TrimSpace(pb[i]))
		}
		if va != vb {
			if va > vb {
				return 1
			}
			return -1
		}
	}
	return 0
}

// adminOnly resolves the caller and verifies the admin role.
func (c *callCtx) adminOnly() (store.User, error) {
	uid, err := c.userID()
	if err != nil {
		return store.User{}, err
	}
	u, err := c.api.DB.GetUser(uid)
	if err != nil {
		return store.User{}, wire.Errorf("not_found", "user not found")
	}
	if u.Role != "admin" {
		return store.User{}, wire.Errorf("forbidden", "administrator role required")
	}
	return u, nil
}

type releaseUploadReq struct {
	Platform  string `json:"platform"`
	Arch      string `json:"arch"`
	Channel   string `json:"channel"`
	Version   string `json:"version"`
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Notes     string `json:"notes"`
	Mandatory bool   `json:"mandatory"`
}

type releaseUploadAck struct {
	OK      bool           `json:"ok"`
	ID      string         `json:"id"`
	Error   string         `json:"error,omitempty"`
	Detail  string         `json:"detail,omitempty"`
	Release *store.Release `json:"release,omitempty"`
}

// updatePut streams a client build into the release store (admin only).
func (c *callCtx) updatePut(body json.RawMessage, conn net.Conn) error {
	ack := func(a releaseUploadAck) { _ = wire.WriteFrame(conn, a) }
	u, err := c.adminOnly()
	if err != nil {
		ack(releaseUploadAck{Error: "forbidden"})
		return err
	}
	var req releaseUploadReq
	if err := wire.Decode(body, &req); err != nil {
		ack(releaseUploadAck{Error: "bad_request", Detail: err.Error()})
		return wire.Errorf("bad_request", "invalid body: %v", err)
	}
	if req.Size <= 0 || req.Size > 512<<20 {
		ack(releaseUploadAck{Error: "bad_request", Detail: "invalid size"})
		return wire.Errorf("bad_request", "invalid size")
	}
	if strings.TrimSpace(req.Version) == "" || strings.TrimSpace(req.Filename) == "" {
		ack(releaseUploadAck{Error: "bad_request", Detail: "version and filename are required"})
		return wire.Errorf("bad_request", "version and filename are required")
	}
	rel := store.Release{
		Platform:   strings.ToLower(req.Platform),
		Arch:       strings.ToLower(req.Arch),
		Channel:    strings.ToLower(req.Channel),
		Version:    strings.TrimSpace(req.Version),
		Filename:   filepath.Base(req.Filename),
		Size:       req.Size,
		SHA256:     strings.ToLower(strings.TrimSpace(req.SHA256)),
		Notes:      req.Notes,
		UploadedBy: u.Username,
		Mandatory:  req.Mandatory,
	}
	blobPath, err := c.api.DB.PutRelease(rel)
	if err != nil {
		ack(releaseUploadAck{Error: "internal", Detail: err.Error()})
		return wire.Errorf("internal", "%v", err)
	}
	f, err := os.Create(blobPath)
	if err != nil {
		ack(releaseUploadAck{Error: "internal", Detail: err.Error()})
		return wire.Errorf("internal", "%v", err)
	}
	hasher := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(f, hasher), conn, req.Size)
	syncErr := f.Sync()
	_ = f.Close()
	if copyErr != nil || written != req.Size {
		_ = os.Remove(blobPath)
		ack(releaseUploadAck{Error: "incomplete", Detail: "short upload"})
		return wire.Errorf("incomplete", "received %d of %d bytes", written, req.Size)
	}
	if syncErr != nil {
		ack(releaseUploadAck{Error: "internal", Detail: syncErr.Error()})
		return wire.Errorf("internal", "%v", syncErr)
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if rel.SHA256 == "" {
		rel.SHA256 = sum
	}
	// Find the generated id from the stored metadata.
	list, _ := c.api.DB.ListReleases(rel.Platform, rel.Channel)
	for _, candidate := range list {
		if candidate.Filename == rel.Filename && candidate.Version == rel.Version && candidate.Size == rel.Size {
			rel.ID = candidate.ID
			rel.UploadedAt = candidate.UploadedAt
			break
		}
	}
	if err := c.api.DB.UpdateRelease(rel); err != nil {
		ack(releaseUploadAck{Error: "internal", Detail: err.Error()})
		return wire.Errorf("internal", "%v", err)
	}
	c.api.DB.Audit(store.AuditEntry{UserID: u.ID, Action: "release-upload", Detail: rel.Version + "/" + rel.Platform})
	ack(releaseUploadAck{OK: true, ID: rel.ID, Release: &rel})
	return nil
}

type releaseRefReq struct {
	ID string `json:"id"`
}

func (c *callCtx) updateDelete(body json.RawMessage) (any, error) {
	u, err := c.adminOnly()
	if err != nil {
		return nil, err
	}
	var req releaseRefReq
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	if err := c.api.DB.DeleteRelease(req.ID); err != nil {
		return nil, wire.Errorf("not_found", "release not found")
	}
	c.api.DB.Audit(store.AuditEntry{UserID: u.ID, Action: "release-delete", Detail: req.ID})
	return map[string]any{"ok": true}, nil
}

// updateGet streams a release binary to the client.
func (c *callCtx) updateGet(body json.RawMessage, conn net.Conn) error {
	header := func(ok bool, size int64, name, msg string) {
		_ = wire.WriteFrame(conn, map[string]any{"ok": ok, "size": size, "filename": name, "error": msg})
	}
	var req releaseRefReq
	if err := wire.Decode(body, &req); err != nil {
		header(false, 0, "", "bad_request")
		return wire.Errorf("bad_request", "invalid body: %v", err)
	}
	rel, err := c.api.DB.GetRelease(req.ID)
	if err != nil {
		header(false, 0, "", "not_found")
		return wire.Errorf("not_found", "release not found")
	}
	blob := c.api.DB.ReleaseBlobPath(rel)
	f, err := os.Open(blob)
	if err != nil {
		header(false, 0, "", "not_found")
		return wire.Errorf("not_found", "release file missing")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		header(false, 0, "", "internal")
		return wire.Errorf("internal", "%v", err)
	}
	if err := wire.WriteFrame(conn, map[string]any{"ok": true, "size": info.Size(), "filename": rel.Filename, "sha256": rel.SHA256, "version": rel.Version}); err != nil {
		return err
	}
	c.api.DB.IncrementReleaseDownloads(rel.ID)
	_, err = io.CopyN(conn, f, info.Size())
	return err
}

// sortReleasesByVersion is a helper used by tests.
func sortReleasesByVersion(list []store.Release) {
	sort.SliceStable(list, func(i, j int) bool {
		return compareVersions(list[i].Version, list[j].Version) > 0
	})
}

var _ = fmt.Sprintf
