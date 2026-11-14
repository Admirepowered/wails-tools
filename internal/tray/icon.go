package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Brand palette: a mint-to-azure gradient squircle with a near-black monogram.
var (
	brandTop = color.RGBA{R: 0x3D, G: 0xD3, B: 0xB0, A: 0xFF}
	brandBot = color.RGBA{R: 0x3D, G: 0x9B, B: 0xE6, A: 0xFF}
	brandInk = color.RGBA{R: 0x08, G: 0x12, B: 0x0E, A: 0xFF}
)

// Squircle exponent; ~2 is a circle, larger tends towards a square.
const brandSquircle = 4.2

// Monogram proportions relative to the icon size.
const (
	markWidthRatio  = 0.63
	markHeightRatio = 0.355
)

// BuildICO renders the application tray icon as a single-image Windows .ico.
func BuildICO(size int) []byte {
	return encodeICO(Render(size))
}

// BuildMultiICO renders a multi-resolution .ico so Windows can pick a crisp
// image for 100%, 125% and 150% DPI instead of rescaling one bitmap.
func BuildMultiICO(sizes []int) []byte {
	imgs := make([]*image.RGBA, 0, len(sizes))
	for _, s := range sizes {
		imgs = append(imgs, Render(s))
	}
	return encodeICOImages(imgs)
}

// Render draws the brand icon as a square RGBA image of the given size.
func Render(size int) *image.RGBA {
	if size < 8 {
		size = 8
	}
	// Supersample, then box-filter down: cheap anti-aliasing for both the
	// squircle edge and the monogram.
	super := 4
	if size <= 24 {
		super = 8
	}
	s := size * super
	img := image.NewRGBA(image.Rect(0, 0, s, s))
	fillSquircle(img)
	drawMonogram(img, size)
	return downsample(img, size)
}

// fillSquircle paints the gradient background, clipped to a superellipse.
func fillSquircle(img *image.RGBA) {
	s := img.Bounds().Dx()
	for y := 0; y < s; y++ {
		v := (float64(y) + 0.5) / float64(s)
		for x := 0; x < s; x++ {
			u := (float64(x) + 0.5) / float64(s)
			cx := math.Abs(u-0.5) * 2
			cy := math.Abs(v-0.5) * 2
			if math.Pow(cx, brandSquircle)+math.Pow(cy, brandSquircle) > 1 {
				continue
			}
			// Diagonal light source: mint at the top-left, azure at the
			// bottom-right.
			t := 0.34*u + 0.66*v
			img.Set(x, y, lerpColor(brandTop, brandBot, clamp01(t)))
		}
	}
}

func lerpColor(a, b color.RGBA, t float64) color.RGBA {
	lerp := func(x, y uint8) uint8 {
		return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t))
	}
	return color.RGBA{R: lerp(a.R, b.R), G: lerp(a.G, b.G), B: lerp(a.B, b.B), A: 0xFF}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// drawMonogram stamps "WT" in the brand ink colour, normalised to a fixed
// box so every icon size keeps the same proportions.
func drawMonogram(img *image.RGBA, size int) {
	s := img.Bounds().Dx()
	mask := renderMark(s)
	if mask == nil {
		drawBlockMonogram(img)
		return
	}
	box := inkBounds(mask)
	if box.Empty() {
		drawBlockMonogram(img)
		return
	}
	tw := int(math.Round(float64(s) * markWidthRatio))
	th := int(math.Round(float64(s) * markHeightRatio))
	if size <= 24 {
		// Tiny tray sizes lose the counters: give the letters more room and a
		// firmer edge so "WT" still reads at 16px.
		tw = int(math.Round(float64(s) * 0.74))
		th = int(math.Round(float64(s) * 0.44))
	}
	scaled := resampleAlpha(mask, box, tw, th)
	if size <= 24 {
		boostAlpha(scaled, 0.78)
	}
	compositeInk(img, scaled, (s-tw)/2, (s-th)/2)
}

// boostAlpha applies a gamma curve that keeps semi-transparent coverage, so
// downsampled strokes stay solid instead of fading into the background.
func boostAlpha(m *image.Alpha, gamma float64) {
	b := m.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			a := m.AlphaAt(x, y).A
			if a == 0 || a == 0xFF {
				continue
			}
			v := math.Pow(float64(a)/255, gamma) * 255
			if v > 255 {
				v = 255
			}
			m.SetAlpha(x, y, color.Alpha{A: uint8(math.Round(v))})
		}
	}
}

