//go:build windows

package shot

import (
	"errors"
	"fmt"
	"image"
	"syscall"
	"unsafe"
)

var (
	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")

	gdi32                  = syscall.NewLazyDLL("gdi32.dll")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procBitBlt             = gdi32.NewProc("BitBlt")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
)

const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
	smCMonitors       = 80
	srcCopy           = 0x00CC0020
	dibRGBColors      = 0
)

// displayRect is a monitor rectangle in virtual-screen coordinates.
type displayRect struct {
	left, top, right, bottom int32
}

func displayCount() int {
	if n, _, _ := procGetSystemMetrics.Call(smCMonitors); n > 0 {
		return int(n)
	}
	return 1
}

// monitorRects enumerates monitors in a stable order.
func monitorRects() []displayRect {
	var rects []displayRect
	cb := syscall.NewCallback(func(hMonitor, hdc, lprc, data uintptr) uintptr {
		if lprc != 0 {
			r := (*struct{ Left, Top, Right, Bottom int32 })(uintptrToPointer(lprc))
			rects = append(rects, displayRect{r.Left, r.Top, r.Right, r.Bottom})
		}
		return 1 // continue enumeration
	})
	procEnumDisplayMonitors.Call(0, 0, cb, 0)
	return rects
}

func virtualScreenRect() displayRect {
	get := func(idx uintptr) int32 {
		v, _, _ := procGetSystemMetrics.Call(idx)
		return int32(v)
	}
	x, y := get(smXVirtualScreen), get(smYVirtualScreen)
	return displayRect{left: x, top: y, right: x + get(smCXVirtualScreen), bottom: y + get(smCYVirtualScreen)}
}

// captureDisplay blits a monitor (-1 = the whole virtual screen) into a
// top-down 32bpp DIB section and converts it to RGBA.
func captureDisplay(display int) (*image.RGBA, error) {
	rects := monitorRects()
	var target displayRect
	switch {
	case display < 0:
		target = virtualScreenRect()
	case display < len(rects):
		target = rects[display]
	default:
		return nil, fmt.Errorf("显示器 %d 不存在（共 %d 台）", display, len(rects))
	}
	width := int(target.right - target.left)
	height := int(target.bottom - target.top)
	if width <= 0 || height <= 0 {
		return nil, errors.New("无效的屏幕尺寸")
	}

	hdcScreen, _, _ := procGetDC.Call(0)
	if hdcScreen == 0 {
		return nil, errors.New("无法获取屏幕设备上下文")
	}
	defer procReleaseDC.Call(0, hdcScreen)

	hdcMem, _, _ := procCreateCompatibleDC.Call(hdcScreen)
	if hdcMem == 0 {
		return nil, errors.New("无法创建内存设备上下文")
	}
	defer procDeleteDC.Call(hdcMem)

	// A DIB section exposes its pixels directly, which is far more reliable
	// than GetDIBits across display drivers.
	header := make([]byte, 40)
	putU32(header[0:], 40)
	putI32(header[4:], int32(width))
	putI32(header[8:], int32(-height)) // top-down
	putU16(header[12:], 1)             // planes
	putU16(header[14:], 32)            // bits per pixel
	putU32(header[16:], 0)             // BI_RGB
	putU32(header[20:], uint32(width*height*4))

	var bits unsafe.Pointer
	hBitmap, _, err := procCreateDIBSection.Call(hdcMem, uintptr(unsafe.Pointer(&header[0])), dibRGBColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hBitmap == 0 || bits == nil {
		return nil, fmt.Errorf("CreateDIBSection 失败: %v", err)
	}
	defer procDeleteObject.Call(hBitmap)

	old, _, _ := procSelectObject.Call(hdcMem, hBitmap)
	ok, _, bltErr := procBitBlt.Call(hdcMem, 0, 0, uintptr(width), uintptr(height),
		hdcScreen, uintptr(int32(target.left)), uintptr(int32(target.top)), srcCopy)
	procSelectObject.Call(hdcMem, old)
	if ok == 0 {
		return nil, fmt.Errorf("BitBlt 失败: %v", bltErr)
	}

	pixels := unsafe.Slice((*byte)(bits), width*height*4)
	return rgbaFromBGRA(pixels, width, height), nil
}

func putU32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

func putI32(b []byte, v int32) { putU32(b, uint32(v)) }

func putU16(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}

// rgbaFromBGRA converts a top-down BGRA buffer into an RGBA image.
func rgbaFromBGRA(pixels []byte, width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		row := y * width * 4
		for x := 0; x < width; x++ {
			o := row + x*4
			di := img.PixOffset(x, y)
			img.Pix[di] = pixels[o+2]   // R
			img.Pix[di+1] = pixels[o+1] // G
			img.Pix[di+2] = pixels[o]   // B
			img.Pix[di+3] = 0xFF
		}
	}
	return img
}
