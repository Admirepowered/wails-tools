package app

import (
	"errors"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ClipboardGet returns the OS clipboard text. The WebView denies
// navigator.clipboard.readText() in this environment, so the terminal menu uses
// this binding instead.
func (a *App) ClipboardGet() (string, error) {
	if a.ctx == nil {
		return "", errors.New("app not ready")
	}
	return runtime.ClipboardGetText(a.ctx)
}

// ClipboardPut writes text to the OS clipboard.
func (a *App) ClipboardPut(text string) error {
	if a.ctx == nil {
		return errors.New("app not ready")
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	runtime.ClipboardSetText(a.ctx, text)
	return nil
}
