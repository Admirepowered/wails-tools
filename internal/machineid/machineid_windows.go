//go:build windows

package machineid

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// rawMachineID reads the Windows installation id, which survives reinstall of
// the application and stays identical for every user on the box.
func rawMachineID() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer key.Close()
	value, _, err := key.GetStringValue("MachineGuid")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}
