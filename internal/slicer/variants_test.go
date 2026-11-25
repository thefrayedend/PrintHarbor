package slicer

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func readMap(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// The Snapmaker U1 profiles that made OrcaSlicer 2.5.1 crash on Windows on
// every plate: after collapseVariants each extruder has one nozzle kind, the
// one the profile has as default, and every list has one value per extruder.
func TestCollapseVariantsSnapmakerU1(t *testing.T) {
	m := readMap(t, "testdata/u1/machine.json")
	p := readMap(t, "testdata/u1/process.json")
	f := readMap(t, "testdata/u1/filament_1.json")
	collapseVariants(m, p, []map[string]any{f})
	for _, k := range []string{"printer_extruder_id", "printer_extruder_variant", "extruder_variant_list"} {
		if _, ok := m[k]; ok {
			t.Errorf("machine still has %s", k)
		}
	}
	for _, k := range []string{"print_extruder_id", "print_extruder_variant"} {
		if _, ok := p[k]; ok {
			t.Errorf("process still has %s", k)
		}
	}
	if _, ok := f["filament_extruder_variant"]; ok {
		t.Error("filament still has filament_extruder_variant")
	}
	for k, v := range m {
		if l, ok := v.([]any); ok && len(l) == 8 {
			t.Errorf("machine %s still has 8 values", k)
		}
	}
	want := func(m map[string]any, k string, v ...any) {
		t.Helper()
		if !reflect.DeepEqual(m[k], v) {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	want(m, "retraction_length", "1.5", "1.5", "1.5", "1.5")
	want(m, "nozzle_type", "hardened_steel", "hardened_steel", "hardened_steel", "hardened_steel")
	want(p, "inner_wall_speed", "300", "300", "300", "300") // Standard, not High Flow (600)
	want(p, "outer_wall_speed", "200", "200", "200", "200")
	want(f, "filament_max_volumetric_speed", "10") // Direct Drive Standard
	// machine_max_* (2 values per extruder kind pair) and single values are untouched
	if l := m["machine_max_speed_x"].([]any); len(l) != 16 {
		t.Errorf("machine_max_speed_x changed: %v", l)
	}
}

// The kind is taken from default_nozzle_volume_type per extruder.
func TestCollapseVariantsHighFlow(t *testing.T) {
	m := readMap(t, "testdata/u1/machine.json")
	p := readMap(t, "testdata/u1/process.json")
	f := readMap(t, "testdata/u1/filament_1.json")
	m["default_nozzle_volume_type"] = []any{"High Flow", "High Flow", "High Flow", "High Flow"}
	collapseVariants(m, p, []map[string]any{f})
	if !reflect.DeepEqual(p["inner_wall_speed"], []any{"600", "600", "600", "600"}) {
		t.Errorf("inner_wall_speed = %v", p["inner_wall_speed"])
	}
	if !reflect.DeepEqual(f["filament_max_volumetric_speed"], []any{"18"}) {
		t.Errorf("filament_max_volumetric_speed = %v", f["filament_max_volumetric_speed"])
	}
}

// Profiles without nozzle kinds (almost all) are not touched.
func TestCollapseVariantsLeavesOthersAlone(t *testing.T) {
	m := map[string]any{"retraction_length": []any{"0.8", "0.8"}, "nozzle_diameter": []any{"0.4", "0.4"}}
	p := map[string]any{"inner_wall_speed": []any{"300", "600"}}
	m0, _ := json.Marshal(m)
	p0, _ := json.Marshal(p)
	collapseVariants(m, p, nil)
	m1, _ := json.Marshal(m)
	p1, _ := json.Marshal(p)
	if string(m0) != string(m1) || string(p0) != string(p1) {
		t.Error("a profile without nozzle kinds was changed")
	}
}

// Two-extruder printers (Bambu H2D/H2C) slice as they are; their profiles are
// not touched.
func TestCollapseVariantsLeavesTwoExtrudersAlone(t *testing.T) {
	m := map[string]any{
		"printer_extruder_id":        []any{"1", "1", "1", "2", "2", "2", "2"},
		"printer_extruder_variant":   []any{"Direct Drive Standard", "Direct Drive High Flow", "Direct Drive E3D High Flow", "Direct Drive Standard", "Direct Drive High Flow", "Direct Drive TPU High Flow", "Direct Drive E3D High Flow"},
		"default_nozzle_volume_type": []any{"Standard", "Standard"},
		"nozzle_volume":              []any{"130", "133", "133", "145", "148", "148", "148"},
	}
	before, _ := json.Marshal(m)
	collapseVariants(m, nil, nil)
	after, _ := json.Marshal(m)
	if string(before) != string(after) {
		t.Error("a two-extruder profile was changed")
	}
}

// Only Orca's per-nozzle-kind settings are shortened, never another list that
// happens to have the same length (a 4-corner bed on a 4-entry printer).
func TestCollapseVariantsOnlyVariantSettings(t *testing.T) {
	m := map[string]any{
		"printer_extruder_id":        []any{"1", "1", "2", "2"},
		"printer_extruder_variant":   []any{"A Standard", "A High Flow", "A Standard", "A High Flow"},
		"default_nozzle_volume_type": []any{"Standard", "Standard"},
		"retraction_length":          []any{"1", "2", "3", "4"},
		"printable_area":             []any{"0x0", "200x0", "200x200", "0x200"},
	}
	m["printer_extruder_id"] = []any{"1", "2", "3", "3"} // three extruders
	m["printer_extruder_variant"] = []any{"A Standard", "A Standard", "A Standard", "A High Flow"}
	m["default_nozzle_volume_type"] = []any{"Standard", "Standard", "High Flow"}
	collapseVariants(m, nil, nil)
	if !reflect.DeepEqual(m["retraction_length"], []any{"1", "2", "4"}) {
		t.Errorf("retraction_length = %v", m["retraction_length"])
	}
	if l := m["printable_area"].([]any); len(l) != 4 {
		t.Errorf("printable_area was cut: %v", l)
	}
}
