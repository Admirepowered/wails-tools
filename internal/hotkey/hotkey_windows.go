//go:build windows

package hotkey

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmHotkey   = 0x0312
	wmDestroy  = 0x0002
	wmClose    = 0x0010
	modAlt     = 0x0001
	modControl = 0x0002
	modShift   = 0x0004
	modWin     = 0x0008
)

var (
	user32h               = windows.NewLazySystemDLL("user32.dll")
	kernel32h             = windows.NewLazySystemDLL("kernel32.dll")
	procRegisterClassExH  = user32h.NewProc("RegisterClassExW")
	procCreateWindowExH   = user32h.NewProc("CreateWindowExW")
	procDefWindowProcH    = user32h.NewProc("DefWindowProcW")
	procDestroyWindowH    = user32h.NewProc("DestroyWindow")
	procGetMessageH       = user32h.NewProc("GetMessageW")
	procTranslateMsgH     = user32h.NewProc("TranslateMessage")
	procDispatchMsgH      = user32h.NewProc("DispatchMessageW")
	procPostQuitMessageH  = user32h.NewProc("PostQuitMessage")
	procRegisterHotKeyH   = user32h.NewProc("RegisterHotKey")
	procUnregisterHotKeyH = user32h.NewProc("UnregisterHotKey")
	procGetModuleHandleH  = kernel32h.NewProc("GetModuleHandleW")
)

type wndClassExH struct {
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

type msgH struct {
	HWnd     windows.Handle
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       struct{ X, Y int32 }
	LPrivate uint32
}

type handleState struct {
	hwnd    windows.Handle
	stop    chan struct{}
	trigger func()
}

var (
	classOnceH sync.Once
	classErrH  error
	statesMu   sync.Mutex
	states             = map[uintptr]*handleState{}
	nextID     uintptr = 0xA001
)

func supported() bool { return true }

func register(combo string, trigger func()) (any, error) {
	mods, vk, err := parseCombo(combo)
	if err != nil {
		return nil, err
	}
	hInstance, _, _ := procGetModuleHandleH.Call(0)
	className, _ := windows.UTF16PtrFromString("WailsToolsHotkeyWindow")

	classOnceH.Do(func() {
		wc := wndClassExH{
			CbSize:        uint32(unsafe.Sizeof(wndClassExH{})),
			LpfnWndProc:   syscall.NewCallback(hotkeyWndProc),
			HInstance:     windows.Handle(hInstance),
			LpszClassName: className,
		}
		if atom, _, err := procRegisterClassExH.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
			classErrH = fmt.Errorf("RegisterClassExW: %v", err)
		}
	})
	if classErrH != nil {
		return nil, classErrH
	}

	hwnd, _, err := procCreateWindowExH.Call(0, uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, 0, 0, hInstance, 0)
	if hwnd == 0 {
		return nil, fmt.Errorf("CreateWindowExW: %v", err)
	}

	statesMu.Lock()
	nextID++
	id := nextID
	st := &handleState{hwnd: windows.Handle(hwnd), stop: make(chan struct{}), trigger: trigger}
	states[id] = st
	statesMu.Unlock()

	if ok, _, rerr := procRegisterHotKeyH.Call(hwnd, id, uintptr(mods), uintptr(vk)); ok == 0 {
		procDestroyWindowH.Call(hwnd)
		statesMu.Lock()
		delete(states, id)
		statesMu.Unlock()
		return nil, fmt.Errorf("RegisterHotKey(%s): %v", combo, rerr)
	}

	go func() {
		var msg msgH
		for {
			ret, _, _ := procGetMessageH.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(ret) <= 0 {
				return
			}
			procTranslateMsgH.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMsgH.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}()
	runtime.KeepAlive(st)
	return id, nil
}

func unregister(handle any) {
	id, ok := handle.(uintptr)
	if !ok {
		return
	}
	statesMu.Lock()
	st := states[id]
	delete(states, id)
	statesMu.Unlock()
	if st == nil {
		return
	}
	procUnregisterHotKeyH.Call(uintptr(st.hwnd), id)
	procDestroyWindowH.Call(uintptr(st.hwnd))
}

func hotkeyWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch uint32(msg) {
	case wmHotkey:
		statesMu.Lock()
		st := states[wparam]
		statesMu.Unlock()
		if st != nil && st.trigger != nil {
			st.trigger()
		}
		return 0
	case wmClose:
		procDestroyWindowH.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessageH.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcH.Call(hwnd, msg, wparam, lparam)
	return ret
}

func parseCombo(combo string) (uint32, uint32, error) {
	parts := strings.Split(strings.ToUpper(strings.ReplaceAll(combo, " ", "")), "+")
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("快捷键格式无效: %s", combo)
	}
	keyName := parts[len(parts)-1]
	var mods uint32
	for _, m := range parts[:len(parts)-1] {
		switch m {
		case "CTRL", "CONTROL":
			mods |= modControl
		case "ALT":
			mods |= modAlt
		case "SHIFT":
			mods |= modShift
		case "WIN", "META", "SUPER":
			mods |= modWin
		default:
			return 0, 0, fmt.Errorf("不支持的修饰键: %s", m)
		}
	}
	vk, ok := virtualKey(keyName)
	if !ok {
		return 0, 0, fmt.Errorf("不支持的按键: %s", keyName)
	}
	return mods, vk, nil
}

func virtualKey(name string) (uint32, bool) {
	if len(name) == 1 {
		c := name[0]
		switch {
		case c >= 'A' && c <= 'Z':
			return uint32(c), true
		case c >= '0' && c <= '9':
			return uint32(c), true
		}
	}
	switch name {
	case "F1":
		return 0x70, true
	case "F2":
		return 0x71, true
	case "F3":
		return 0x72, true
	case "F4":
		return 0x73, true
	case "F5":
		return 0x74, true
	case "F6":
		return 0x75, true
	case "F7":
		return 0x76, true
	case "F8":
		return 0x77, true
	case "F9":
		return 0x78, true
	case "F10":
		return 0x79, true
	case "F11":
		return 0x7A, true
	case "F12":
		return 0x7B, true
	case "PRINTSCREEN", "PRTSC":
		return 0x2C, true
	case "SPACE":
		return 0x20, true
	}
	return 0, false
}
