package p2p

import "testing"

func TestSessionLifecycle(t *testing.T) {
	h := NewHub(nil)
	info, err := h.CreateSession("user-1", "dev-a", "dev-b")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if info.ID == "" || info.Secret == "" {
		t.Fatalf("session missing id/secret: %+v", info)
	}

	// Only the target device sees it as pending.
	if got := h.Pending("user-1", "dev-b"); len(got) != 1 || got[0].ID != info.ID {
		t.Fatalf("pending for target: %+v", got)
	}
	if got := h.Pending("user-1", "dev-a"); len(got) != 0 {
		t.Fatalf("initiator must not see its own offer: %+v", got)
	}
	if got := h.Pending("user-2", "dev-b"); len(got) != 0 {
		t.Fatalf("another user must not see the session: %+v", got)
	}

	// Lookup validates ownership and device membership.
	if _, ok := h.Lookup("user-1", info.ID, "dev-a"); !ok {
		t.Fatal("initiator should resolve its session")
	}
	if _, ok := h.Lookup("user-1", info.ID, "dev-c"); ok {
		t.Fatal("foreign device must not resolve the session")
	}
	if _, ok := h.Lookup("user-2", info.ID, "dev-b"); ok {
		t.Fatal("other user must not resolve the session")
	}

	// A new offer for the same pair replaces the old one.
	second, err := h.CreateSession("user-1", "dev-a", "dev-b")
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if _, ok := h.Lookup("user-1", info.ID, "dev-a"); ok {
		t.Fatal("old session should have been replaced")
	}
	if got := h.Pending("user-1", "dev-b"); len(got) != 1 || got[0].ID != second.ID {
		t.Fatalf("pending after replace: %+v", got)
	}

	h.Drop("user-1", second.ID)
	if got := h.Pending("user-1", "dev-b"); len(got) != 0 {
		t.Fatalf("dropped session still pending: %+v", got)
	}

	if _, err := h.CreateSession("user-1", "dev-a", "dev-a"); err == nil {
		t.Fatal("self punch must be rejected")
	}
	if _, err := h.CreateSession("user-1", "", "dev-b"); err == nil {
		t.Fatal("empty device must be rejected")
	}
}

func TestHandleAnnounceExchange(t *testing.T) {
	h := NewHub(nil)
	info, err := h.CreateSession("user-1", "dev-a", "dev-b")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	addrA := mustAddr(t, "203.0.113.5:1000")
	addrB := mustAddr(t, "198.51.100.7:2000")

	if rep := h.handle(Announce{Session: info.ID, Role: "a"}, addrA); !rep.OK || rep.You != addrA.String() {
		t.Fatalf("first announce: %+v", rep)
	}
	repA := h.handle(Announce{Session: info.ID, Role: "a"}, addrA)
	if repA.Peer != "" {
		t.Fatalf("peer should be unknown before B announces: %+v", repA)
	}
	repB := h.handle(Announce{Session: info.ID, Role: "b"}, addrB)
	if !repB.OK || repB.Peer != addrA.String() {
		t.Fatalf("B should learn A's endpoint: %+v", repB)
	}
	repA = h.handle(Announce{Session: info.ID, Role: "a"}, addrA)
	if repA.Peer != addrB.String() {
		t.Fatalf("A should learn B's endpoint: %+v", repA)
	}

	if rep := h.handle(Announce{Session: "nope", Role: "a"}, addrA); rep.OK || rep.Error == "" {
		t.Fatalf("unknown session must fail: %+v", rep)
	}
	if rep := h.handle(Announce{Session: info.ID, Role: "x"}, addrA); rep.OK {
		t.Fatalf("bad role must fail: %+v", rep)
	}
}
