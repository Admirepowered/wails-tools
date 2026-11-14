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
	Machine  string `json:"machine,omitempty"`
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

// readDevices decodes a user's device list inside an existing transaction.
// Never call ListDevices from inside db.Update: bbolt holds the meta lock for
// write transactions, so a nested View would deadlock.
func readDevices(tx *bolt.Tx, userID string) []Device {
	out := []Device{}
	b := tx.Bucket(bucketDevices)
	if b == nil || userID == "" {
		return out
	}
	raw := b.Get([]byte(userID))
	if raw == nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func putDevices(tx *bolt.Tx, userID string, devices []Device) error {
	raw, err := json.Marshal(devices)
	if err != nil {
		return err
	}
	return tx.Bucket(bucketDevices).Put([]byte(userID), raw)
}

// ListDevices returns the devices registered for a user.
func (d *DB) ListDevices(userID string) []Device {
	out := []Device{}
	if userID == "" {
		return out
	}
	_ = d.db.View(func(tx *bolt.Tx) error {
		out = readDevices(tx, userID)
		return nil
	})
	return out
}

// AddDevice registers a new device for a user.
func (d *DB) AddDevice(userID, id, name, machine string) error {
	if userID == "" || id == "" {
		return errors.New("invalid device")
	}
	return d.db.Update(func(tx *bolt.Tx) error {
		devices := readDevices(tx, userID)
		devices = append(devices, Device{ID: id, Name: name, Machine: machine, LastSeen: time.Now().UTC().Format(time.RFC3339)})
		return putDevices(tx, userID, devices)
	})
}

// TouchDevice updates a device's last-seen timestamp.
func (d *DB) TouchDevice(userID, id, name, machine string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		devices := readDevices(tx, userID)
		for i := range devices {
			if devices[i].ID == id {
				devices[i].LastSeen = time.Now().UTC().Format(time.RFC3339)
				if name != "" {
					devices[i].Name = name
				}
				if machine != "" {
					devices[i].Machine = machine
				}
				return putDevices(tx, userID, devices)
			}
		}
		return nil
	})
}

// DeleteDevicesByMachine removes every registration for a machine, returning the
// ids that were dropped (used to collapse duplicate rows created before the
// machine fingerprint existed).
func (d *DB) DeleteDevicesByMachine(userID, machine string) ([]string, error) {
	if machine == "" {
		return nil, nil
	}
	var removed []string
	err := d.db.Update(func(tx *bolt.Tx) error {
		devices := readDevices(tx, userID)
		out := make([]Device, 0, len(devices))
		for _, dev := range devices {
			if dev.Machine == machine {
				removed = append(removed, dev.ID)
				continue
			}
			out = append(out, dev)
		}
		if len(removed) == 0 {
			return nil
		}
		return putDevices(tx, userID, out)
	})
	return removed, err
}

// DeleteDevice removes a device registration.
func (d *DB) DeleteDevice(userID, id string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		devices := readDevices(tx, userID)
		out := make([]Device, 0, len(devices))
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
		return putDevices(tx, userID, out)
	})
}

// DeleteUserDevices drops every device record for a deleted user.
func (d *DB) DeleteUserDevices(userID string) {
	_ = d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketDevices)
		if b == nil {
			return nil
		}
		return b.Delete([]byte(userID))
	})
}
