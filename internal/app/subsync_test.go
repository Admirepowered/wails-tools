package app

import (
	"testing"

	"wailstools/internal/subscription"
)

func TestParseSyncPayloadLegacyArray(t *testing.T) {
	raw := []byte(`[{"id":"sub-1","name":"机场","url":"https://x/sub"}]`)
	sources, nodes, err := parseSyncPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Name != "机场" {
		t.Fatalf("legacy sources not parsed: %+v", sources)
	}
	if len(nodes) != 0 {
		t.Fatalf("legacy payload has no nodes, got %d", len(nodes))
	}
}

func TestParseSyncPayloadDocument(t *testing.T) {
	raw := []byte(`{"version":2,"sources":[{"id":"sub-2","name":"新机场"}],"nodes":[{"id":"custom-1","name":"落地","server":"1.2.3.4","port":443,"protocol":"vless","uuid":"u"}]}`)
	sources, nodes, err := parseSyncPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].ID != "sub-2" {
		t.Fatalf("sources: %+v", sources)
	}
	if len(nodes) != 1 || nodes[0].ID != "custom-1" || nodes[0].Port != 443 {
		t.Fatalf("nodes: %+v", nodes)
	}
}

func TestMergeSubscriptionsNewestWins(t *testing.T) {
	local := []subscription.Source{
		{ID: "a", Name: "本地较新", UpdatedAt: "2026-11-15T10:00:00Z"},
		{ID: "only-local", Name: "本地独有", UpdatedAt: "2026-11-01T00:00:00Z"},
	}
	remote := []subscription.Source{
		{ID: "a", Name: "远端较旧", UpdatedAt: "2026-11-01T00:00:00Z"},
		{ID: "only-remote", Name: "远端独有", UpdatedAt: "2026-11-10T00:00:00Z"},
	}
	merged := mergeSubscriptions(local, remote)
	if len(merged) != 3 {
		t.Fatalf("expected union of 3, got %d", len(merged))
	}
	byID := map[string]subscription.Source{}
	for _, s := range merged {
		byID[s.ID] = s
	}
	if byID["a"].Name != "本地较新" {
		t.Fatalf("newest should win, got %q", byID["a"].Name)
	}
	if byID["only-remote"].Name != "远端独有" {
		t.Fatalf("remote-only source lost")
	}
}

func TestMergeSubscriptionsRemoteNewer(t *testing.T) {
	local := []subscription.Source{{ID: "a", Name: "旧", UpdatedAt: "2026-11-01T00:00:00Z"}}
	remote := []subscription.Source{{ID: "a", Name: "新", UpdatedAt: "2026-11-15T00:00:00Z"}}
	merged := mergeSubscriptions(local, remote)
	if len(merged) != 1 || merged[0].Name != "新" {
		t.Fatalf("remote newer must win: %+v", merged)
	}
}
