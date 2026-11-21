package mesh

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// RenderPNG draws a shaded three-quarter view of the mesh, centered and
// scaled to fit a size×size transparent image. Pure Go, no GPU.
func (m *Mesh) RenderPNG(size int) ([]byte, error) {
	img := m.Render(size)
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	return buf.Bytes(), err
}

func (m *Mesh) Render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	if m.Count() == 0 {
		return img
	}
	// Camera: rotate around Z by az, then tilt by el (looking down at the part).
	az, el := 35*math.Pi/180, 30*math.Pi/180
	ca, sa, ce, se := math.Cos(az), math.Sin(az), math.Cos(el), math.Sin(el)
	lo, hi := m.Bounds()
	cx, cy, cz := float64(lo[0]+hi[0])/2, float64(lo[1]+hi[1])/2, float64(lo[2]+hi[2])/2
	proj := func(x, y, z float64) (float64, float64, float64) {
		x, y, z = x-cx, y-cy, z-cz
		// Rotate about Z.
		rx := x*ca - y*sa
		ry := x*sa + y*ca
		// Tilt: screen up mixes Z and depth.
		sx := rx
		sy := z*ce - ry*se
		d := ry*ce + z*se // larger = farther away
		return sx, sy, d
	}
	// Fit to image.
	minx, miny, maxx, maxy := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i := 0; i+2 < len(m.Tris); i += 3 {
		sx, sy, _ := proj(float64(m.Tris[i]), float64(m.Tris[i+1]), float64(m.Tris[i+2]))
		minx, maxx = math.Min(minx, sx), math.Max(maxx, sx)
		miny, maxy = math.Min(miny, sy), math.Max(maxy, sy)
	}
	span := math.Max(maxx-minx, maxy-miny)
	if span <= 0 {
		span = 1
	}
	scale := float64(size) * 0.88 / span
	ox := float64(size)/2 - (minx+maxx)/2*scale
	oy := float64(size)/2 + (miny+maxy)/2*scale

	zbuf := make([]float64, size*size)
	for i := range zbuf {
		zbuf[i] = math.Inf(1)
	}
	// Light from upper left, slightly toward the viewer.
	lx, ly, lz := -0.45, 0.55, 0.70
	ln := math.Sqrt(lx*lx + ly*ly + lz*lz)
	lx, ly, lz = lx/ln, ly/ln, lz/ln

	for t := 0; t+8 < len(m.Tris)+0; t += 9 {
		var px, py, pd [3]float64
		var wx, wy, wz [3]float64
		for k := 0; k < 3; k++ {
			x, y, z := float64(m.Tris[t+k*3]), float64(m.Tris[t+k*3+1]), float64(m.Tris[t+k*3+2])
			wx[k], wy[k], wz[k] = x, y, z
			sx, sy, d := proj(x, y, z)
			px[k], py[k], pd[k] = ox+sx*scale, oy-sy*scale, d
		}
		// Face normal in view space for shading (two-sided).
		ux, uy, uz := wx[1]-wx[0], wy[1]-wy[0], wz[1]-wz[0]
		vx, vy, vz := wx[2]-wx[0], wy[2]-wy[0], wz[2]-wz[0]
		nx, ny, nz := uy*vz-uz*vy, uz*vx-ux*vz, ux*vy-uy*vx
		nl := math.Sqrt(nx*nx + ny*ny + nz*nz)
		if nl == 0 {
			continue
		}
		nx, ny, nz = nx/nl, ny/nl, nz/nl
		// Into view space (same rotation as proj).
		rnx := nx*ca - ny*sa
		rny := nx*sa + ny*ca
		vnx, vny, vnz := rnx, nz*ce-rny*se, rny*ce+nz*se
		if vnz > 0 { // facing away: flip (two-sided lighting)
			vnx, vny, vnz = -vnx, -vny, -vnz
		}
		diff := math.Max(0, vnx*lx+vny*ly-vnz*lz)
		shade := 0.32 + 0.68*diff
		col := color.NRGBA{uint8(math.Min(255, 70+150*shade)), uint8(math.Min(255, 110+130*shade)), uint8(math.Min(255, 150+105*shade)), 255}

		x0 := int(math.Max(0, math.Floor(math.Min(px[0], math.Min(px[1], px[2])))))
		x1 := int(math.Min(float64(size-1), math.Ceil(math.Max(px[0], math.Max(px[1], px[2])))))
		y0 := int(math.Max(0, math.Floor(math.Min(py[0], math.Min(py[1], py[2])))))
		y1 := int(math.Min(float64(size-1), math.Ceil(math.Max(py[0], math.Max(py[1], py[2])))))
		area := (px[1]-px[0])*(py[2]-py[0]) - (px[2]-px[0])*(py[1]-py[0])
		if math.Abs(area) < 1e-9 {
			continue
		}
		for y := y0; y <= y1; y++ {
			fy := float64(y) + 0.5
			for x := x0; x <= x1; x++ {
				fx := float64(x) + 0.5
				w0 := ((px[1]-fx)*(py[2]-fy) - (px[2]-fx)*(py[1]-fy)) / area
				w1 := ((px[2]-fx)*(py[0]-fy) - (px[0]-fx)*(py[2]-fy)) / area
				w2 := 1 - w0 - w1
				if w0 < 0 || w1 < 0 || w2 < 0 {
					continue
				}
				d := w0*pd[0] + w1*pd[1] + w2*pd[2]
				i := y*size + x
				if d < zbuf[i] {
					zbuf[i] = d
					img.SetNRGBA(x, y, col)
				}
			}
		}
	}
	return img
}
