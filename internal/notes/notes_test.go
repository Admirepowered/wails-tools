package notes

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"wailstools/internal/serverlink"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func TestSaveListAndDelete(t *testing.T) {
	s := openStore(t)
	n, err := s.Save(Note{Title: "第一篇", Content: "hello", Folder: "工作", Tags: []string{"a"}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if n.ID == "" || n.UpdatedAt == 0 || !n.Dirty {
		t.Fatalf("unexpected note after save: %+v", n)
	}
	list := s.List(false)
	if len(list) != 1 || list[0].Title != "第一篇" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if len(s.Dirty()) != 1 {
		t.Fatal("expected one dirty note")
	}

	// Update keeps the id and bumps updatedAt.
	time.Sleep(2 * time.Millisecond)
	updated, err := s.Save(Note{ID: n.ID, Title: "改标题", Content: "world", CreatedAt: n.CreatedAt})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ID != n.ID || updated.UpdatedAt <= n.UpdatedAt {
		t.Fatalf("update did not bump timestamp: %+v", updated)
	}

	// Delete tombstones and hides from the default listing.
	if err := s.Delete(n.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(s.List(false)) != 0 {
		t.Fatal("deleted note still listed")
	}
	if len(s.List(true)) != 1 {
		t.Fatal("tombstone missing from the full listing")
	}
	if got, _ := s.Get(n.ID); got.Content != "" {
		t.Fatal("tombstone must clear content")
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	n, err := s.Save(Note{Title: "持久化", Content: "内容"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.Get(n.ID)
	if !ok || got.Title != "持久化" || got.Content != "内容" {
		t.Fatalf("unexpected reloaded note: %+v", got)
	}
}

func TestMergeLastWriteWins(t *testing.T) {
	s := openStore(t)
	local, err := s.Save(Note{Title: "本地", Content: "v1"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	// Mark it synced so it is no longer dirty.
	if err := s.MarkSynced([]string{local.ID}, 1000); err != nil {
		t.Fatalf("mark synced: %v", err)
	}
	if len(s.Dirty()) != 0 {
		t.Fatal("expected no dirty notes after sync")
	}

	// A newer remote version must win.
	newer := local
	newer.Title = "远端更新"
	newer.Content = "v2"
	newer.UpdatedAt = local.UpdatedAt + 5000
	changed, err := s.Merge([]Note{newer})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if changed != 1 {
		t.Fatalf("expected 1 merged note, got %d", changed)
	}
	got, _ := s.Get(local.ID)
	if got.Title != "远端更新" || got.Content != "v2" {
		t.Fatalf("remote note did not win: %+v", got)
	}

	// An older remote version must not clobber a newer local edit.
	time.Sleep(2 * time.Millisecond)
	edited, err := s.Save(Note{ID: local.ID, Title: "本地再编辑", Content: "v3", CreatedAt: got.CreatedAt})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	stale := got
	stale.Title = "过期远端"
	stale.UpdatedAt = edited.UpdatedAt - 1000
	if _, err := s.Merge([]Note{stale}); err != nil {
		t.Fatalf("merge stale: %v", err)
	}
	after, _ := s.Get(local.ID)
	if after.Title != "本地再编辑" {
		t.Fatalf("stale remote overwrote local: %+v", after)
	}

	// Remote deletions propagate as tombstones.
	tomb := after
	tomb.Deleted = true
	tomb.UpdatedAt = after.UpdatedAt + 10_000
	if _, err := s.Merge([]Note{tomb}); err != nil {
		t.Fatalf("merge tombstone: %v", err)
	}
	if len(s.List(false)) != 0 {
		t.Fatal("remote tombstone did not remove the note")
	}
}

func TestMergeInsertsNewRemoteNotes(t *testing.T) {
	s := openStore(t)
	changed, err := s.Merge([]Note{
		{ID: "remote-1", Title: "远端笔记", Content: "x", UpdatedAt: 1234, CreatedAt: 1200},
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if changed != 1 {
		t.Fatalf("expected one insert, got %d", changed)
	}
	got, ok := s.Get("remote-1")
	if !ok || got.Title != "远端笔记" || got.Dirty {
		t.Fatalf("unexpected inserted note: %+v", got)
	}
}

func TestMarkSyncedAndLastSync(t *testing.T) {
	s := openStore(t)
	n, _ := s.Save(Note{Title: "同步", Content: "x"})
	if err := s.MarkSynced([]string{n.ID}, 7777); err != nil {
		t.Fatalf("mark synced: %v", err)
	}
	if s.LastSync() != 7777 {
		t.Fatalf("last sync %d, want 7777", s.LastSync())
	}
	if len(s.Dirty()) != 0 {
		t.Fatal("note still dirty after MarkSynced")
	}
}

func TestPurgeTombstones(t *testing.T) {
	s := openStore(t)
	n, _ := s.Save(Note{Title: "删除", Content: "x"})
	if err := s.Delete(n.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.PurgeTombstones(-1 * time.Hour); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if len(s.List(true)) != 0 {
		t.Fatal("tombstone was not purged")
	}
}

func TestConversionHelpers(t *testing.T) {
	n := Note{ID: "n1", Title: "T", Content: "C", Tags: []string{"x"}, UpdatedAt: 5}
	remote := ToRemote(n)
	if remote.ID != "n1" || remote.UpdatedAt != 5 || len(remote.Tags) != 1 {
		t.Fatalf("unexpected remote conversion: %+v", remote)
	}
	back := FromRemote(serverlink.Note{ID: "n1", Title: "T", Content: "C", UpdatedAt: 5})
	if back.Title != "T" || back.Deleted {
		t.Fatalf("unexpected local conversion: %+v", back)
	}
	md := n.ExportMarkdown()
	if md == "" || md[:1] != "#" {
		t.Fatalf("unexpected markdown export: %q", md)
	}
	if n.Checksum() == (Note{ID: "n1", Title: "T", Content: "other"}).Checksum() {
		t.Fatal("checksum must depend on content")
	}
}

func TestOpenCreatesDirectoryAndHandlesEmptyFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "notes")
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open nested: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("directory not created: %v", err)
	}
	if len(s.List(false)) != 0 {
		t.Fatal("expected empty store")
	}
}
