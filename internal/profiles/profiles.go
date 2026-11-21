// Package profiles loads OrcaSlicer presets (system and user), resolves their
// `inherits` chains into flat settings, and works out which filament and
// process presets are compatible with a given printer.
//
// Orca's layout (resources/profiles):
//
//	<Vendor>.json                      vendor index
//	<Vendor>/machine/*.json            "type": "machine" or "machine_model"
//	<Vendor>/process/*.json            "type": "process"
//	<Vendor>/filament/**/*.json        "type": "filament"
//
// Presets with "instantiation": "true" are the ones users pick. Others are
// abstract bases that only exist to be inherited from.
package profiles

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Preset struct {
	Name          string         `json:"name"`
	Type          string         `json:"type"`
	Vendor        string         `json:"vendor"`
	Inherits      string         `json:"inherits,omitempty"`
	Instantiation bool           `json:"instantiation"`
	Source        string         `json:"source"` // system | orca-user | harbor
	File          string         `json:"-"`
	Raw           map[string]any `json:"-"`
}

type Library struct {
	byType map[string]map[string]*Preset // type -> name -> preset
	// vendorByName lets inherits prefer the same vendor when names collide.
	byVendor map[string]map[string]*Preset // vendor+"\x00"+type -> name -> preset
	Errors   []string
	onAdd    func(raw map[string]any, vendor, path string) // LoadDirCached records what it reads
}

func NewLibrary() *Library {
	return &Library{byType: map[string]map[string]*Preset{}, byVendor: map[string]map[string]*Preset{}}
}

// LoadDir walks dir and adds every preset JSON it finds. The first path
// element under dir is taken as the vendor name.
func (l *Library) LoadDir(dir, source string) error {
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			l.Errors = append(l.Errors, err.Error())
			return nil
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".json") {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 2 { // vendor index files live at the top level
			return nil
		}
		vendor := parts[0]
		b, err := os.ReadFile(path)
		if err != nil {
			l.Errors = append(l.Errors, err.Error())
			return nil
		}
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			l.Errors = append(l.Errors, fmt.Sprintf("%s: %v", rel, err))
			return nil
		}
		if l.onAdd != nil {
			l.onAdd(raw, vendor, path)
		}
		l.Add(raw, vendor, source, path)
		return nil
	})
}

// Add registers one preset from its raw JSON.
func (l *Library) Add(raw map[string]any, vendor, source, file string) *Preset {
	p := &Preset{
		Name:     str(raw["name"]),
		Type:     str(raw["type"]),
		Inherits: str(raw["inherits"]),
		Vendor:   vendor,
		Source:   source,
		File:     file,
		Raw:      raw,
	}
	if p.Name == "" || p.Type == "" {
		return nil
	}
	inst := str(raw["instantiation"])
	// User presets usually omit instantiation; they are always selectable.
	p.Instantiation = inst == "true" || (inst == "" && source != "system")
	if l.byType[p.Type] == nil {
		l.byType[p.Type] = map[string]*Preset{}
	}
	l.byType[p.Type][p.Name] = p
	k := vendor + "\x00" + p.Type
	if l.byVendor[k] == nil {
		l.byVendor[k] = map[string]*Preset{}
	}
	l.byVendor[k][p.Name] = p
	return p
}

func (l *Library) Get(typ, name string) *Preset { return l.byType[typ][name] }

func (l *Library) lookupParent(p *Preset) *Preset {
	if p.Inherits == "" {
		return nil
	}
	if v := l.byVendor[p.Vendor+"\x00"+p.Type][p.Inherits]; v != nil && v != p {
		return v
	}
	if v := l.byType[p.Type][p.Inherits]; v != nil && v != p {
		return v
	}
	return nil
}

// OwnKeys lists the settings that you, not the vendor, gave a preset: every
// key set by the preset itself or one of its parents that is not a system
// (stock) preset. For a stock preset the list is empty.
func (l *Library) OwnKeys(typ, name string) map[string]bool {
	out := map[string]bool{}
	seen := map[*Preset]bool{}
	for cur := l.Get(typ, name); cur != nil && !seen[cur]; cur = l.lookupParent(cur) {
		seen[cur] = true
		if cur.Source == "system" {
			continue
		}
		for k := range cur.Raw {
			out[k] = true
		}
	}
	return out
}

// Resolve flattens the inherits chain of a preset. Child values win.
// The returned map has no "inherits" key and is safe to hand to Orca.
func (l *Library) Resolve(typ, name string) (map[string]any, error) {
	p := l.Get(typ, name)
	if p == nil {
		return nil, fmt.Errorf("%s preset %q not found", typ, name)
	}
	var chain []*Preset
	seen := map[*Preset]bool{}
	for cur := p; cur != nil; cur = l.lookupParent(cur) {
		if seen[cur] {
			return nil, fmt.Errorf("inherits loop at %q", cur.Name)
		}
		seen[cur] = true
		chain = append(chain, cur)
		if cur.Inherits != "" && l.lookupParent(cur) == nil {
			return nil, fmt.Errorf("%q inherits missing preset %q", cur.Name, cur.Inherits)
		}
	}
	out := map[string]any{}
	for i := len(chain) - 1; i >= 0; i-- {
		for k, v := range chain[i].Raw {
			out[k] = v
		}
	}
	delete(out, "inherits")
	out["name"] = p.Name
	out["type"] = p.Type
	out["from"] = "User"
	if p.Source == "system" {
		// Marked as the system preset it is, so Orca's command line can check
		// the quality preset fits the printer (compatible_printers lists
		// system printer names) without looking for parent files.
		out["from"] = "system"
	}
	out["instantiation"] = "true"
	return out, nil
}

