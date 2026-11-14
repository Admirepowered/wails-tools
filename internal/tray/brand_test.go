package tray

import (
	"bytes"
	"testing"
)

// The brand icon must stay squircle-shaped: opaque in the middle, transparent
// in the corners, mint at the top-left and azure at the bottom-right.
func TestRenderBrandIcon(t *testing.T) {
	for _, size := range []int{16, 32, 256} {
		img := Render(size)
		if got := img.Bounds().Dx(); got != size {
			t.Fatalf("size %d: got %d", size, got)
		}
		b := img.Bounds()
		cx, cy := b.Dx()/2, b.Dy()/2

		// Centre is opaque and carries the dark monogram.
		if a := img.RGBAAt(cx, cy).A; a < 200 {
			t.Fatalf("size %d: centre alpha %d, want opaque", size, a)
		}
		// Corners are rounded away.
		if a := img.RGBAAt(0, 0).A; a != 0 {
			t.Fatalf("size %d: corner alpha %d, want transparent", size, a)
		}
		// Gradient runs mint -> azure along the diagonal.
		top := img.RGBAAt(b.Dx()/2, b.Dy()/8)
		bottom := img.RGBAAt(b.Dx()/2, b.Dy()-b.Dy()/8)
		if top.A == 0 || bottom.A == 0 {
			t.Fatalf("size %d: gradient samples not opaque: %v %v", size, top, bottom)
		}
		if !(top.G > bottom.G && top.B < bottom.B) {
			t.Fatalf("size %d: expected mint->azure, got top=%v bottom=%v", size, top, bottom)
		}
	}
}

func TestBuildMultiICO(t *testing.T) {
	sizes := []int{16, 24, 32}
	ico := BuildMultiICO(sizes)
	if len(ico) < 6 {
		t.Fatal("icon too small")
	}
	if bytes.Compare(ico[:4], []byte{0, 0, 1, 0}) != 0 {
		t.Fatal("bad ICONDIR header")
	}
	count := int(ico[4]) | int(ico[5])<<8
	if count != len(sizes) {
		t.Fatalf("expected %d images, got %d", len(sizes), count)
	}
	for i, size := range sizes {
		entry := 6 + i*16
		if got := int(ico[entry]); got != size {
			t.Fatalf("entry %d: width %d, want %d", i, got, size)
		}
		offset := int(ico[entry+12]) | int(ico[entry+13])<<8 | int(ico[entry+14])<<16 | int(ico[entry+15])<<24
		headerLen := 6 + len(sizes)*16
		if offset < headerLen || offset >= len(ico) {
			t.Fatalf("entry %d: bad offset %d", i, offset)
		}
	}
}

// Semi-transparent edges must stay free of colour fringes: the PNG round trip
// is what the tray actually renders.
func TestRenderAlphaEdgeIsClean(t *testing.T) {
	img := Render(64)
	found := false
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] == 0 || img.Pix[i] == 0xFF {
			continue
		}
		found = true
		// Premultiplied storage: a channel can never exceed its alpha.
		a := uint32(img.Pix[i])
		for c := i - 3; c < i; c++ {
			if uint32(img.Pix[c]) > a {
				t.Fatalf("channel %d exceeds alpha %d at edge pixel %v", img.Pix[c], a, img.RGBAAt((c-3)/4%64, (c-3)/4/64))
			}
		}
		break
	}
	if !found {
		t.Skip("no semi-transparent pixels")
	}
}
