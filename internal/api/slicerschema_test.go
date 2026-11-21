package api

import (
	"path/filepath"
	"testing"

	"github.com/thefrayedend/printharbor/internal/config"
)

// A process setting left at its profile default — set only in an inherited base,
// not written into the child preset — must still appear in the slicer schema.
// Otherwise the "All settings" list is missing options FlashBridge shows, because
// FlashBridge includes every scope=="process" setting while we derive the set
// from the resolved presets. Regression for the raw-vs-resolved preset fix.
func TestSlicerSchemaIncludesInheritedProcessSettings(t *testing.T) {
	dir := t.TempDir()
	prof := filepath.Join(dir, "profiles", "Acme")
	// Base (not instantiable) carries the defaults a child inherits.
	write(t, filepath.Join(prof, "process", "base.json"),
		`{"type":"process","name":"Acme Base","instantiation":"false","ironing_type":"no ironing","raft_layers":"0"}`)
	// Child overrides only layer_height; ironing_type / raft_layers come via inherit.
	write(t, filepath.Join(prof, "process", "std.json"),
		`{"type":"process","name":"0.20mm Standard @Acme","instantiation":"true","inherits":"Acme Base","layer_height":"0.2"}`)
	write(t, filepath.Join(prof, "machine", "m.json"),
		`{"type":"machine","name":"Acme 0.4","instantiation":"true","nozzle_diameter":["0.4"],"printable_area":["0x0","200x0","200x200","0x200"],"printable_height":"200"}`)
	write(t, filepath.Join(prof, "filament", "f.json"),
		`{"type":"filament","name":"Generic PLA @Acme","instantiation":"true","filament_type":["PLA"]}`)

	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.OrcaPath = "/none"
	cfg.OrcaProfilesDir = filepath.Join(dir, "profiles")
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, s := range a.slicerSchema() {
		if k, ok := s["key"].(string); ok {
			keys[k] = true
		}
	}
	// layer_height is set directly; ironing_type and raft_layers only via inherit —
	// all three must be offered.
	for _, want := range []string{"layer_height", "ironing_type", "raft_layers"} {
		if !keys[want] {
			t.Errorf("slicer schema is missing %q (inherited process settings must be included)", want)
		}
	}
	// G-code settings are never per-print overrides and must stay excluded.
	if keys["machine_start_gcode"] {
		t.Error("machine_start_gcode should be excluded from the slicer schema")
	}
}

// TestMaxFilamentsSupportsBigToolChangers guards the colour ceiling: big
// multi-AMS tool changers need well over 16 (e.g. 24 colours), so maxFilaments
// must stay generous. If you lower it, these printers break.
func TestMaxFilamentsSupportsBigToolChangers(t *testing.T) {
	if maxFilaments < 24 {
		t.Fatalf("maxFilaments is %d; must be at least 24 for multi-AMS tool changers", maxFilaments)
	}
}
