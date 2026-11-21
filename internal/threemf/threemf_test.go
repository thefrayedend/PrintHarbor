package threemf

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// box returns an object with an axis-aligned box mesh of the given size.
func box(id int, x, y, z float64, painted bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<object id="%d" type="model"><mesh><vertices>`, id)
	for _, v := range [][3]float64{{0, 0, 0}, {x, 0, 0}, {x, y, 0}, {0, y, 0}, {0, 0, z}, {x, 0, z}, {x, y, z}, {0, y, z}} {
		fmt.Fprintf(&b, `<vertex x="%g" y="%g" z="%g"/>`, v[0], v[1], v[2])
	}
	b.WriteString(`</vertices><triangles>`)
	paint := ""
	if painted {
		paint = ` paint_color="4"`
	}
	fmt.Fprintf(&b, `<triangle v1="0" v2="1" v3="2"%s/><triangle v1="0" v2="2" v3="3"/>`, paint)
	b.WriteString(`</triangles></mesh></object>`)
	return b.String()
}

func build3mf(t *testing.T, files map[string]string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return bytes.NewReader(buf.Bytes())
}

func TestBambuStyleProject(t *testing.T) {
	objects := `<?xml version="1.0"?><model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"><resources>` +
		box(1, 100, 50, 20, false) + box(2, 10, 10, 250, true) + `</resources></model>`
	// Object 3 is a 2x scaled box (100x50x20 -> 200x100x40) rotated 90° about Z.
	root := `<?xml version="1.0"?><model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"
	  xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06"><resources>
	  <object id="3" type="model"><components><component p:path="/3D/Objects/object_1.model" objectid="1" transform="2 0 0 0 2 0 0 0 2 0 0 0"/></components></object>
	  <object id="4" type="model"><components><component p:path="/3D/Objects/object_1.model" objectid="2"/></components></object>
	  </resources><build>
	  <item objectid="3" transform="0 1 0 -1 0 0 0 0 1 128 128 0" printable="1"/>
	  <item objectid="4" transform="1 0 0 0 1 0 0 0 1 400 128 0" printable="1"/>
	  <item objectid="4" transform="1 0 0 0 1 0 0 0 1 420 128 0" printable="1"/>
	  </build></model>`
	settings := `<?xml version="1.0"?><config>
	  <object id="3"><metadata key="name" value="Big bracket"/><metadata key="extruder" value="1"/></object>
	  <object id="4"><metadata key="name" value="Tall pin"/><metadata key="extruder" value="2"/>
	     <part id="1"><metadata key="extruder" value="3"/></part></object>
	  <plate><metadata key="plater_id" value="1"/><metadata key="plater_name" value="Brackets"/>
	     <model_instance><metadata key="object_id" value="3"/><metadata key="instance_id" value="0"/></model_instance></plate>
	  <plate><metadata key="plater_id" value="2"/>
	     <model_instance><metadata key="object_id" value="4"/><metadata key="instance_id" value="0"/></model_instance>
	     <model_instance><metadata key="object_id" value="4"/><metadata key="instance_id" value="1"/></model_instance></plate>
	</config>`
	proj := `{"filament_colour":["#000000","#FFD700","#FFFFFF"],"filament_type":["PLA","PETG","PLA"],
	  "printer_settings_id":"Bambu Lab X1 Carbon 0.4 nozzle","print_settings_id":"0.20mm Standard @BBL X1C"}`
	r := build3mf(t, map[string]string{
		"3D/3dmodel.model":                 root,
		"3D/Objects/object_1.model":        objects,
		"Metadata/model_settings.config":   settings,
		"Metadata/project_settings.config": proj,
		"Metadata/plate_1.png":             "png",
	})
	p, err := Read(r, r.Size())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Plates) != 2 || len(p.Filaments) != 3 || p.SourcePrinter != "Bambu Lab X1 Carbon 0.4 nozzle" {
		t.Fatalf("bad project: %+v", p)
	}
	p1 := p.Plates[0]
	if p1.Name != "Brackets" || len(p1.Parts) != 1 || p1.Thumbnail != "Metadata/plate_1.png" {
		t.Fatalf("bad plate 1: %+v", p1)
	}
	part := p1.Parts[0]
	if part.SizeX != 100 || part.SizeY != 200 || part.SizeZ != 40 || part.Name != "Big bracket" {
		t.Fatalf("bad transform math: %+v", part)
	}
	p2 := p.Plates[1]
	if len(p2.Parts) != 2 || p2.MaxZ != 250 || !p2.Painted || p2.Painted == p1.Painted {
		t.Fatalf("bad plate 2: %+v", p2)
	}
	if fmt.Sprint(p2.Filaments) != "[2 3]" || fmt.Sprint(p1.Filaments) != "[1]" {
		t.Fatalf("bad filaments: %v %v", p1.Filaments, p2.Filaments)
	}
	if _, err := Thumbnail(r, r.Size(), "Metadata/plate_1.png"); err != nil {
		t.Fatal(err)
	}
	if _, err := Thumbnail(r, r.Size(), "3D/3dmodel.model"); err == nil {
		t.Fatal("must refuse non-thumbnails")
	}
}

func TestPlain3MF(t *testing.T) {
	root := `<?xml version="1.0"?><model><resources>` + box(1, 30, 40, 50, false) +
		`</resources><build><item objectid="1"/></build></model>`
	r := build3mf(t, map[string]string{"3D/3dmodel.model": root})
	p, err := Read(r, r.Size())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Plates) != 1 || p.Plates[0].MaxZ != 50 || len(p.Filaments) != 1 {
		t.Fatalf("%+v", p)
	}
}
