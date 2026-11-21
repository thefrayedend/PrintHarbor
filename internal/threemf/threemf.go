// Package threemf reads the parts of a 3MF project that PrintHarbor needs to
// plan a print: plates, the parts on each plate and their sizes, the
// project's filaments, and whether color painting is used.
//
// It understands Orca/Bambu Studio project files (plates in
// Metadata/model_settings.config, meshes in 3D/Objects/*.model) and plain
// 3MF files (meshes inline, everything on one plate). It never modifies the
// file.
package threemf

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
)

type Project struct {
	Plates        []Plate    `json:"plates"`
	Filaments     []Filament `json:"filaments"`
	SourcePrinter string     `json:"source_printer,omitempty"`
	SourceProcess string     `json:"source_process,omitempty"`
}

type Filament struct {
	Index int    `json:"index"` // 1-based, as Orca numbers them
	Color string `json:"color"`
	Type  string `json:"type"`
	Name  string `json:"name,omitempty"`
}

type Plate struct {
	Index        int     `json:"index"` // 1-based, as Orca's --slice expects
	Name         string  `json:"name"`
	Parts        []Part  `json:"parts"`
	Filaments    []int   `json:"filaments"` // project filament indices used
	Painted      bool    `json:"painted"`   // color painting present: may use any project filament
	Thumbnail    string  `json:"thumbnail,omitempty"`
	MaxX         float64 `json:"max_x_mm"`
	MaxY         float64 `json:"max_y_mm"`
	MaxZ         float64 `json:"max_z_mm"`
	FootprintMM2 float64 `json:"footprint_mm2"`
}

type Part struct {
	ObjectID int     `json:"object_id"`
	Instance int     `json:"instance"`
	Name     string  `json:"name"`
	SizeX    float64 `json:"size_x_mm"`
	SizeY    float64 `json:"size_y_mm"`
	SizeZ    float64 `json:"size_z_mm"`
}

// --- internal model ---

type mat [12]float64 // 3MF row-major affine: x' = x*m0 + y*m3 + z*m6 + m9 ...

var identity = mat{1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0}

func parseMat(s string) mat {
	f := strings.Fields(s)
	if len(f) != 12 {
		return identity
	}
	var m mat
	for i, v := range f {
		m[i], _ = strconv.ParseFloat(v, 64)
	}
	return m
}

// mul returns a then b (apply a first).
func mul(a, b mat) mat {
	var r mat
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

func (m mat) apply(x, y, z float64) (float64, float64, float64) {
	return x*m[0] + y*m[3] + z*m[6] + m[9],
		x*m[1] + y*m[4] + z*m[7] + m[10],
		x*m[2] + y*m[5] + z*m[8] + m[11]
}

type component struct {
	path     string
	objectID int
	t        mat
}

type object struct {
	verts      []float32
	painted    bool
	components []component
}

type buildItem struct {
	objectID int
	t        mat
}

type reader struct {
	zr      *zip.Reader
	files   map[string]*zip.File
	objects map[string]*object // key: path + "#" + id
}

// Read parses a 3MF from r (size bytes long).
func Read(r io.ReaderAt, size int64) (*Project, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("not a 3MF (zip) file: %w", err)
	}
	rd := &reader{zr: zr, files: map[string]*zip.File{}, objects: map[string]*object{}}
	for _, f := range zr.File {
		rd.files[strings.TrimPrefix(f.Name, "/")] = f
	}
	root := "3D/3dmodel.model"
	if _, ok := rd.files[root]; !ok {
		for name := range rd.files {
			if strings.HasPrefix(name, "3D/") && strings.HasSuffix(strings.ToLower(name), ".model") {
				root = name
				break
			}
		}
	}
	items, err := rd.parseModel(root)
	if err != nil {
		return nil, err
	}

	proj := &Project{Plates: []Plate{}, Filaments: []Filament{}}
	rd.readProjectSettings(proj)
	cfg := rd.readModelSettings()

	// Per object instance: size and name.
	type key struct{ obj, inst int }
	parts := map[key]Part{}
	instCount := map[int]int{}
	var order []key
	for _, it := range items {
		inst := instCount[it.objectID]
		instCount[it.objectID]++
		minx, miny, minz := math.Inf(1), math.Inf(1), math.Inf(1)
		maxx, maxy, maxz := math.Inf(-1), math.Inf(-1), math.Inf(-1)
		rd.walk(root, it.objectID, it.t, 0, func(x, y, z float64) {
			minx, maxx = math.Min(minx, x), math.Max(maxx, x)
			miny, maxy = math.Min(miny, y), math.Max(maxy, y)
			minz, maxz = math.Min(minz, z), math.Max(maxz, z)
		})
		p := Part{ObjectID: it.objectID, Instance: inst, Name: cfg.objName[it.objectID]}
		if !math.IsInf(minx, 1) {
			p.SizeX, p.SizeY, p.SizeZ = round(maxx-minx), round(maxy-miny), round(maxz-minz)
		}
		if p.Name == "" {
			p.Name = fmt.Sprintf("Object %d", it.objectID)
		}
		k := key{it.objectID, inst}
		parts[k] = p
		order = append(order, k)
	}

	painted := func(objID int) bool { return rd.isPainted(root, objID, 0) }

	addPlate := func(idx int, name string, keys []key) {
		pl := Plate{Index: idx, Name: name, Parts: []Part{}, Filaments: []int{}}
		used := map[int]bool{}
		for _, k := range keys {
			p, ok := parts[k]
			if !ok {
				continue
			}
			pl.Parts = append(pl.Parts, p)
			pl.MaxX = math.Max(pl.MaxX, p.SizeX)
			pl.MaxY = math.Max(pl.MaxY, p.SizeY)
			pl.MaxZ = math.Max(pl.MaxZ, p.SizeZ)
			pl.FootprintMM2 += p.SizeX * p.SizeY
			ex := cfg.objExtruders[k.obj]
			if len(ex) == 0 {
				ex = []int{1}
			}
			for _, e := range ex {
				used[e] = true
			}
			if painted(k.obj) {
				pl.Painted = true
			}
		}
		for e := range used {
			pl.Filaments = append(pl.Filaments, e)
		}
		sort.Ints(pl.Filaments)
		pl.FootprintMM2 = round(pl.FootprintMM2)
		thumb := fmt.Sprintf("Metadata/plate_%d.png", idx)
		if _, ok := rd.files[thumb]; ok {
			pl.Thumbnail = thumb
		}
		proj.Plates = append(proj.Plates, pl)
	}

	if len(cfg.plates) == 0 {
		addPlate(1, "Plate 1", order)
	} else {
		for _, cp := range cfg.plates {
			var keys []key
			for _, mi := range cp.instances {
				keys = append(keys, key{mi[0], mi[1]})
			}
			addPlate(cp.id, cp.name, keys)
		}
	}
	sort.Slice(proj.Plates, func(i, j int) bool { return proj.Plates[i].Index < proj.Plates[j].Index })

	// Make sure every filament a plate refers to exists in the list.
	maxF := 0
	for _, pl := range proj.Plates {
		for _, f := range pl.Filaments {
			if f > maxF {
				maxF = f
			}
		}
	}
	for len(proj.Filaments) < maxF {
		proj.Filaments = append(proj.Filaments, Filament{Index: len(proj.Filaments) + 1, Type: "PLA"})
	}
	return proj, nil
}