// renderMark draws the monogram into an alpha mask at supersampled size.
func renderMark(s int) *image.Alpha {
	f := brandFont()
	if f == nil {
		return nil
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    float64(s) * 0.55,
		DPI:     72,
		Hinting: font.HintingNone,
	})
	if err != nil {
		return nil
	}
	defer face.Close()

	mask := image.NewRGBA(image.Rect(0, 0, s, s))
	// Draw white over transparent so the alpha channel is the glyph coverage.
	d := &font.Drawer{Dst: mask, Src: image.White, Face: face}
	const text = "WT"
	wpx := int(d.MeasureString(text)) / 64
	// Roughly centre the glyphs; the ink bounds below do the exact fit.
	d.Dot = fixed.P((s-wpx)/2, int(float64(s)*0.62))
	d.DrawString(text)
	alpha := image.NewAlpha(image.Rect(0, 0, s, s))
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			alpha.SetAlpha(x, y, color.Alpha{A: mask.RGBAAt(x, y).A})
		}
	}
	return alpha
}

// drawBlockMonogram is the fallback when no system font can be loaded.
func drawBlockMonogram(img *image.RGBA) {
	s := img.Bounds().Dx()
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
	scale := s / 8
	if scale < 1 {
		scale = 1
	}
	totalW := (glyphW*2 + 1) * scale
	totalH := glyphH * scale
	startX := (s - totalW) / 2
	startY := (s - totalH) / 2
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
						if px >= 0 && py >= 0 && px < s && py < s {
							img.Set(px, py, brandInk)
						}
					}
				}
			}
		}
	}
	drawGlyph(wGlyph, startX)
	drawGlyph(tGlyph, startX+(glyphW+1)*scale)
}

var (
	brandFontOnce sync.Once
	brandFaceFont *opentype.Font
)

// brandFont loads a heavy sans font from the Windows font directory. The icon
// is only drawn on Windows, but every lookup degrades gracefully.
func brandFont() *opentype.Font {
	brandFontOnce.Do(func() {
		dirs := []string{}
		for _, env := range []string{"SystemRoot", "windir"} {
			if root := os.Getenv(env); root != "" {
				dirs = append(dirs, filepath.Join(root, "Fonts"))
			}
		}
		dirs = append(dirs, `C:\Windows\Fonts`)
		names := []string{"ariblk.ttf", "arialbd.ttf", "segoeuib.ttf", "seguisb.ttf"}
		for _, dir := range dirs {
			for _, name := range names {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					continue
				}
				f, err := opentype.Parse(data)
				if err != nil {
					continue
				}
				brandFaceFont = f
				return
			}
		}
	})
	return brandFaceFont
}

// inkBounds returns the tight bounding box of non-zero alpha in m.
func inkBounds(m *image.Alpha) image.Rectangle {
	b := m.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	found := false
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if m.AlphaAt(x, y).A == 0 {
				continue
			}
			found = true
			if x < minX {
				minX = x
			}
			if y < minY {
				minY = y
			}
			if x > maxX {
				maxX = x
			}
			if y > maxY {
				maxY = y
			}
		}
	}
	if !found {
		return image.Rectangle{}
	}
	return image.Rect(minX, minY, maxX+1, maxY+1)
}

// resampleAlpha scales src (cropped to box) to w×h with bilinear sampling.
func resampleAlpha(src *image.Alpha, box image.Rectangle, w, h int) *image.Alpha {
	if w < 1 || h < 1 || box.Empty() {
		return image.NewAlpha(image.Rect(0, 0, 1, 1))
	}
	out := image.NewAlpha(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := float64(box.Min.Y) + (float64(y)+0.5)*float64(box.Dy())/float64(h) - 0.5
		for x := 0; x < w; x++ {
			sx := float64(box.Min.X) + (float64(x)+0.5)*float64(box.Dx())/float64(w) - 0.5
			out.SetAlpha(x, y, color.Alpha{A: sampleAlpha(src, sx, sy)})
		}
	}
	return out
}

func sampleAlpha(src *image.Alpha, x, y float64) uint8 {
	x0 := int(math.Floor(x))
	y0 := int(math.Floor(y))
	fx := x - float64(x0)
	fy := y - float64(y0)

	at := func(px, py int) float64 {
		b := src.Bounds()
		if px < b.Min.X {
			px = b.Min.X
		}
		if py < b.Min.Y {
			py = b.Min.Y
		}
		if px >= b.Max.X {
			px = b.Max.X - 1
		}
		if py >= b.Max.Y {
			py = b.Max.Y - 1
		}
		return float64(src.AlphaAt(px, py).A)
	}

	top := at(x0, y0)*(1-fx) + at(x0+1, y0)*fx
	bottom := at(x0, y0+1)*(1-fx) + at(x0+1, y0+1)*fx
	return uint8(math.Round(clamp01((top*(1-fy)+bottom*fy)/255) * 255))
}

