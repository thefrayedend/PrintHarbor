package threemf

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// MeshFormats are the plain mesh files PrintHarbor can slice besides 3MF.
var MeshFormats = map[string]bool{".stl": true, ".obj": true}

// ReadMesh measures an STL (binary or ASCII) or OBJ file and returns it as a
// part. Meshes carry no plates or colors: the caller puts them on one plate.
func ReadMesh(name string, r io.ReaderAt, size int64) (Part, error) {
	ext := strings.ToLower(filepath.Ext(name))
	p := Part{Name: strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))}
	b := newBox()
	var err error
	switch ext {
	case ".stl":
		err = readSTL(io.NewSectionReader(r, 0, size), size, b)
	case ".obj":
		err = readOBJ(io.NewSectionReader(r, 0, size), b)
	default:
		return p, fmt.Errorf("%s: unsupported file type", name)
	}
	if err != nil {
		return p, fmt.Errorf("%s: %w", name, err)
	}
	if !b.ok() {
		return p, fmt.Errorf("%s: no geometry found", name)
	}
	p.SizeX, p.SizeY, p.SizeZ = round(b.maxx-b.minx), round(b.maxy-b.miny), round(b.maxz-b.minz)
	return p, nil
}

// ProjectFromMeshes builds a one-plate, one-filament project from parts.
func ProjectFromMeshes(parts []Part) *Project {
	pl := Plate{Index: 1, Name: "Plate 1", Parts: []Part{}, Filaments: []int{1}}
	for i, p := range parts {
		p.ObjectID = i + 1
		pl.Parts = append(pl.Parts, p)
		pl.MaxX = math.Max(pl.MaxX, p.SizeX)
		pl.MaxY = math.Max(pl.MaxY, p.SizeY)
		pl.MaxZ = math.Max(pl.MaxZ, p.SizeZ)
		pl.FootprintMM2 += p.SizeX * p.SizeY
	}
	pl.FootprintMM2 = round(pl.FootprintMM2)
	return &Project{Plates: []Plate{pl}, Filaments: []Filament{{Index: 1, Type: "PLA"}}}
}

type bbox struct{ minx, miny, minz, maxx, maxy, maxz float64 }

func newBox() *bbox {
	i, n := math.Inf(1), math.Inf(-1)
	return &bbox{i, i, i, n, n, n}
}

func (b *bbox) add(x, y, z float64) {
	if math.IsNaN(x) || math.IsNaN(y) || math.IsNaN(z) || math.IsInf(x, 0) || math.IsInf(y, 0) || math.IsInf(z, 0) {
		return
	}
	b.minx, b.maxx = math.Min(b.minx, x), math.Max(b.maxx, x)
	b.miny, b.maxy = math.Min(b.miny, y), math.Max(b.maxy, y)
	b.minz, b.maxz = math.Min(b.minz, z), math.Max(b.maxz, z)
}

func (b *bbox) ok() bool { return !math.IsInf(b.minx, 1) }

func readSTL(r io.Reader, size int64, b *bbox) error {
	br := bufio.NewReaderSize(r, 1<<16)
	head, _ := br.Peek(84)
	if len(head) == 84 {
		n := int64(binary.LittleEndian.Uint32(head[80:84]))
		// Binary STL: exact size match. (Some binary files start with "solid",
		// so the size check decides, not the header text.)
		if 84+50*n == size {
			if _, err := br.Discard(84); err != nil {
				return err
			}
			tri := make([]byte, 50)
			for i := int64(0); i < n; i++ {
				if _, err := io.ReadFull(br, tri); err != nil {
					return err
				}
				for v := 0; v < 3; v++ {
					o := 12 + v*12
					b.add(f32(tri[o:]), f32(tri[o+4:]), f32(tri[o+8:]))
				}
			}
			return nil
		}
	}
	if !bytes.HasPrefix(bytes.TrimSpace(head), []byte("solid")) {
		return fmt.Errorf("not a valid STL file")
	}
	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 4 && f[0] == "vertex" {
			b.add(num(f[1]), num(f[2]), num(f[3]))
		}
	}
	return sc.Err()
}

func readOBJ(r io.Reader, b *bbox) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 4 && f[0] == "v" {
			b.add(num(f[1]), num(f[2]), num(f[3]))
		}
	}
	return sc.Err()
}

func f32(b []byte) float64 { return float64(math.Float32frombits(binary.LittleEndian.Uint32(b))) }

func num(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return v
}
