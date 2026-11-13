//go:build !windows

package shot

import (
	"errors"
	"image"
)

func displayCount() int { return 1 }

func captureDisplay(display int) (*image.RGBA, error) {
	return nil, errors.New("screen capture is only implemented on Windows")
}
