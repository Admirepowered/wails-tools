//go:build windows

package singleinst

import (
	"syscall"
	"time"
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
	// 更新重启时旧进程可能还在退出，最多等 10 秒。
	for attempt := 0; ; attempt++ {
		h, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(ptr)))
		if h == 0 {
			return func() {}, err
		}
		errno, ok := err.(syscall.Errno)
		if !ok || errno != 183 {
			return func() { procCloseHandle.Call(h) }, nil
		}
		procCloseHandle.Call(h)
		if attempt >= 20 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if title, terr := syscall.UTF16PtrFromString("WailsTools"); terr == nil {
		if hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title))); hwnd != 0 {
			procShowWindow.Call(hwnd, 9) // SW_RESTORE
			procSetForeground.Call(hwnd)
		}
	}
	return nil, ErrAlreadyRunning
}
