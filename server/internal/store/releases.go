package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketReleases = []byte("releases")

// Release is one uploaded client build.
type Release struct {
	ID         string `json:"id"`
	Platform   string `json:"platform"` // windows | linux | darwin
	Arch       string `json:"arch"`     // amd64 | arm64
	Channel    string `json:"channel"`  // stable | beta
	Version    string `json:"version"`
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Notes      string `json:"notes,omitempty"`
	UploadedBy string `json:"uploadedBy,omitempty"`
	UploadedAt string `json:"uploadedAt"`
	Downloads  int64  `json:"downloads"`
	Mandatory  bool   `json:"mandatory"`
}

// EnsureReleasesBucket makes sure the bucket exists (older databases).
func (d *DB) EnsureReleasesBucket() error {
	return d.db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketReleases)
		return err
	})
}

// PutRelease stores metadata and returns the blob path to write.
func (d *DB) PutRelease(r Release) (string, error) {
	if r.ID == "" {
		r.ID = "rel-" + strings.ReplaceAll(strings.ToLower(time.Now().UTC().Format("20060102T150405.000000")), ".", "")
	}
	if r.UploadedAt == "" {
		r.UploadedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if r.Channel == "" {
		r.Channel = "stable"
	}
	if r.Platform == "" || r.Arch == "" {
		return "", errors.New("platform and arch are required")
	}
	dir := filepath.Join(d.DataDir(), "..", "releases", r.ID)
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	blob := filepath.Join(dir, filepath.Base(r.Filename))
	if err := d.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketReleases), r.ID, r)
	}); err != nil {
		return "", err
	}
	return blob, nil
}

// UpdateRelease rewrites release metadata (used to record size/sha after upload).
func (d *DB) UpdateRelease(r Release) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketReleases), r.ID, r)
	})
}

// GetRelease loads one release.
func (d *DB) GetRelease(id string) (Release, error) {
	var r Release
	err := d.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx.Bucket(bucketReleases), id, &r)
	})
	return r, err
}

// ReleaseBlobPath resolves the stored file for a release.
func (d *DB) ReleaseBlobPath(r Release) string {
	dir, err := filepath.Abs(filepath.Join(d.DataDir(), "..", "releases", r.ID))
	if err != nil {
		return ""
	}
	return filepath.Join(dir, filepath.Base(r.Filename))
}

// ListReleases returns releases filtered by platform/channel (empty = all).
func (d *DB) ListReleases(platform, channel string) ([]Release, error) {
	var out []Release
	err := d.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketReleases).ForEach(func(_, v []byte) error {
			var r Release
			if json.Unmarshal(v, &r) != nil {
				return nil
			}
			if platform != "" && !strings.EqualFold(r.Platform, platform) {
				return nil
			}
			if channel != "" && !strings.EqualFold(r.Channel, channel) {
				return nil
			}
			out = append(out, r)
			return nil
		})
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UploadedAt != out[j].UploadedAt {
			return out[i].UploadedAt > out[j].UploadedAt
		}
		return out[i].Version > out[j].Version
	})
	return out, err
}

// DeleteRelease removes metadata and the stored blob.
func (d *DB) DeleteRelease(id string) error {
	r, err := d.GetRelease(id)
	if err != nil {
		return err
	}
	if path := d.ReleaseBlobPath(r); path != "" {
		_ = os.RemoveAll(filepath.Dir(path))
	}
	return d.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketReleases).Delete([]byte(id))
	})
}

// IncrementReleaseDownloads bumps the download counter.
func (d *DB) IncrementReleaseDownloads(id string) {
	r, err := d.GetRelease(id)
	if err != nil {
		return
	}
	r.Downloads++
	_ = d.UpdateRelease(r)
}
