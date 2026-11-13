//go:build windows

package shot

import (
	"encoding/binary"
	"image"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const cfDIB = 8

var (
	user32                     = syscall.NewLazyDLL("user32.dll")
	procOpenClipboard          = user32.NewProc("OpenClipboard")
	procCloseClipboard         = user32.NewProc("CloseClipboard")
	procGetClipboardData       = user32.NewProc("GetClipboardData")
	procIsClipboardFormatAvail = user32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardSequence   = user32.NewProc("GetClipboardSequenceNumber")

	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procGlobalLock   = kernel32.NewProc("GlobalLock")
	procGlobalUnlock = kernel32.NewProc("GlobalUnlock")
	procGlobalSize   = kernel32.NewProc("GlobalSize")
)

func clipboardSupported() bool { return true }

type clipboardWatcher struct {
	cb     func(*image.RGBA)
	stopCh chan struct{}
	done   chan struct{}
	once   sync.Once
}

func newClipboardWatcher(cb func(*image.RGBA)) *clipboardWatcher {
	return &clipboardWatcher{cb: cb, stopCh: make(chan struct{}), done: make(chan struct{})}
}

func (w *clipboardWatcher) start() error {
	go func() {
		defer close(w.done)
		var last uint32
		if n, _, _ := procGetClipboardSequence.Call(); n != 0 {
			last = uint32(n)
		}
		ticker := time.NewTicker(900 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-w.stopCh:
				return
			case <-ticker.C:
				seq, _, _ := procGetClipboardSequence.Call()
				if seq == 0 || uint32(seq) == last {
					continue
				}
				last = uint32(seq)
				if img := readClipboardImage(); img != nil && w.cb != nil {
					w.cb(img)
				}
			}
		}
	}()
	return nil
}

func (w *clipboardWatcher) stop() {
	w.once.Do(func() { close(w.stopCh) })
	<-w.done
}

func readClipboardImage() *image.RGBA {
	if avail, _, _ := procIsClipboardFormatAvail.Call(cfDIB); avail == 0 {
		return nil
	}
	if ok, _, _ := procOpenClipboard.Call(0); ok == 0 {
		return nil
	}
	defer procCloseClipboard.Call()

	handle, _, _ := procGetClipboardData.Call(cfDIB)
	if handle == 0 {
		return nil
	}
	size, _, _ := procGlobalSize.Call(handle)
	if size < 40 {
		return nil
	}
	ptr, _, _ := procGlobalLock.Call(handle)
	if ptr == 0 {
		return nil
	}
	defer procGlobalUnlock.Call(handle)
	data := unsafe.Slice((*byte)(uintptrToPointer(ptr)), int(size))
	return decodeDIB(data)
}

// decodeDIB converts a CF_DIB payload (BITMAPINFOHEADER + pixels) to RGBA.
func decodeDIB(data []byte) *image.RGBA {
	if len(data) < 40 {
		return nil
	}
	headerSize := binary.LittleEndian.Uint32(data[0:4])
	if headerSize < 40 || int(headerSize) > len(data) {
		return nil
	}
	width := int(int32(binary.LittleEndian.Uint32(data[4:8])))
	heightRaw := int(int32(binary.LittleEndian.Uint32(data[8:12])))
	bitCount := int(binary.LittleEndian.Uint16(data[14:16]))
	compression := binary.LittleEndian.Uint32(data[16:20])
	if compression != 0 {
		return nil
	}
	if width <= 0 || heightRaw == 0 || (bitCount != 24 && bitCount != 32) {
		return nil
	}
	topDown := heightRaw < 0
	height := heightRaw
	if topDown {
		height = -heightRaw
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	pixels := data[headerSize:]
	bytesPerPixel := bitCount / 8
	stride := ((width*bitCount + 31) / 32) * 4
	if len(pixels) < stride*height {
		return nil
	}
	for y := 0; y < height; y++ {
		srcRow := y
		if !topDown {
			srcRow = height - 1 - y
		}
		rowStart := srcRow * stride
		for x := 0; x < width; x++ {
			offset := rowStart + x*bytesPerPixel
			if offset+bytesPerPixel > len(pixels) {
				break
			}
			b := pixels[offset]
			g := pixels[offset+1]
			r := pixels[offset+2]
			a := byte(255)
			if bitCount == 32 {
				if alpha := pixels[offset+3]; alpha != 0 {
					a = alpha
				}
			}
			di := img.PixOffset(x, y)
			img.Pix[di] = r
			img.Pix[di+1] = g
			img.Pix[di+2] = b
			img.Pix[di+3] = a
		}
	}
	return img
}
