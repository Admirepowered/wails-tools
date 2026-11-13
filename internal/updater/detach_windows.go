//go:build windows

package updater

import "syscall"

// detachedAttr keeps the restarted app independent from the updater process.
func detachedAttr() *syscall.SysProcAttr {
	const detachedProcess = 0x00000008
	const createNewProcessGroup = 0x00000200
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup}
}
