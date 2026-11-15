package store

import (
	"path/filepath"
	"testing"
)

func TestDeviceNumberAllocation(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.EnsureAdminBuckets(); err != nil {
		t.Fatalf("buckets: %v", err)
	}
	u, err := db.CreateUser(User{ID: "u1", Username: "x", Role: "user"})
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := db.AddDevice(u.ID, "d1", "pc", "m1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	devs := db.ListDevices(u.ID)
	if len(devs) != 1 || len(devs[0].Number) != 9 {
		t.Fatalf("device number not allocated: %+v", devs)
	}
	num := devs[0].Number
	if _, got, ok := db.FindDeviceByNumber(num); !ok || got.ID != "d1" {
		t.Fatalf("find by number failed: %v %v", got, ok)
	}
	// Touch keeps the same number.
	if err := db.TouchDevice(u.ID, "d1", "pc", "m1"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	if devs := db.ListDevices(u.ID); devs[0].Number != num {
		t.Fatalf("number changed on touch: %s -> %s", num, devs[0].Number)
	}
}
