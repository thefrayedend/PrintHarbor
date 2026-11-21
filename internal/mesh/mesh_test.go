package mesh

import (
	"archive/zip"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// cube returns the 12 triangles of an axis-aligned box.
func cube(sx, sy, sz float32) []float32 {
	v := [8][3]float32{{0, 0, 0}, {sx, 0, 0}, {sx, sy, 0}, {0, sy, 0}, {0, 0, sz}, {sx, 0, sz}, {sx, sy, sz}, {0, sy, sz}}
	f := [][3]int{{0, 2, 1}, {0, 3, 2}, {4, 5, 6}, {4, 6, 7}, {0, 1, 5}, {0, 5, 4}, {1, 2, 6}, {1, 6, 5}, {2, 3, 7}, {2, 7, 6}, {3, 0, 4}, {3, 4, 7}}
	var out []float32
	for _, t := range f {
		for _, i := range t {
			out = append(out, v[i][:]...)
		}
	}
	return out
}

func writeBinarySTL(t *testing.T, path string, tris []float32) {
	f, _ := os.Create(path)
	defer f.Close()
	f.Write(make([]byte, 80))
	binary.Write(f, binary.LittleEndian, uint32(len(tris)/9))
	for i := 0; i < len(tris); i += 9 {
		f.Write(make([]byte, 12))
		for _, v := range tris[i : i+9] {
			binary.Write(f, binary.LittleEndian, math.Float32bits(v))
		}
		f.Write([]byte{0, 0})
	}
}

func TestFormatsAndRender(t *testing.T) {
	dir := t.TempDir()
	stl := filepath.Join(dir, "c.stl")
	writeBinarySTL(t, stl, cube(20, 10, 5))
	obj := filepath.Join(dir, "q.obj")
	os.WriteFile(obj, []byte("v 0 0 0\nv 4 0 0\nv 4 4 0\nv 0 4 2\nf 1/1 2/2 3/3 4/4\n"), 0o644)
	tmf := filepath.Join(dir, "m.3mf")
	zf, _ := os.Create(tmf)
	zw := zip.NewWriter(zf)
	w, _ := zw.Create("3D/3dmodel.model")
	w.Write([]byte(`<model><resources><object id="1"><mesh><vertices><vertex x="0" y="0" z="0"/><vertex x="10" y="0" z="0"/><vertex x="0" y="10" z="3"/></vertices>
	  <triangles><triangle v1="0" v2="1" v3="2"/></triangles></mesh></object>
	  <object id="2"><components><component objectid="1" transform="2 0 0 0 2 0 0 0 2 0 0 0"/></components></object></resources>
	  <build><item objectid="2" transform="1 0 0 0 1 0 0 0 1 50 50 0"/></build></model>`))
	zw.Close()
	zf.Close()

	m, err := Load(stl, 0)
	if err != nil || m.Count() != 12 || m.Size() != [3]float64{20, 10, 5} {
		t.Fatal(err, m.Count(), m.Size())
	}
	o, err := Load(obj, 0)
	if err != nil || o.Count() != 2 || o.Size()[2] != 2 {
		t.Fatal(err, o.Count(), o.Size())
	}
	x, err := Load(tmf, 0)
	if err != nil || x.Count() != 1 || x.Size() != [3]float64{20, 20, 6} {
		t.Fatal(err, x.Count(), x.Size())
	}
	b := m.Binary()
	if binary.LittleEndian.Uint32(b) != 12 || len(b) != 4+12*9*4 {
		t.Fatal("bad binary")
	}
	png, err := m.RenderPNG(128)
	if err != nil || len(png) < 200 {
		t.Fatal(err, len(png))
	}
	img := m.Render(64)
	painted := 0
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] > 0 {
			painted++
		}
	}
	if painted < 500 {
		t.Fatal("render looks empty", painted)
	}
	if s := m.sample(5); s.Count() != 5 {
		t.Fatal("sample", s.Count())
	}
	if mm := Merge([]*Mesh{m, x}, 5); mm.Size()[0] != 45 {
		t.Fatal("merge", mm.Size())
	}
}
