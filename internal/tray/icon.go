package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
)

// BuildICO renders the application tray icon as a Windows .ico file.
func BuildICO(size int) []byte {
	img := drawBrand(size)
	return encodeICO(img)
}

// drawBrand paints a rounded accent square with a block "WT" monogram.
func drawBrand(size int) *image.RGBA {
	if size < 16 {
		size = 16
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	bg := color.RGBA{R: 0x3D, G: 0xDC, B: 0x97, A: 0xFF}
	fg := color.RGBA{R: 0x08, G: 0x12, B: 0x0E, A: 0xFF}

	// Rounded square background with a simple corner radius.
	radius := size / 5
	if radius < 2 {
		radius = 2
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if insideRounded(x, y, size, radius) {
				img.Set(x, y, bg)
			}
		}
	}

	// 5x7 block glyphs for "W" and "T".
	wGlyph := []string{
		"10001",
		"10001",
		"10001",
		"10101",
		"10101",
		"11011",
		"10001",
	}
	tGlyph := []string{
		"11111",
		"00100",
		"00100",
		"00100",
		"00100",
		"00100",
		"00100",
	}
	glyphH := len(wGlyph)
	glyphW := len(wGlyph[0])
	scale := size / 8
	if scale < 1 {
		scale = 1
	}
	totalW := (glyphW*2 + 1) * scale
	totalH := glyphH * scale
	startX := (size - totalW) / 2
	startY := (size - totalH) / 2
	drawGlyph := func(glyph []string, ox int) {
		for gy, row := range glyph {
			for gx, ch := range row {
				if ch != '1' {
					continue
				}
				for sy := 0; sy < scale; sy++ {
					for sx := 0; sx < scale; sx++ {
						px := ox + gx*scale + sx
						py := startY + gy*scale + sy
						if px >= 0 && py >= 0 && px < size && py < size {
							img.Set(px, py, fg)
						}
					}
				}
			}
		}
	}
	drawGlyph(wGlyph, startX)
	drawGlyph(tGlyph, startX+(glyphW+1)*scale)
	return img
}

func insideRounded(x, y, size, radius int) bool {
	corners := [][2]int{
		{radius, radius},
		{size - radius - 1, radius},
		{radius, size - radius - 1},
		{size - radius - 1, size - radius - 1},
	}
	for _, c := range corners {
		inCornerBoxX := (x < radius && c[0] == radius) || (x > size-radius-1 && c[0] != radius)
		inCornerBoxY := (y < radius && c[1] == radius) || (y > size-radius-1 && c[1] != radius)
		if inCornerBoxX && inCornerBoxY {
			dx := float64(x - c[0])
			dy := float64(y - c[1])
			if dx*dx+dy*dy > float64(radius*radius) {
				return false
			}
		}
	}
	return true
}

// encodeICO wraps an RGBA image into a single-image ICO (32bpp BGRA + AND mask).
func encodeICO(img *image.RGBA) []byte {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// XOR bitmap: bottom-up BGRA rows.
	xor := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		srcRow := h - 1 - y
		for x := 0; x < w; x++ {
			r, g, b, a := img.At(bounds.Min.X+x, bounds.Min.Y+srcRow).RGBA()
			o := (y*w + x) * 4
			xor[o] = byte(b >> 8)
			xor[o+1] = byte(g >> 8)
			xor[o+2] = byte(r >> 8)
			xor[o+3] = byte(a >> 8)
		}
	}
	// AND mask: 1 bit per pixel, rows padded to 4 bytes; zero = opaque.
	maskRow := ((w + 31) / 32) * 4
	mask := make([]byte, maskRow*h)

	header := make([]byte, 40)
	binary.LittleEndian.PutUint32(header[0:], 40)
	binary.LittleEndian.PutUint32(header[4:], uint32(w))
	binary.LittleEndian.PutUint32(header[8:], uint32(h*2)) // XOR + AND
	binary.LittleEndian.PutUint16(header[12:], 1)
	binary.LittleEndian.PutUint16(header[14:], 32)
	binary.LittleEndian.PutUint32(header[16:], 0) // BI_RGB
	binary.LittleEndian.PutUint32(header[20:], uint32(len(xor)+len(mask)))

	imageData := bytes.Join([][]byte{header, xor, mask}, nil)

	var buf bytes.Buffer
	// ICONDIR
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	// ICONDIRENTRY
	buf.WriteByte(byte(w))
	buf.WriteByte(byte(h))
	buf.WriteByte(0)
	buf.WriteByte(0)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(32))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(imageData)))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(22))
	buf.Write(imageData)
	return buf.Bytes()
}
