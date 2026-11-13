//go:build windows

package tray

import (
	"testing"
	"time"
)

// TestTrayLifecycle installs a real tray icon, updates its tooltip and removes
// it again. It proves the Win32 plumbing (window class, Shell_NotifyIcon,
// message loop) works without needing a human to look at the taskbar.
func TestTrayLifecycle(t *testing.T) {
	if !Supported() {
		t.Skip("tray not supported")
	}
	menuCalls := make(chan int, 4)
	tr, err := New(Options{
		Tooltip: "WailsTools test",
		Menu: func() []MenuItem {
			return []MenuItem{
				{ID: 1, Label: "显示主窗口"},
				{Separator: true},
				{ID: 2, Label: "勾选项", Checked: true},
				{ID: 3, Label: "禁用项", Disabled: true},
			}
		},
		OnMenu: func(id int) { menuCalls <- id },
	})
	if err != nil {
		t.Fatalf("create tray: %v", err)
	}
	// Give the message loop a moment to settle.
	time.Sleep(300 * time.Millisecond)
	if err := tr.UpdateTooltip("WailsTools test · 已连接"); err != nil {
		t.Fatalf("update tooltip: %v", err)
	}
	if err := tr.UpdateTooltip(""); err != nil {
		t.Fatalf("update empty tooltip: %v", err)
	}
	if err := tr.Remove(); err != nil {
		t.Fatalf("remove tray: %v", err)
	}
	// Removing twice must be harmless.
	if err := tr.Remove(); err != nil {
		t.Fatalf("second remove: %v", err)
	}
	select {
	case id := <-menuCalls:
		t.Fatalf("unexpected menu callback %d", id)
	default:
	}
}

func TestTrayTooltipAfterRemove(t *testing.T) {
	tr, err := New(Options{Tooltip: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := tr.Remove(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := tr.UpdateTooltip("after"); err == nil {
		t.Fatal("expected an error when updating a removed tray")
	}
}
