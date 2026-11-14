//go:build !windows

package hotkey

import "errors"

func supported() bool { return false }

func register(combo string, trigger func()) (any, error) {
	return nil, errors.New("global hotkeys are only supported on Windows")
}

func unregister(handle any) {}
