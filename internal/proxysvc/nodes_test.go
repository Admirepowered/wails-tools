package proxysvc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltinDirectNodeIsPermanent(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenNodes(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	nodes := store.List()
	if len(nodes) == 0 || nodes[0].ID != BuiltinDirectID {
		t.Fatalf("builtin node missing: %+v", nodes)
	}
	if nodes[0].Protocol != "direct" {
		t.Fatalf("builtin protocol = %q", nodes[0].Protocol)
	}

	if err := store.Delete(BuiltinDirectID); err == nil {
		t.Fatal("builtin node must not be deletable")
	}
	if _, err := store.Save(BuiltinDirectNode()); err == nil {
		t.Fatal("builtin node must not be editable")
	}

	// Re-opening the store keeps exactly one builtin node.
	store2, err := OpenNodes(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	count := 0
	for _, n := range store2.List() {
		if n.ID == BuiltinDirectID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one builtin node, got %d", count)
	}

	// A corrupted node file is recreated with the builtin node present.
	path := filepath.Join(dir, "custom-nodes.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenNodesFresh(dir)
	if err != nil {
		t.Fatalf("fresh: %v", err)
	}
	if len(fresh.List()) != 1 || fresh.List()[0].ID != BuiltinDirectID {
		t.Fatalf("fresh store should start with the builtin node: %+v", fresh.List())
	}
}
