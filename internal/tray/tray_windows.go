//go:build windows

package tray

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmNull          = 0x0000
	wmContextMenu   = 0x007B
	wmApp           = 0x8000
	wmTrayIcon      = wmApp + 1
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nimAdd     = 0x00000000
	nimModify  = 0x00000001
	nimDelete  = 0x00000002

	imageIcon      = 1
	lrLoadFromFile = 0x00000010

	mfString    = 0x00000000
	mfGrayed    = 0x00000001
	mfChecked   = 0x00000008
	mfSeparator = 0x00000800

	tpmLeftAlign   = 0x0000
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procLoadImageW          = user32.NewProc("LoadImageW")
	procDestroyIcon         = user32.NewProc("DestroyIcon")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
)

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

type point struct {
	X int32
	Y int32
}

type msgStruct struct {
	HWnd     windows.Handle
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             windows.Handle
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     windows.Handle
}

var (
	activeMu  sync.Mutex
	active    *trayImpl
	classOnce sync.Once
	classErr  error
)

type trayImpl struct {
	opts   Options
	hwnd   windows.Handle
	hIcon  windows.Handle
	mu     sync.Mutex
	tip    string
	ready  chan error
	done   chan struct{}
	closed bool
}

func supported() bool { return true }

// newImpl creates the hidden message window and installs the tray icon.
func newImpl(opts Options) (impl, error) {
	t := &trayImpl{
		opts:  opts,
		tip:   opts.Tooltip,
		ready: make(chan error, 1),
		done:  make(chan struct{}),
	}
	go t.run()
	if err := <-t.ready; err != nil {
		return nil, err
	}
	return t, nil
}

func (t *trayImpl) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className, _ := windows.UTF16PtrFromString("WailsToolsTrayWindow")

	classOnce.Do(func() {
		wc := wndClassEx{
			CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
			LpfnWndProc:   syscall.NewCallback(trayWndProc),
			HInstance:     windows.Handle(hInstance),
			LpszClassName: className,
		}
		atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		if atom == 0 {
			classErr = fmt.Errorf("RegisterClassExW failed: %v", err)
			return
		}
	})
	if classErr != nil {
		t.ready <- classErr
		return
	}

	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		0,
		0,
		0, 0, 0, 0,
		0, 0,
		hInstance,
		0,
	)
	if hwnd == 0 {
		t.ready <- fmt.Errorf("CreateWindowExW failed: %v", err)
		return
	}
	t.hwnd = windows.Handle(hwnd)

	activeMu.Lock()
	active = t
	activeMu.Unlock()

	if err := t.addIcon(); err != nil {
		procDestroyWindow.Call(hwnd)
		t.ready <- err
		return
	}
	t.ready <- nil

	// Pump messages until the window is destroyed.
	var msg msgStruct
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 { // 0 = WM_QUIT, -1 = error
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	if t.hIcon != 0 {
		procDestroyIcon.Call(uintptr(t.hIcon))
		t.hIcon = 0
	}
	close(t.done)
}

func (t *trayImpl) iconFile() (string, error) {
	if t.opts.IconPath != "" {
		if _, err := os.Stat(t.opts.IconPath); err == nil {
			return t.opts.IconPath, nil
		}
	}
	dir := filepath.Join(os.TempDir(), "wails-tools")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "tray.ico")
	if err := os.WriteFile(path, BuildICO(32), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (t *trayImpl) addIcon() error {
	iconPath, err := t.iconFile()
	if err != nil {
		return wrapErr("write icon", err)
	}
	iconPtr, _ := windows.UTF16PtrFromString(iconPath)
	hIcon, _, loadErr := procLoadImageW.Call(0, uintptr(unsafe.Pointer(iconPtr)), imageIcon, 0, 0, lrLoadFromFile)
	if hIcon == 0 {
		return wrapErr("LoadImageW", loadErr)
	}
	t.hIcon = windows.Handle(hIcon)

	nid := notifyIconData{
		CbSize:           uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:             t.hwnd,
		UID:              1,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: wmTrayIcon,
		HIcon:            t.hIcon,
	}
	copy(nid.SzTip[:], windows.StringToUTF16(t.tip))
	ok, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	if ok == 0 {
		return wrapErr("Shell_NotifyIcon(NIM_ADD)", err)
	}
	return nil
}

func (t *trayImpl) updateTooltip(text string) error {
	t.mu.Lock()
	t.tip = text
	t.mu.Unlock()
	nid := notifyIconData{
		CbSize: uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:   t.hwnd,
		UID:    1,
		UFlags: nifTip,
	}
	copy(nid.SzTip[:], windows.StringToUTF16(text))
	ok, _, err := procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
	if ok == 0 {
		return wrapErr("Shell_NotifyIcon(NIM_MODIFY)", err)
	}
	return nil
}

func (t *trayImpl) remove() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()

	nid := notifyIconData{
		CbSize: uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:   t.hwnd,
		UID:    1,
	}
	_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	if t.hwnd != 0 {
		procPostMessageW.Call(uintptr(t.hwnd), wmClose, 0, 0)
	}
	select {
	case <-t.done:
	case <-time.After(3 * time.Second):
		// The loop is wedged; don't block shutdown.
	}
	activeMu.Lock()
	if active == t {
		active = nil
	}
	activeMu.Unlock()
	return nil
}

func trayWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch uint32(msg) {
	case wmTrayIcon:
		t := currentTray()
		if t == nil {
			return 0
		}
		switch uint32(lparam) & 0xFFFF {
		case wmRButtonUp, wmContextMenu:
			t.showMenu()
		case wmLButtonDblClk:
			if t.opts.OnDoubleClick != nil {
				cb := t.opts.OnDoubleClick
				go cb()
			}
		}
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return ret
}

func currentTray() *trayImpl {
	activeMu.Lock()
	defer activeMu.Unlock()
	return active
}

func (t *trayImpl) showMenu() {
	items := t.opts.Menu()
	if len(items) == 0 {
		return
	}
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	for _, item := range items {
		if item.Separator {
			procAppendMenuW.Call(hMenu, mfSeparator, 0, 0)
			continue
		}
		flags := uintptr(mfString)
		if item.Checked {
			flags |= mfChecked
		}
		if item.Disabled {
			flags |= mfGrayed
		}
		label, _ := windows.UTF16PtrFromString(item.Label)
		procAppendMenuW.Call(hMenu, flags, uintptr(item.ID), uintptr(unsafe.Pointer(label)))
	}

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// Required so the popup dismisses correctly when clicking elsewhere.
	procSetForegroundWindow.Call(uintptr(t.hwnd))
	chosen, _, _ := procTrackPopupMenu.Call(
		hMenu,
		tpmRightButton|tpmReturnCmd|tpmLeftAlign,
		uintptr(pt.X), uintptr(pt.Y),
		0, uintptr(t.hwnd), 0,
	)
	procPostMessageW.Call(uintptr(t.hwnd), wmNull, 0, 0)

	if int32(chosen) > 0 && t.opts.OnMenu != nil {
		id := int(int32(chosen))
		cb := t.opts.OnMenu
		go cb(id)
	}
}

var errTrayClosed = errors.New("tray: closed")
