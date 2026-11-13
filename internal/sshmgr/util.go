package sshmgr

import (
	"crypto/rand"
	"encoding/hex"
)

// newID returns a short random identifier with a readable prefix.
func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "-" + hex.EncodeToString([]byte{0})
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
