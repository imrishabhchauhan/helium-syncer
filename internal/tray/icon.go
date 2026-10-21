package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// The icon is drawn in code (two arrows chasing each other in a circle), so
// there are no image files to keep in sync across Windows and macOS.

var (
	blue  = color.NRGBA{0x15, 0x60, 0xEF, 0xFF}
	amber = color.NRGBA{0xE5, 0x8A, 0x00, 0xFF}
	white = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	black = color.NRGBA{0x00, 0x00, 0x00, 0xFF}
)

// mark draws the sync mark. With a background it is a coloured rounded
// square with a white mark (Windows tray); without, a plain mark in fg
// (macOS menu-bar template, which the system tints for light/dark).
func mark(size int, bg, fg color.NRGBA, withBg bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	ro, ri := 0.34, 0.22 // ring radii as a share of the size
	if !withBg {
		ro, ri = 0.40, 0.27
	}
	const ss = 4 // supersampling per axis, for smooth edges
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var inBg, inFg int
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					u := (float64(x) + (float64(sx)+0.5)/ss) / float64(size)
					v := (float64(y) + (float64(sy)+0.5)/ss) / float64(size)
					if withBg && inRoundedSquare(u, v, 0.22) {
						inBg++
					}
					if inMark(u, v, ro, ri) {
						inFg++
					}
				}
			}
			n := float64(ss * ss)
			fa, ba := float64(inFg)/n, float64(inBg)/n
			if !withBg {
				img.SetNRGBA(x, y, color.NRGBA{fg.R, fg.G, fg.B, uint8(math.Round(255 * fa))})
				continue
			}
			// White mark over the coloured square; the mark only shows
			// where the square is.
			fa = math.Min(fa, ba)
			mix := func(a, b uint8) uint8 { return uint8(math.Round(float64(a)*(1-fa) + float64(b)*fa)) }
			img.SetNRGBA(x, y, color.NRGBA{mix(bg.R, fg.R), mix(bg.G, fg.G), mix(bg.B, fg.B), uint8(math.Round(255 * ba))})
		}
	}
	return img
}

func inRoundedSquare(u, v, r float64) bool {
	dx := math.Max(math.Max(r-u, u-(1-r)), 0)
	dy := math.Max(math.Max(r-v, v-(1-r)), 0)
	return dx*dx+dy*dy <= r*r
}

// inMark: two arcs with gaps at upper right and lower left, each ending in
// an arrowhead that points clockwise into its gap.
func inMark(u, v, ro, ri float64) bool {
	dx, dy := u-0.5, v-0.5
	r := math.Hypot(dx, dy)
	a := math.Atan2(dy, dx) * 180 / math.Pi // clockwise on screen, -180..180
	const gap = 28.0                        // half-width of each gap, degrees
	inGap := func(center float64) bool { return math.Abs(angleDiff(a, center)) < gap }
	if r >= ri && r <= ro && !inGap(-45) && !inGap(135) {
		return true
	}
	mid, half := (ro+ri)/2, (ro-ri)*1.05
	for _, end := range []float64{135 - gap, -45 - gap} {
		if inArrow(dx, dy, end, mid, half) {
			return true
		}
	}
	return false
}

func inArrow(dx, dy, endDeg, mid, half float64) bool {
	pt := func(rad, deg float64) (float64, float64) {
		t := deg * math.Pi / 180
		return rad * math.Cos(t), rad * math.Sin(t)
	}
	ax, ay := pt(mid-half, endDeg)
	bx, by := pt(mid+half, endDeg)
	cx, cy := pt(mid, endDeg+30)
	s1 := (bx-ax)*(dy-ay) - (by-ay)*(dx-ax)
	s2 := (cx-bx)*(dy-by) - (cy-by)*(dx-bx)
	s3 := (ax-cx)*(dy-cy) - (ay-cy)*(dx-cx)
	return (s1 >= 0 && s2 >= 0 && s3 >= 0) || (s1 <= 0 && s2 <= 0 && s3 <= 0)
}

func angleDiff(a, b float64) float64 {
	d := math.Mod(a-b+540, 360) - 180
	return d
}

func pngBytes(img image.Image) []byte {
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// icoBytes packs images into a Windows .ico, using classic 32-bit bitmaps
// (understood by every Windows version's icon loader).
func icoBytes(imgs ...*image.NRGBA) []byte {
	var out bytes.Buffer
	w := func(v any) { binary.Write(&out, binary.LittleEndian, v) }
	w(uint16(0))
	w(uint16(1))
	w(uint16(len(imgs)))
	var bodies [][]byte
	for _, img := range imgs {
		bodies = append(bodies, dib(img))
	}
	offset := 6 + 16*len(imgs)
	for i, img := range imgs {
		s := img.Bounds().Dx()
		w(uint8(s % 256))
		w(uint8(s % 256))
		w(uint8(0))
		w(uint8(0))
		w(uint16(1))
		w(uint16(32))
		w(uint32(len(bodies[i])))
		w(uint32(offset))
		offset += len(bodies[i])
	}
	for _, b := range bodies {
		out.Write(b)
	}
	return out.Bytes()
}

func dib(img *image.NRGBA) []byte {
	s := img.Bounds().Dx()
	maskRow := ((s + 31) / 32) * 4
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	w(uint32(40))
	w(int32(s))
	w(int32(2 * s)) // colour rows + mask rows
	w(uint16(1))
	w(uint16(32))
	w(uint32(0))
	w(uint32(s*s*4 + maskRow*s))
	w(int32(0))
	w(int32(0))
	w(uint32(0))
	w(uint32(0))
	for y := s - 1; y >= 0; y-- { // bottom-up
		for x := 0; x < s; x++ {
			c := img.NRGBAAt(x, y)
			b.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	b.Write(make([]byte, maskRow*s)) // alpha decides; AND mask all zero
	return b.Bytes()
}
