// Package hotkey registers a global keyboard shortcut.
package hotkey

import (
	"errors"
	"strings"
)

// Options configures the shortcut.
type Options struct {
	// Combo is a human readable shortcut, e.g. "Ctrl+Alt+S".
	Combo string
	// OnTrigger runs when the shortcut fires.
	OnTrigger func()
}

// Hotkey is a registered global shortcut.
type Hotkey struct {
	combo   string
	trigger func()
	handle  any
}

// Register installs the shortcut.
func Register(opts Options) (*Hotkey, error) {
	if strings.TrimSpace(opts.Combo) == "" {
		return nil, errors.New("快捷键为空")
	}
	if opts.OnTrigger == nil {
		opts.OnTrigger = func() {}
	}
	h := &Hotkey{combo: opts.Combo, trigger: opts.OnTrigger}
	handle, err := register(h.combo, func() { go h.trigger() })
	if err != nil {
		return nil, err
	}
	h.handle = handle
	return h, nil
}

// Combo returns the registered combination.
func (h *Hotkey) Combo() string {
	if h == nil {
		return ""
	}
	return h.combo
}

// Unregister removes the shortcut.
func (h *Hotkey) Unregister() {
	if h == nil || h.handle == nil {
		return
	}
	unregister(h.handle)
	h.handle = nil
}

// Supported reports whether the platform implements global hotkeys.
func Supported() bool { return supported() }

// Parse validates a combo and returns its normalised form.
func Parse(combo string) (string, error) {
	parts := strings.Split(strings.ToUpper(strings.ReplaceAll(combo, " ", "")), "+")
	if len(parts) < 2 {
		return "", errors.New("快捷键至少需要修饰键 + 按键，例如 Ctrl+Alt+S")
	}
	key := parts[len(parts)-1]
	if key == "" {
		return "", errors.New("缺少按键")
	}
	mods := parts[:len(parts)-1]
	for _, m := range mods {
		switch m {
		case "CTRL", "CONTROL", "ALT", "SHIFT", "WIN", "META", "SUPER":
		default:
			return "", errors.New("不支持的修饰键: " + m)
		}
	}
	return strings.Join(parts, "+"), nil
}
