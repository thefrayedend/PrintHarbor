package threemf

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

func binarySTL(tris [][3][3]float32, header string) []byte {
	var buf bytes.Buffer
	h := make([]byte, 80)
	copy(h, header)
	buf.Write(h)
	binary.Write(&buf, binary.LittleEndian, uint32(len(tris)))
	for _, t := range tris {
		buf.Write(make([]byte, 12)) // normal
		for _, v := range t {
			for _, c := range v {
				binary.Write(&buf, binary.LittleEndian, math.Float32bits(c))
			}
		}
		buf.Write([]byte{0, 0})
	}
	return buf.Bytes()
}

func TestBinarySTL(t *testing.T) {
	tris := [][3][3]float32{{{-10, 0, 0}, {30, 5, 0}, {0, 20, 12.5}}, {{0, 0, 0}, {1, 1, 1}, {2, -4, 3}}}
	// Header starting with "solid" must still be read as binary.
	b := binarySTL(tris, "solid lies about being ASCII")
	p, err := ReadMesh("Bracket.STL", bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Bracket" || p.SizeX != 40 || p.SizeY != 24 || p.SizeZ != 12.5 {
		t.Fatalf("%+v", p)
	}
}

func TestASCIISTLAndOBJ(t *testing.T) {
	stl := "solid x\nfacet normal 0 0 1\nouter loop\nvertex 0 0 0\nvertex 10 0 0\nvertex 0 20 5\nendloop\nendfacet\nendsolid x\n"
	p, err := ReadMesh("a.stl", strings.NewReader(stl), int64(len(stl)))
	if err != nil || p.SizeX != 10 || p.SizeY != 20 || p.SizeZ != 5 {
		t.Fatal(p, err)
	}
	obj := "# cube\nv 0 0 0\nv 2 3 4\nvn 0 0 1\nf 1 2 1\n"
	p, err = ReadMesh("b.obj", strings.NewReader(obj), int64(len(obj)))
	if err != nil || p.SizeZ != 4 {
		t.Fatal(p, err)
	}
	if _, err := ReadMesh("c.stl", strings.NewReader("garbage"), 7); err == nil {
		t.Fatal("garbage should fail")
	}
	proj := ProjectFromMeshes([]Part{p, p})
	if len(proj.Plates) != 1 || len(proj.Plates[0].Parts) != 2 || proj.Plates[0].Parts[1].ObjectID != 2 || len(proj.Filaments) != 1 {
		t.Fatalf("%+v", proj)
	}
}