func round(v float64) float64 { return math.Round(v*100) / 100 }

func (rd *reader) open(name string) (io.ReadCloser, error) {
	f, ok := rd.files[strings.TrimPrefix(name, "/")]
	if !ok {
		return nil, fmt.Errorf("missing %s", name)
	}
	return f.Open()
}

// parseModel loads every object in a .model file and returns its build items.
func (rd *reader) parseModel(name string) ([]buildItem, error) {
	name = strings.TrimPrefix(name, "/")
	rc, err := rd.open(name)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	var items []buildItem
	var cur *object
	for {
		tk, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		se, ok := tk.(xml.StartElement)
		if !ok {
			if ee, ok := tk.(xml.EndElement); ok && ee.Name.Local == "object" {
				cur = nil
			}
			continue
		}
		switch se.Name.Local {
		case "object":
			cur = &object{}
			rd.objects[name+"#"+attr(se, "id")] = cur
		case "vertex":
			if cur != nil {
				x, _ := strconv.ParseFloat(attr(se, "x"), 32)
				y, _ := strconv.ParseFloat(attr(se, "y"), 32)
				z, _ := strconv.ParseFloat(attr(se, "z"), 32)
				cur.verts = append(cur.verts, float32(x), float32(y), float32(z))
			}
		case "triangle":
			if cur != nil && !cur.painted {
				for _, a := range se.Attr {
					if (a.Name.Local == "paint_color" || a.Name.Local == "mmu_segmentation") && a.Value != "" {
						cur.painted = true
					}
				}
			}
		case "component":
			if cur != nil {
				id, _ := strconv.Atoi(attr(se, "objectid"))
				p := attr(se, "path")
				if p == "" {
					p = name
				}
				cur.components = append(cur.components, component{path: strings.TrimPrefix(p, "/"), objectID: id, t: parseMat(attr(se, "transform"))})
			}
		case "item":
			id, _ := strconv.Atoi(attr(se, "objectid"))
			if attr(se, "printable") == "0" {
				continue
			}
			items = append(items, buildItem{objectID: id, t: parseMat(attr(se, "transform"))})
		}
	}
	return items, nil
}

func (rd *reader) getObject(file string, id int) *object {
	k := file + "#" + strconv.Itoa(id)
	if o, ok := rd.objects[k]; ok {
		return o
	}
	if _, ok := rd.files[file]; ok {
		if _, err := rd.parseModel(file); err == nil {
			return rd.objects[k]
		}
	}
	return nil
}

func (rd *reader) walk(file string, id int, t mat, depth int, fn func(x, y, z float64)) {
	if depth > 16 {
		return
	}
	o := rd.getObject(file, id)
	if o == nil {
		return
	}
	for i := 0; i+2 < len(o.verts); i += 3 {
		fn(t.apply(float64(o.verts[i]), float64(o.verts[i+1]), float64(o.verts[i+2])))
	}
	for _, c := range o.components {
		rd.walk(c.path, c.objectID, mul(c.t, t), depth+1, fn)
	}
}

