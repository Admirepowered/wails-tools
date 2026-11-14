package store

import (
	"encoding/json"
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketVault = []byte("vault")

// VaultBlob is one end-to-end encrypted client secret bundle. The server only
// ever sees ciphertext: the key is derived from the account password on the
// client and never transmitted.
type VaultBlob struct {
	Kind      string `json:"kind"`
	Data      string `json:"data"`
	UpdatedAt string `json:"updatedAt"`
	Size      int    `json:"size"`
}

func vaultKey(userID, kind string) []byte { return []byte(userID + "\x00" + kind) }

// GetVault returns the blob stored for a kind.
func (d *DB) GetVault(userID, kind string) (VaultBlob, bool) {
	var b VaultBlob
	found := false
	_ = d.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketVault)
		if bucket == nil {
			return nil
		}
		raw := bucket.Get(vaultKey(userID, kind))
		if raw == nil {
			return nil
		}
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil
		}
		found = true
		return nil
	})
	return b, found
}

// PutVault stores (or replaces) a blob for a kind.
func (d *DB) PutVault(userID, kind, data string) (VaultBlob, error) {
	if userID == "" || kind == "" {
		return VaultBlob{}, errors.New("invalid vault key")
	}
	b := VaultBlob{
		Kind:      kind,
		Data:      data,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Size:      len(data),
	}
	err := d.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketVault)
		if bucket == nil {
			return errors.New("vault bucket missing")
		}
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		return bucket.Put(vaultKey(userID, kind), raw)
	})
	return b, err
}

// ListVault returns the blob metadata for a user (ciphertext omitted).
func (d *DB) ListVault(userID string) []VaultBlob {
	out := []VaultBlob{}
	prefix := []byte(userID + "\x00")
	_ = d.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketVault)
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(k, v []byte) error {
			if len(k) < len(prefix) || string(k[:len(prefix)]) != string(prefix) {
				return nil
			}
			var b VaultBlob
			if err := json.Unmarshal(v, &b); err != nil {
				return nil
			}
			b.Data = ""
			out = append(out, b)
			return nil
		})
	})
	return out
}

// DeleteVault removes one kind.
func (d *DB) DeleteVault(userID, kind string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketVault)
		if bucket == nil {
			return nil
		}
		return bucket.Delete(vaultKey(userID, kind))
	})
}

// WipeVault removes every blob of a user (used when the password changes,
// because the old ciphertext can never be opened again).
func (d *DB) WipeVault(userID string) (int, error) {
	prefix := []byte(userID + "\x00")
	removed := 0
	err := d.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketVault)
		if bucket == nil {
			return nil
		}
		keys := [][]byte{}
		if err := bucket.ForEach(func(k, _ []byte) error {
			if len(k) >= len(prefix) && string(k[:len(prefix)]) == string(prefix) {
				keys = append(keys, append([]byte(nil), k...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range keys {
			if err := bucket.Delete(k); err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	return removed, err
}