// compositeInk blends the mask over dst at (ox, oy) using brandInk. dst and
// the result are premultiplied (image.RGBA semantics), so the blend happens in
// premultiplied space.
func compositeInk(dst *image.RGBA, mask *image.Alpha, ox, oy int) {
	m := mask.Bounds()
	for y := m.Min.Y; y < m.Max.Y; y++ {
		for x := m.Min.X; x < m.Max.X; x++ {
			a := mask.AlphaAt(x, y).A
			if a == 0 {
				continue
			}
			px, py := ox+x, oy+y
			if !image.Pt(px, py).In(dst.Bounds()) {
				continue
			}
			under := dst.RGBAAt(px, py)
			if under.A == 0 {
				// Outside the squircle: nothing to blend onto.
				continue
			}
			if a == 0xFF {
				dst.Set(px, py, brandInk)
				continue
			}
			af := float64(a) / 255
			dst.Set(px, py, color.RGBA{
				R: uint8(math.Round(float64(brandInk.R)*af + float64(under.R)*(1-af))),
				G: uint8(math.Round(float64(brandInk.G)*af + float64(under.G)*(1-af))),
				B: uint8(math.Round(float64(brandInk.B)*af + float64(under.B)*(1-af))),
				A: under.A,
			})
		}
	}
}

// downsample box-filters src down to size×size. The result is premultiplied,
// which is what image.RGBA stores, so transparent corners stay clean.
func downsample(src *image.RGBA, size int) *image.RGBA {
	s := src.Bounds().Dx()
	factor := s / size
	if factor < 1 {
		factor = 1
	}
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	n := uint64(factor * factor)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var sr, sg, sb, sa uint64
			for dy := 0; dy < factor; dy++ {
				for dx := 0; dx < factor; dx++ {
					// RGBA() returns premultiplied 16-bit components.
					r, g, b, a := src.At(x*factor+dx, y*factor+dy).RGBA()
					sr += uint64(r)
					sg += uint64(g)
					sb += uint64(b)
					sa += uint64(a)
				}
			}
			if sa == 0 {
				continue
			}
			out.Set(x, y, color.RGBA{
				R: uint8((sr / n) >> 8),
				G: uint8((sg / n) >> 8),
				B: uint8((sb / n) >> 8),
				A: uint8((sa / n) >> 8),
			})
		}
	}
	return out
}

// encodeICO wraps a single RGBA image into a 32bpp ICO with an AND mask.
func encodeICO(img *image.RGBA) []byte {
	return encodeICOImages([]*image.RGBA{img})
}

// encodeICOImages builds a multi-image ICO in the order given.
func encodeICOImages(imgs []*image.RGBA) []byte {
	if len(imgs) == 0 {
		return nil
	}
	entries := make([][]byte, len(imgs))
	offsets := make([]uint32, len(imgs))
	headerLen := 6 + 16*len(imgs)
	var total uint32 = uint32(headerLen)
	for i, img := range imgs {
		data := icoImageData(img)
		entries[i] = data
		offsets[i] = total
		total += uint32(len(data))
	}

	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0))         // reserved
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))         // type: icon
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(imgs))) // count
	for i, img := range imgs {
		b := img.Bounds()
		w, h := b.Dx(), b.Dy()
		buf.WriteByte(dimByte(w))
		buf.WriteByte(dimByte(h))
		buf.WriteByte(0)                                        // palette size
		buf.WriteByte(0)                                        // reserved
		_ = binary.Write(&buf, binary.LittleEndian, uint16(1))  // planes
		_ = binary.Write(&buf, binary.LittleEndian, uint16(32)) // bit count
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(entries[i])))
		_ = binary.Write(&buf, binary.LittleEndian, offsets[i])
	}
	for _, data := range entries {
		buf.Write(data)
	}
	return buf.Bytes()
}

// dimByte encodes an icon dimension; 256 is stored as 0.
func dimByte(v int) byte {
	if v >= 256 {
		return 0
	}
	return byte(v)
}

// straight converts one premultiplied 16-bit channel back to straight 8-bit.
func straight(v, a uint32) byte {
	if a == 0 {
		return 0
	}
	s := v * 0xFFFF / a
	if s > 0xFFFF {
		s = 0xFFFF
	}
	return byte(s >> 8)
}

// icoImageData renders one BITMAPINFOHEADER + XOR bitmap + AND mask.
func icoImageData(img *image.RGBA) []byte {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// XOR bitmap: bottom-up BGRA rows. ICO bitmaps carry straight alpha, so
	// un-premultiply the stored image.RGBA values.
	xor := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		srcRow := h - 1 - y
		for x := 0; x < w; x++ {
			r, g, b, a := img.At(bounds.Min.X+x, bounds.Min.Y+srcRow).RGBA()
			o := (y*w + x) * 4
			if a == 0 {
				continue
			}
			xor[o] = straight(b, a)
			xor[o+1] = straight(g, a)
			xor[o+2] = straight(r, a)
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

	return bytes.Join([][]byte{header, xor, mask}, nil)
}
