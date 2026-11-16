package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// OfflineTask is a server-side magnet download.
type OfflineTask struct {
	ID     string `json:"id"`
	UserID string `json:"userId"`
	// Kind is "magnet" or "url".
	Kind   string `json:"kind"`
	Magnet string `json:"magnet,omitempty"`
	URL    string `json:"url,omitempty"`
	// Headers are sent with an HTTP download (Cookie/User-Agent/Referer/...).
	Headers   map[string]string `json:"headers,omitempty"`
	Name      string            `json:"name"`
	Status    string            `json:"status"` // queued|downloading|completed|failed|canceled
	Total     int64             `json:"total"`
	Done      int64             `json:"done"`
	Files     int               `json:"files"`
	Error     string            `json:"error,omitempty"`
	Dir       string            `json:"dir,omitempty"` // relative to the user's files root
	CreatedAt string            `json:"createdAt"`
	UpdatedAt string            `json:"updatedAt"`
}

func offlineKey(id string) []byte { return []byte("offline:" + id) }

// PutOfflineTask inserts or updates a task.
func (d *DB) PutOfflineTask(t OfflineTask) error {
	t.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if t.CreatedAt == "" {
		t.CreatedAt = t.UpdatedAt
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return errors.New("meta bucket missing")
		}
		return b.Put(offlineKey(t.ID), raw)
	})
}

// GetOfflineTask loads one task.
func (d *DB) GetOfflineTask(id string) (OfflineTask, bool) {
	var t OfflineTask
	found := false
	_ = d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return nil
		}
		if raw := b.Get(offlineKey(id)); raw != nil {
			if json.Unmarshal(raw, &t) == nil {
				found = true
			}
		}
		return nil
	})
	return t, found
}

// ListOfflineTasks returns the tasks of one user (newest last).
func (d *DB) ListOfflineTasks(userID string) []OfflineTask {
	var out []OfflineTask
	_ = d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			if len(k) < 8 || string(k[:8]) != "offline:" {
				return nil
			}
			var t OfflineTask
			if json.Unmarshal(v, &t) != nil || t.UserID != userID {
				return nil
			}
			out = append(out, t)
			return nil
		})
	})
	return out
}

// DeleteOfflineTask removes a task record.
func (d *DB) DeleteOfflineTask(id string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return fmt.Errorf("meta bucket missing")
		}
		return b.Delete(offlineKey(id))
	})
}
