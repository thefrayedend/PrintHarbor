package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// IconPNG is the PrintHarbor logo as a PNG at the given size.
func IconPNG(size int) []byte { return iconPNG(size) }

// IcoBytes builds a Windows .ico (PNG-compressed entries) for desktop
// shortcuts, so the pinned app shows the PrintHarbor anchor.
func IcoBytes() []byte {
	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	type ent struct {
		size int
		png  []byte
	}
	ents := make([]ent, 0, len(sizes))
	for _, s := range sizes {
		ents = append(ents, ent{s, iconPNG(s)})
	}
	var dir, blobs bytes.Buffer
	offset := 6 + 16*len(ents)
	binary.Write(&dir, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(&dir, binary.LittleEndian, uint16(1)) // type: icon
	binary.Write(&dir, binary.LittleEndian, uint16(len(ents)))
	for _, e := range ents {
		w := byte(e.size)
		if e.size >= 256 {
			w = 0 // 0 means 256 in the ICO directory
		}
		dir.WriteByte(w)                                    // width
		dir.WriteByte(w)                                    // height
		dir.WriteByte(0)                                    // palette
		dir.WriteByte(0)                                    // reserved
		binary.Write(&dir, binary.LittleEndian, uint16(1))  // colour planes
		binary.Write(&dir, binary.LittleEndian, uint16(32)) // bits per pixel
		binary.Write(&dir, binary.LittleEndian, uint32(len(e.png)))
		binary.Write(&dir, binary.LittleEndian, uint32(offset))
		offset += len(e.png)
		blobs.Write(e.png)
	}
	return append(dir.Bytes(), blobs.Bytes()...)
}

// iconPNG draws the PrintHarbor anchor (the same shape as the web logo) on a
// blue rounded square, so no icon file has to ship with the program.
func iconPNG(size int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size) / 32
	blue := color.NRGBA{0x2b, 0x7d, 0xe9, 0xff}
	seg := func(px, py, ax, ay, bx, by float64) float64 {
		dx, dy := bx-ax, by-ay
		t := math.Max(0, math.Min(1, ((px-ax)*dx+(py-ay)*dy)/(dx*dx+dy*dy)))
		return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := (float64(x)+.5)/s, (float64(y)+.5)/s
			// rounded square, radius 7
			qx, qy := math.Max(math.Abs(px-16)-9, 0), math.Max(math.Abs(py-16)-9, 0)
			bg := math.Max(0, math.Min(1, 7-math.Hypot(qx, qy)+.5))
			if bg == 0 {
				continue
			}
			// the anchor: shaft, cross bar and the curved bottom
			d := math.Min(seg(px, py, 16, 6, 16, 24), seg(px, py, 11, 10.5, 21, 10.5))
			cx, cy, r := 16.0, 15.0, 10.0
			if py >= cy {
				d = math.Min(d, math.Abs(math.Hypot(px-cx, py-cy)-r))
			} else {
				d = math.Min(d, math.Min(math.Hypot(px-(cx-r), py-cy), math.Hypot(px-(cx+r), py-cy)))
			}
			w := math.Max(0, math.Min(1, 1.6-d+.5/s))
			c := color.NRGBA{
				R: uint8(float64(blue.R)*(1-w) + 255*w),
				G: uint8(float64(blue.G)*(1-w) + 255*w),
				B: uint8(float64(blue.B)*(1-w) + 255*w),
				A: uint8(255 * bg),
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}
