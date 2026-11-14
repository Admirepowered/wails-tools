package store

import (
	"encoding/json"
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketDevices = []byte("devices")
var bucketMeta = []byte("meta")

// Device is one registered client device.
type Device struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	LastSeen string `json:"lastSeen"`
}

// EnsureAdminBuckets creates the buckets used by admin features.
func (d *DB) EnsureAdminBuckets() error {
	return d.db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketDevices); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(bucketMeta)
		return err
	})
}

// AllowRegister reports whether public registration is enabled (default true).
func (d *DB) AllowRegister() bool {
	v := true
	_ = d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return nil
		}
		raw := b.Get([]byte("allowRegister"))
		if raw == nil {
			return nil
		}
		return json.Unmarshal(raw, &v)
	})
	return v
}

// SetAllowRegister toggles public registration.
func (d *DB) SetAllowRegister(v bool) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return errors.New("meta bucket missing")
		}
		raw, _ := json.Marshal(v)
		return b.Put([]byte("allowRegister"), raw)
	})
}

// ListDevices returns the devices registered for a user.
func (d *DB) ListDevices(userID string) []Device {
	out := []Device{}
	if userID == "" {
		return out
	}
	_ = d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketDevices)
		if b == nil {
			return nil
		}
		raw := b.Get([]byte(userID))
		if raw == nil {
			return nil
		}
		return json.Unmarshal(raw, &out)
	})
	return out
}

func (d *DB) putDevices(tx *bolt.Tx, userID string, devices []Device) error {
	raw, err := json.Marshal(devices)
	if err != nil {
		return err
	}
	return tx.Bucket(bucketDevices).Put([]byte(userID), raw)
}

// AddDevice registers a new device for a user.
func (d *DB) AddDevice(userID, id, name string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		devices := d.ListDevices(userID)
		devices = append(devices, Device{ID: id, Name: name, LastSeen: time.Now().UTC().Format(time.RFC3339)})
		return d.putDevices(tx, userID, devices)
	})
}

// TouchDevice updates a device's last-seen timestamp.
func (d *DB) TouchDevice(userID, id, name string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		devices := d.ListDevices(userID)
		for i := range devices {
			if devices[i].ID == id {
				devices[i].LastSeen = time.Now().UTC().Format(time.RFC3339)
				if name != "" {
					devices[i].Name = name
				}
				return d.putDevices(tx, userID, devices)
			}
		}
		return nil
	})
}

// DeleteDevice removes a device registration.
func (d *DB) DeleteDevice(userID, id string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		devices := d.ListDevices(userID)
		out := devices[:0]
		found := false
		for _, dev := range devices {
			if dev.ID == id {
				found = true
				continue
			}
			out = append(out, dev)
		}
		if !found {
			return errors.New("device not found")
		}
		return d.putDevices(tx, userID, out)
	})
}
