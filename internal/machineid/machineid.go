// Package machineid derives a stable identifier for the physical machine so the
// sync server can treat every installation on the same box as one device.
package machineid

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"strings"
)

// ID returns a stable 32-char hex identifier, or "" when nothing can be read.
func ID() string {
	if raw := rawMachineID(); strings.TrimSpace(raw) != "" {
		return digest(raw)
	}
	return digest(hostAndMAC())
}

func digest(raw string) string {
	sum := sha256.Sum256([]byte("wails-tools-machine-v1:" + strings.ToLower(strings.TrimSpace(raw))))
	return hex.EncodeToString(sum[:16])
}

func hostAndMAC() string {
	host, _ := os.Hostname()
	parts := []string{host}
	for _, iface := range interfaces() {
		if len(iface.HardwareAddr) == 0 {
			continue
		}
		parts = append(parts, iface.HardwareAddr.String())
		break
	}
	return strings.Join(parts, "|")
}

func interfaces() []net.Interface {
	all, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]net.Interface, 0, len(all))
	for _, iface := range all {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		out = append(out, iface)
	}
	return out
}
