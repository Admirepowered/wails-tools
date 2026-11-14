package p2p

import (
	"net"
	"testing"
)

func mustAddr(t *testing.T, s string) *net.UDPAddr {
	t.Helper()
	a, err := net.ResolveUDPAddr("udp4", s)
	if err != nil {
		t.Fatalf("resolve %s: %v", s, err)
	}
	return a
}