func (rd *reader) isPainted(file string, id int, depth int) bool {
	o := rd.getObject(file, id)
	if o == nil || depth > 16 {
		return false
	}
	if o.painted {
		return true
	}
	for _, c := range o.components {
		if rd.isPainted(c.path, c.objectID, depth+1) {
			return true
		}
	}
	return false
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// --- Metadata/project_settings.config (JSON) ---

func (rd *reader) readProjectSettings(p *Project) {
	rc, err := rd.open("Metadata/project_settings.config")
	if err != nil {
		return
	}
	defer rc.Close()
	var m map[string]any
	if json.NewDecoder(rc).Decode(&m) != nil {
		return
	}
	colors := list(m["filament_colour"])
	types := list(m["filament_type"])
	names := list(m["filament_settings_id"])
	n := len(colors)
	if len(types) > n {
		n = len(types)
	}
	for i := 0; i < n; i++ {
		f := Filament{Index: i + 1}
		if i < len(colors) {
			f.Color = colors[i]
		}
		if i < len(types) {
			f.Type = types[i]
		}
		if i < len(names) {
			f.Name = names[i]
		}
		p.Filaments = append(p.Filaments, f)
	}
	p.SourcePrinter = first(m["printer_settings_id"])
	p.SourceProcess = first(m["print_settings_id"])
}

func list(v any) []string {
	switch t := v.(type) {
	case []any:
		var out []string
		for _, e := range t {
			out = append(out, fmt.Sprint(e))
		}
		return out
	case string:
		return []string{t}
	}
	return nil
}

func first(v any) string {
	if l := list(v); len(l) > 0 {
		return l[0]
	}
	return ""
}

// --- Metadata/model_settings.config (XML) ---

type cfgPlate struct {
	id        int
	name      string
	instances [][2]int // object id, instance id
}

type modelCfg struct {
	objName      map[int]string
	objExtruders map[int][]int
	plates       []cfgPlate
}

type xmlMeta struct {
	Key   string `xml:"key,attr"`
	Value string `xml:"value,attr"`
}

type xmlConfig struct {
	Objects []struct {
		ID    int       `xml:"id,attr"`
		Meta  []xmlMeta `xml:"metadata"`
		Parts []struct {
			Meta []xmlMeta `xml:"metadata"`
		} `xml:"part"`
	} `xml:"object"`
	Plates []struct {
		Meta      []xmlMeta `xml:"metadata"`
		Instances []struct {
			Meta []xmlMeta `xml:"metadata"`
		} `xml:"model_instance"`
	} `xml:"plate"`
}

func meta(ms []xmlMeta, key string) string {
	for _, m := range ms {
		if m.Key == key {
			return m.Value
		}
	}
	return ""
}

func (rd *reader) readModelSettings() modelCfg {
	c := modelCfg{objName: map[int]string{}, objExtruders: map[int][]int{}}
	rc, err := rd.open("Metadata/model_settings.config")
	if err != nil {
		return c
	}
	defer rc.Close()
	var x xmlConfig
	if xml.NewDecoder(rc).Decode(&x) != nil {
		return c
	}
	for _, o := range x.Objects {
		c.objName[o.ID] = meta(o.Meta, "name")
		seen := map[int]bool{}
		add := func(s string) {
			if e, err := strconv.Atoi(s); err == nil && e > 0 && !seen[e] {
				seen[e] = true
				c.objExtruders[o.ID] = append(c.objExtruders[o.ID], e)
			}
		}
		objEx := meta(o.Meta, "extruder")
		add(objEx)
		for _, p := range o.Parts {
			if pe := meta(p.Meta, "extruder"); pe != "" {
				add(pe)
			}
		}
		if objEx == "" && len(c.objExtruders[o.ID]) == 0 {
			add("1")
		}
	}
	for _, p := range x.Plates {
		id, _ := strconv.Atoi(meta(p.Meta, "plater_id"))
		cp := cfgPlate{id: id, name: meta(p.Meta, "plater_name")}
		if cp.name == "" {
			cp.name = fmt.Sprintf("Plate %d", id)
		}
		for _, mi := range p.Instances {
			oid, _ := strconv.Atoi(meta(mi.Meta, "object_id"))
			iid, _ := strconv.Atoi(meta(mi.Meta, "instance_id"))
			cp.instances = append(cp.instances, [2]int{oid, iid})
		}
		c.plates = append(c.plates, cp)
	}
	return c
}

// Thumbnail returns the bytes of a file inside the 3MF (used for plate images).
func Thumbnail(r io.ReaderAt, size int64, name string) ([]byte, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	name = path.Clean(strings.TrimPrefix(name, "/"))
	if !strings.HasPrefix(name, "Metadata/") || !strings.HasSuffix(name, ".png") {
		return nil, fmt.Errorf("not a thumbnail")
	}
	for _, f := range zr.File {
		if strings.TrimPrefix(f.Name, "/") == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 8<<20))
		}
	}
	return nil, fmt.Errorf("not found")
}
