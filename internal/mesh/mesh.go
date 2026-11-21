// Package mesh loads triangle meshes from STL, OBJ and 3MF files, renders
// small preview images, and exports a compact binary form for the browser's
// 3D viewer. It never modifies the source files.
package mesh

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Mesh is a triangle soup: 9 float32 per triangle (x,y,z of 3 vertices).
type Mesh struct {
	Tris []float32
}

func (m *Mesh) Count() int { return len(m.Tris) / 9 }

// Bounds returns min and max corners.
func (m *Mesh) Bounds() (min, max [3]float32) {
	if len(m.Tris) == 0 {
		return
	}
	min = [3]float32{m.Tris[0], m.Tris[1], m.Tris[2]}
	max = min
	for i := 0; i+2 < len(m.Tris); i += 3 {
		for k := 0; k < 3; k++ {
			v := m.Tris[i+k]
			if v < min[k] {
				min[k] = v
			}
			if v > max[k] {
				max[k] = v
			}
		}
	}
	return
}

// Size returns the bounding box dimensions in mm.
func (m *Mesh) Size() [3]float64 {
	lo, hi := m.Bounds()
	return [3]float64{float64(hi[0] - lo[0]), float64(hi[1] - lo[1]), float64(hi[2] - lo[2])}
}

// Formats lists the extensions Load understands.
var Formats = map[string]bool{".stl": true, ".obj": true, ".3mf": true}

// Load reads a mesh file. maxTris > 0 limits memory on huge files by keeping
// an even sample of triangles (fine for previews).
func Load(path string, maxTris int) (*Mesh, error) {
	ext := strings.ToLower(filepath.Ext(path))
	var m *Mesh
	var err error
	switch ext {
	case ".stl":
		m, err = loadSTL(path)
	case ".obj":
		m, err = loadOBJ(path)
	case ".3mf":
		m, err = load3MF(path)
	default:
		return nil, fmt.Errorf("can't preview %s files", ext)
	}
	if err != nil {
		return nil, err
	}
	if m.Count() == 0 {
		return nil, fmt.Errorf("no triangles found")
	}
	if maxTris > 0 && m.Count() > maxTris {
		m = m.sample(maxTris)
	}
	return m, nil
}

func (m *Mesh) sample(n int) *Mesh {
	step := float64(m.Count()) / float64(n)
	out := &Mesh{Tris: make([]float32, 0, n*9)}
	for f := 0.0; int(f) < m.Count() && out.Count() < n; f += step {
		i := int(f) * 9
		out.Tris = append(out.Tris, m.Tris[i:i+9]...)
	}
	return out
}

func loadSTL(path string) (*Mesh, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, _ := f.Stat()
	br := bufio.NewReaderSize(f, 1<<20)
	head, _ := br.Peek(84)
	if len(head) == 84 {
		n := int64(binary.LittleEndian.Uint32(head[80:84]))
		if 84+50*n == fi.Size() {
			br.Discard(84)
			m := &Mesh{Tris: make([]float32, 0, n*9)}
			rec := make([]byte, 50)
			for i := int64(0); i < n; i++ {
				if _, err := io.ReadFull(br, rec); err != nil {
					return nil, err
				}
				for k := 0; k < 9; k++ {
					m.Tris = append(m.Tris, math.Float32frombits(binary.LittleEndian.Uint32(rec[12+4*k:])))
				}
			}
			return m, nil
		}
	}
	if !bytes.HasPrefix(bytes.TrimSpace(head), []byte("solid")) {
		return nil, fmt.Errorf("not a valid STL file")
	}
	m := &Mesh{}
	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		fl := strings.Fields(sc.Text())
		if len(fl) == 4 && fl[0] == "vertex" {
			for _, s := range fl[1:] {
				v, _ := strconv.ParseFloat(s, 32)
				m.Tris = append(m.Tris, float32(v))
			}
		}
	}
	m.Tris = m.Tris[:len(m.Tris)/9*9]
	return m, sc.Err()
}

func loadOBJ(path string) (*Mesh, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var verts []float32
	m := &Mesh{}
	idx := func(s string) int {
		if i := strings.IndexByte(s, '/'); i >= 0 {
			s = s[:i]
		}
		n, _ := strconv.Atoi(s)
		if n < 0 {
			return len(verts)/3 + n
		}
		return n - 1
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		fl := strings.Fields(sc.Text())
		if len(fl) == 0 {
			continue
		}
		switch fl[0] {
		case "v":
			if len(fl) >= 4 {
				for _, s := range fl[1:4] {
					v, _ := strconv.ParseFloat(s, 32)
					verts = append(verts, float32(v))
				}
			}
		case "f":
			if len(fl) < 4 {
				continue
			}
			a := idx(fl[1])
			for i := 2; i+1 < len(fl); i++ { // fan triangulation
				b, c := idx(fl[i]), idx(fl[i+1])
				ok := true
				for _, v := range []int{a, b, c} {
					if v < 0 || v*3+2 >= len(verts) {
						ok = false
					}
				}
				if ok {
					m.Tris = append(m.Tris, verts[a*3:a*3+3]...)
					m.Tris = append(m.Tris, verts[b*3:b*3+3]...)
					m.Tris = append(m.Tris, verts[c*3:c*3+3]...)
				}
			}
		}
	}
	return m, sc.Err()
}

// --- 3MF ---

type m3obj struct {
	verts []float32
	tris  []int32
	comps []m3comp
}

type m3comp struct {
	path string
	id   string
	t    [12]float64
}

var ident = [12]float64{1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0}

