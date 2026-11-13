// Package tray provides a system tray icon with a context menu.
//
// The Windows implementation is a self-contained Win32 implementation
// (Shell_NotifyIcon + a hidden message window), so no cgo or third-party
// dependency is required.
package tray

import (
	"errors"
	"fmt"
)

// ErrUnsupported marks platforms without an implementation.
var ErrUnsupported = errors.New("system tray is not supported on this platform")

// MenuItem is one entry of the tray context menu.
type MenuItem struct {
	// ID is passed back to OnMenu when the item is clicked (must be > 0).
	ID int
	// Label is the visible text.
	Label string
	// Checked renders a check mark.
	Checked bool
	// Disabled greys the item out.
	Disabled bool
	// Separator renders a divider and ignores the other fields.
	Separator bool
}

// Options configures the tray icon.
type Options struct {
	// Tooltip is the hover text (max 127 characters).
	Tooltip string
	// IconPath is a .ico file loaded from disk.
	IconPath string
	// OnMenu receives the ID of the clicked item.
	OnMenu func(id int)
	// OnDoubleClick is invoked on a double click of the icon.
	OnDoubleClick func()
	// OnQuit is invoked when the shell requests the tray icon to go away.
	OnQuit func()
	// Menu builds the menu on demand so it always reflects current state.
	Menu func() []MenuItem
}

// impl is the platform implementation.
type impl interface {
	updateTooltip(text string) error
	remove() error
}

// Tray is a live tray icon.
type Tray struct {
	impl impl
}

// New creates the tray icon. On Windows the icon appears immediately.
func New(opts Options) (*Tray, error) {
	if opts.Menu == nil {
		opts.Menu = func() []MenuItem { return nil }
	}
	platformImpl, err := newImpl(opts)
	if err != nil {
		return nil, err
	}
	return &Tray{impl: platformImpl}, nil
}

// UpdateTooltip refreshes the hover text.
func (t *Tray) UpdateTooltip(text string) error {
	if t == nil || t.impl == nil {
		return ErrUnsupported
	}
	return t.impl.updateTooltip(text)
}

// Remove destroys the icon and stops the message loop.
func (t *Tray) Remove() error {
	if t == nil || t.impl == nil {
		return nil
	}
	err := t.impl.remove()
	t.impl = nil
	return err
}

// Supported reports whether the current platform implements the tray.
func Supported() bool { return supported() }

// Tooltip truncates text to the 127 character Win32 limit.
func Tooltip(parts ...string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += " · "
		}
		out += p
	}
	if len(out) > 127 {
		out = out[:127]
	}
	return out
}

func wrapErr(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("tray: %s: %w", op, err)
}
