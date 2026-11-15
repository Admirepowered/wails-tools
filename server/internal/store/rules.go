package store

import (
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	keySNIRules   = []byte("sniRules")
	keySNIRulesAt = []byte("sniRulesUpdatedAt")
	keySNIRulesBy = []byte("sniRulesUpdatedBy")
)

// GetSNIRules returns the server-published default SNI rule list.
func (d *DB) GetSNIRules() (string, string, string, bool) {
	var rules, at, by string
	found := false
	_ = d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return nil
		}
		if raw := b.Get(keySNIRules); raw != nil {
			rules = string(raw)
			found = true
		}
		if raw := b.Get(keySNIRulesAt); raw != nil {
			at = string(raw)
		}
		if raw := b.Get(keySNIRulesBy); raw != nil {
			by = string(raw)
		}
		return nil
	})
	return rules, at, by, found
}

// PutSNIRules stores the server-published default SNI rule list.
func (d *DB) PutSNIRules(rules, by string) (string, error) {
	if len(rules) > 1<<20 {
		return "", errors.New("sni rules too large")
	}
	at := time.Now().UTC().Format(time.RFC3339)
	err := d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return errors.New("meta bucket missing")
		}
		if err := b.Put(keySNIRules, []byte(rules)); err != nil {
			return err
		}
		if err := b.Put(keySNIRulesAt, []byte(at)); err != nil {
			return err
		}
		return b.Put(keySNIRulesBy, []byte(by))
	})
	return at, err
}