func parseT(s string) [12]float64 {
	f := strings.Fields(s)
	if len(f) != 12 {
		return ident
	}
	var t [12]float64
	for i, v := range f {
		t[i], _ = strconv.ParseFloat(v, 64)
	}
	return t
}

func mulT(a, b [12]float64) [12]float64 {
	var r [12]float64
	for row := 0; row < 4; row++ {
		for col := 0; col < 3; col++ {
			var s float64
			for k := 0; k < 3; k++ {
				s += a[row*3+k] * b[k*3+col]
			}
			if row == 3 {
				s += b[9+col]
			}
			r[row*3+col] = s
		}
	}
	return r
}

func load3MF(path string) (*Mesh, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("not a 3MF file: %w", err)
	}
	defer zr.Close()
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[strings.TrimPrefix(f.Name, "/")] = f
	}
	objs := map[string]*m3obj{} // path#id
	parsed := map[string]bool{}
	var parse func(name string) ([]m3comp, error)
	parse = func(name string) ([]m3comp, error) {
		parsed[name] = true
		f, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("missing %s", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		dec := xml.NewDecoder(bufio.NewReaderSize(rc, 1<<20))
		var items []m3comp
		var cur *m3obj
		attr := func(se xml.StartElement, k string) string {
			for _, a := range se.Attr {
				if a.Name.Local == k {
					return a.Value
				}
			}
			return ""
		}
		for {
			tk, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			switch se := tk.(type) {
			case xml.StartElement:
				switch se.Name.Local {
				case "object":
					cur = &m3obj{}
					objs[name+"#"+attr(se, "id")] = cur
				case "vertex":
					if cur != nil {
						for _, k := range []string{"x", "y", "z"} {
							v, _ := strconv.ParseFloat(attr(se, k), 32)
							cur.verts = append(cur.verts, float32(v))
						}
					}
				case "triangle":
					if cur != nil {
						for _, k := range []string{"v1", "v2", "v3"} {
							v, _ := strconv.Atoi(attr(se, k))
							cur.tris = append(cur.tris, int32(v))
						}
					}
				case "component":
					if cur != nil {
						p := strings.TrimPrefix(attr(se, "path"), "/")
						if p == "" {
							p = name
						}
						cur.comps = append(cur.comps, m3comp{p, attr(se, "objectid"), parseT(attr(se, "transform"))})
					}
				case "item":
					if attr(se, "printable") != "0" {
						items = append(items, m3comp{name, attr(se, "objectid"), parseT(attr(se, "transform"))})
					}
				}
			case xml.EndElement:
				if se.Name.Local == "object" {
					cur = nil
				}
			}
		}
		return items, nil
	}
	root := "3D/3dmodel.model"
	if _, ok := files[root]; !ok {
		for n := range files {
			if strings.HasPrefix(n, "3D/") && strings.HasSuffix(strings.ToLower(n), ".model") {
				root = n
				break
			}
		}
	}
	items, err := parse(root)
	if err != nil {
		return nil, err
	}
	m := &Mesh{}
	var emit func(p, id string, t [12]float64, depth int)
	emit = func(p, id string, t [12]float64, depth int) {
		if depth > 16 {
			return
		}
		o := objs[p+"#"+id]
		if o == nil && !parsed[p] {
			parse(p)
			o = objs[p+"#"+id]
		}
		if o == nil {
			return
		}
		nv := int32(len(o.verts) / 3)
		for i := 0; i+2 < len(o.tris); i += 3 {
			for k := 0; k < 3; k++ {
				vi := o.tris[i+k]
				if vi < 0 || vi >= nv {
					continue
				}
				x, y, z := float64(o.verts[vi*3]), float64(o.verts[vi*3+1]), float64(o.verts[vi*3+2])
				m.Tris = append(m.Tris,
					float32(x*t[0]+y*t[3]+z*t[6]+t[9]),
					float32(x*t[1]+y*t[4]+z*t[7]+t[10]),
					float32(x*t[2]+y*t[5]+z*t[8]+t[11]))
			}
		}
		for _, c := range o.comps {
			emit(c.path, c.id, mulT(c.t, t), depth+1)
		}
	}
	for _, it := range items {
		emit(it.path, it.id, it.t, 0)
	}
	m.Tris = m.Tris[:len(m.Tris)/9*9]
	return m, nil
}

// Binary encodes the mesh for the browser viewer: a uint32 triangle count
// followed by float32 coordinates, centered on X/Y with the bottom at Z=0.
func (m *Mesh) Binary() []byte {
	lo, hi := m.Bounds()
	cx, cy := (lo[0]+hi[0])/2, (lo[1]+hi[1])/2
	buf := make([]byte, 4+len(m.Tris)*4)
	binary.LittleEndian.PutUint32(buf, uint32(m.Count()))
	for i, v := range m.Tris {
		switch i % 3 {
		case 0:
			v -= cx
		case 1:
			v -= cy
		case 2:
			v -= lo[2]
		}
		binary.LittleEndian.PutUint32(buf[4+i*4:], math.Float32bits(v))
	}
	return buf
}

// Merge combines meshes laid out side by side along X (for plate previews).
func Merge(ms []*Mesh, gap float32) *Mesh {
	out := &Mesh{}
	var x float32
	for _, m := range ms {
		lo, hi := m.Bounds()
		dx, dy, dz := x-lo[0], -(lo[1]+hi[1])/2, -lo[2]
		for i, v := range m.Tris {
			switch i % 3 {
			case 0:
				v += dx
			case 1:
				v += dy
			case 2:
				v += dz
			}
			out.Tris = append(out.Tris, v)
		}
		x += hi[0] - lo[0] + gap
	}
	return out
}