// List returns selectable presets of a type, sorted by name.
func (l *Library) List(typ string) []*Preset {
	var out []*Preset
	for _, p := range l.byType[typ] {
		if p.Instantiation {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MachineInfo is the printer data the rest of PrintHarbor cares about,
// pulled out of a resolved machine preset.
type MachineInfo struct {
	Preset     string       `json:"preset"`
	Vendor     string       `json:"vendor"`
	Model      string       `json:"model"`
	Nozzle     float64      `json:"nozzle_mm"`
	Extruders  int          `json:"extruders"`
	BedX       float64      `json:"bed_x_mm"`
	BedY       float64      `json:"bed_y_mm"`
	MaxZ       float64      `json:"max_z_mm"`
	BedPolygon [][2]float64 `json:"bed_polygon,omitempty"`
}

func (l *Library) Machine(name string) (MachineInfo, error) {
	r, err := l.Resolve("machine", name)
	if err != nil {
		return MachineInfo{}, err
	}
	p := l.Get("machine", name)
	mi := MachineInfo{Preset: name, Vendor: p.Vendor, Model: str(r["printer_model"])}
	noz := strs(r["nozzle_diameter"])
	mi.Extruders = len(noz)
	if len(noz) > 0 {
		mi.Nozzle, _ = strconv.ParseFloat(noz[0], 64)
	}
	mi.MaxZ, _ = strconv.ParseFloat(first(r["printable_height"]), 64)
	minX, minY, maxX, maxY := 0.0, 0.0, 0.0, 0.0
	for i, pt := range strs(r["printable_area"]) {
		xy := strings.SplitN(pt, "x", 2)
		if len(xy) != 2 {
			continue
		}
		x, _ := strconv.ParseFloat(xy[0], 64)
		y, _ := strconv.ParseFloat(xy[1], 64)
		mi.BedPolygon = append(mi.BedPolygon, [2]float64{x, y})
		if i == 0 || x < minX {
			minX = x
		}
		if i == 0 || y < minY {
			minY = y
		}
		if i == 0 || x > maxX {
			maxX = x
		}
		if i == 0 || y > maxY {
			maxY = y
		}
	}
	mi.BedX, mi.BedY = maxX-minX, maxY-minY
	return mi, nil
}

// Compatible reports whether a filament or process preset may be used with
// the given machine preset, following Orca's rules: an explicit
// compatible_printers list wins; otherwise compatible_printers_condition is
// evaluated against the machine's settings. ok=false with a non-nil error
// means the condition could not be understood (callers should allow it but
// flag it).
func (l *Library) Compatible(machine string, typ, name string) (bool, error) {
	pr, err := l.Resolve(typ, name)
	if err != nil {
		return false, err
	}
	// Like Orca: a vendor's own (system) presets only go with that vendor's
	// printers. Orca's shared filament library goes with every printer.
	if p, m := l.Get(typ, name), l.Get("machine", machine); p != nil && m != nil && p.Source == "system" && m.Source == "system" &&
		p.Vendor != m.Vendor && p.Vendor != "OrcaFilamentLibrary" {
		return false, nil
	}
	if list := strs(pr["compatible_printers"]); len(list) > 0 {
		for _, m := range list {
			if m == machine {
				return true, nil
			}
		}
		return false, nil
	}
	cond := strings.TrimSpace(first(pr["compatible_printers_condition"]))
	if cond == "" {
		return true, nil
	}
	mr, err := l.Resolve("machine", machine)
	if err != nil {
		return false, err
	}
	ok, err := EvalCondition(cond, mr)
	if err != nil {
		return true, err
	}
	return ok, nil
}

// CompatibleList returns the selectable presets of typ usable with machine.
func (l *Library) CompatibleList(machine, typ string) []*Preset {
	var out []*Preset
	for _, p := range l.List(typ) {
		if ok, _ := l.Compatible(machine, typ, p.Name); ok {
			out = append(out, p)
		}
	}
	return out
}

// Vendors groups selectable machine presets by vendor and printer model, for
// a Brand -> Printer -> Nozzle picker.
type VendorTree map[string]map[string][]string

func (l *Library) Vendors() VendorTree {
	t := VendorTree{}
	for _, p := range l.List("machine") {
		r, err := l.Resolve("machine", p.Name)
		if err != nil {
			continue
		}
		model := str(r["printer_model"])
		if model == "" {
			model = p.Name
		}
		if t[p.Vendor] == nil {
			t[p.Vendor] = map[string][]string{}
		}
		t[p.Vendor][model] = append(t[p.Vendor][model], p.Name)
	}
	for _, models := range t {
		for m := range models {
			sort.Strings(models[m])
		}
	}
	return t
}

// --- helpers for Orca's "everything is a string or list of strings" JSON ---

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		if len(t) > 0 {
			return str(t[0])
		}
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

func strs(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			out = append(out, str(e))
		}
		return out
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	}
	return nil
}

func first(v any) string { return str(v) }

// Str and Strs are exported for other packages that read resolved presets.
func Str(v any) string    { return str(v) }
func Strs(v any) []string { return strs(v) }
