//go:build !windows

package tray

// newImpl is unavailable outside Windows.
func newImpl(opts Options) (impl, error) {
	return nil, ErrUnsupported
}

func supported() bool { return false }
