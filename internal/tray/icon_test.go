package tray

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestBuildICOStructure(t *testing.T) {
	ico := BuildICO(32)
	if len(ico) < 22+40 {
		t.Fatalf("icon too small: %d bytes", len(ico))
	}
	if binary.LittleEndian.Uint16(ico[0:]) != 0 || binary.LittleEndian.Uint16(ico[2:]) != 1 {
		t.Fatal("invalid ICONDIR header")
	}
	if count := binary.LittleEndian.Uint16(ico[4:]); count != 1 {
		t.Fatalf("expected 1 image, got %d", count)
	}
	w, h := ico[6], ico[7]
	if w != 32 || h != 32 {
		t.Fatalf("unexpected dimensions %dx%d", w, h)
	}
	if bpp := binary.LittleEndian.Uint16(ico[12:]); bpp != 32 {
		t.Fatalf("expected 32bpp, got %d", bpp)
	}
	size := binary.LittleEndian.Uint32(ico[14:])
	offset := binary.LittleEndian.Uint32(ico[18:])
	if offset != 22 {
		t.Fatalf("unexpected image offset %d", offset)
	}
	if int(offset)+int(size) != len(ico) {
		t.Fatalf("image size mismatch: offset %d + size %d != %d", offset, size, len(ico))
	}
	// BITMAPINFOHEADER: width 32, height 64 (XOR + AND masks), 32bpp.
	bmp := ico[22:]
	if binary.LittleEndian.Uint32(bmp[0:]) != 40 {
		t.Fatal("unexpected BITMAPINFOHEADER size")
	}
	if binary.LittleEndian.Uint32(bmp[4:]) != 32 || binary.LittleEndian.Uint32(bmp[8:]) != 64 {
		t.Fatalf("unexpected bitmap dimensions: %d x %d", binary.LittleEndian.Uint32(bmp[4:]), binary.LittleEndian.Uint32(bmp[8:]))
	}
	if binary.LittleEndian.Uint16(bmp[14:]) != 32 {
		t.Fatal("unexpected bit count")
	}
	// Pixels must not be entirely transparent.
	pixels := bmp[40 : 40+32*32*4]
	nonZero := false
	for i := 3; i < len(pixels); i += 4 {
		if pixels[i] != 0 {
			nonZero = true
			break
		}
	}
	if !nonZero {
		t.Fatal("icon appears fully transparent")
	}
}

func TestBuildICOOtherSizes(t *testing.T) {
	for _, size := range []int{16, 24, 32, 48} {
		ico := BuildICO(size)
		if got := int(ico[6]); got != size {
			t.Fatalf("size %d: header says %d", size, got)
		}
		if !bytes.Equal(ico[:4], []byte{0, 0, 1, 0}) {
			t.Fatalf("size %d: bad header", size)
		}
	}
}

func TestTooltipTruncation(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	if got := Tooltip(long); len(got) != 127 {
		t.Fatalf("expected 127 chars, got %d", len(got))
	}
	if got := Tooltip("a", "", "b"); got != "a · b" {
		t.Fatalf("unexpected join: %q", got)
	}
}
