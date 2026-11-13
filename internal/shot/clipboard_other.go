//go:build !windows

package shot

import (
	"errors"
	"image"
)

func clipboardSupported() bool { return false }

type clipboardWatcher struct {
	cb func(*image.RGBA)
}

func newClipboardWatcher(cb func(*image.RGBA)) *clipboardWatcher {
	return &clipboardWatcher{cb: cb}
}

func (w *clipboardWatcher) start() error {
	return errors.New("clipboard watching is only implemented on Windows")
}

func (w *clipboardWatcher) stop() {}
