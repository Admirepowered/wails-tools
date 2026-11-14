//go:build !windows

package machineid

import (
	"os"
	"strings"
)

// rawMachineID reads the platform machine id when available.
func rawMachineID() string {
	for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		data, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(data)) != "" {
			return strings.TrimSpace(string(data))
		}
	}
	return ""
}
