package store

import (
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketSubs = []byte("subscriptions")

// SubscriptionDoc is the per-user subscription document.
type SubscriptionDoc struct {
	Payload   json.RawMessage `json:"payload"`
	UpdatedAt int64           `json:"updatedAt"`
}

// EnsureSubsBucket makes sure the bucket exists (older databases).
func (d *DB) EnsureSubsBucket() error {
	return d.db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketSubs)
		return err
	})
}

// PutSubscriptions stores the raw subscription list for a user.
func (d *DB) PutSubscriptions(userID string, payload json.RawMessage) (int64, error) {
	now := time.Now().UnixMilli()
	err := d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSubs)
		if b == nil {
			return bolt.ErrBucketNotFound
		}
		return putJSON(b, userID, SubscriptionDoc{Payload: payload, UpdatedAt: now})
	})
	return now, err
}

// GetSubscriptions returns the stored subscription document.
func (d *DB) GetSubscriptions(userID string) (SubscriptionDoc, bool) {
	var doc SubscriptionDoc
	found := false
	_ = d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSubs)
		if b == nil {
			return nil
		}
		if err := getJSON(b, userID, &doc); err != nil {
			return nil
		}
		found = true
		return nil
	})
	return doc, found
}
