package notes

import "testing"

func TestMergeKeepsNewerLocalAndQueuesPush(t *testing.T) {
	s := openStore(t)
	local, err := s.Save(Note{Title: "本地较新", Content: "a"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.MarkSynced([]string{local.ID}, 1000); err != nil {
		t.Fatalf("mark synced: %v", err)
	}
	stale := local
	stale.Title = "远端较旧"
	stale.UpdatedAt = local.UpdatedAt - 60_000
	if _, err := s.Merge([]Note{stale}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	got, _ := s.Get(local.ID)
	if got.Title != "本地较新" {
		t.Fatalf("older remote overwrote newer local: %+v", got)
	}
	if !got.Dirty {
		t.Fatal("newer local note must be queued for the next push")
	}
}
