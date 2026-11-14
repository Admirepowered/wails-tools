//go:build windows

package singleinst

import (
	"syscall"
	"unsafe"
)

var (
	kernel32d         = syscall.NewLazyDLL("kernel32.dll")
	user32d           = syscall.NewLazyDLL("user32.dll")
	procCreateMutexW  = kernel32d.NewProc("CreateMutexW")
	procCloseHandle   = kernel32d.NewProc("CloseHandle")
	procFindWindowW   = user32d.NewProc("FindWindowW")
	procShowWindow    = user32d.NewProc("ShowWindow")
	procSetForeground = user32d.NewProc("SetForegroundWindow")
)

func acquire(name string) (func(), error) {
	ptr, _ := syscall.UTF16PtrFromString(`Local\` + name)
	h, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(ptr)))
	if h == 0 {
		return func() {}, err
	}
	if errno, ok := err.(syscall.Errno); ok && errno == 183 { // ERROR_ALREADY_EXISTS
		if title, terr := syscall.UTF16PtrFromString("WailsTools"); terr == nil {
			if hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title))); hwnd != 0 {
				procShowWindow.Call(hwnd, 9) // SW_RESTORE
				procSetForeground.Call(hwnd)
			}
		}
		procCloseHandle.Call(h)
		return nil, ErrAlreadyRunning
	}
	return func() { procCloseHandle.Call(h) }, nil
}
