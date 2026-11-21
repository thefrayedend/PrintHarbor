package profiles

import (
	"os"
	"path/filepath"
	"testing"
)

func writeJSON(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture builds a tiny Orca-style profiles tree.
func fixture(t *testing.T) *Library {
	dir := t.TempDir()
	v := filepath.Join(dir, "Acme")
	writeJSON(t, filepath.Join(dir, "Acme.json"), `{"name":"Acme"}`)
	writeJSON(t, filepath.Join(v, "machine", "fdm_machine_common.json"), `{
	  "type":"machine","name":"fdm_machine_common","instantiation":"false",
	  "printable_height":"200","nozzle_diameter":["0.4"],"printer_notes":"PRINTER_VENDOR_ACME"}`)
	writeJSON(t, filepath.Join(v, "machine", "Acme Big 0.4 nozzle.json"), `{
	  "type":"machine","name":"Acme Big 0.4 nozzle","inherits":"fdm_machine_common","instantiation":"true",
	  "printer_model":"Acme Big","printable_area":["0x0","300x0","300x300","0x300"],"printable_height":"320"}`)
	writeJSON(t, filepath.Join(v, "machine", "Acme Mini 0.6 nozzle.json"), `{
	  "type":"machine","name":"Acme Mini 0.6 nozzle","inherits":"fdm_machine_common","instantiation":"true",
	  "printer_model":"Acme Mini","nozzle_diameter":["0.6"],"printable_area":["0x0","180x0","180x180","0x180"]}`)
	writeJSON(t, filepath.Join(v, "process", "base.json"), `{
	  "type":"process","name":"fdm_process_common","instantiation":"false","layer_height":"0.2","wall_loops":"2"}`)
	writeJSON(t, filepath.Join(v, "process", "fine.json"), `{
	  "type":"process","name":"0.12mm Fine @Acme","inherits":"fdm_process_common","instantiation":"true",
	  "layer_height":"0.12","compatible_printers_condition":"printer_notes=~/.*PRINTER_VENDOR_ACME.*/ and nozzle_diameter[0]==0.4"}`)
	writeJSON(t, filepath.Join(v, "filament", "pla", "pla.json"), `{
	  "type":"filament","name":"Acme PLA","instantiation":"true","filament_type":["PLA"],
	  "nozzle_temperature":["210"],"compatible_printers":["Acme Mini 0.6 nozzle"]}`)
	l := NewLibrary()
	if err := l.LoadDir(dir, "system"); err != nil {
		t.Fatal(err)
	}
	if len(l.Errors) > 0 {
		t.Fatal(l.Errors)
	}
	return l
}

func TestResolveInherits(t *testing.T) {
	l := fixture(t)
	r, err := l.Resolve("process", "0.12mm Fine @Acme")
	if err != nil {
		t.Fatal(err)
	}
	if r["layer_height"] != "0.12" || r["wall_loops"] != "2" {
		t.Fatalf("bad merge: %v", r)
	}
	if _, ok := r["inherits"]; ok {
		t.Fatal("inherits should be removed")
	}
}

func TestMachineInfo(t *testing.T) {
	l := fixture(t)
	m, err := l.Machine("Acme Big 0.4 nozzle")
	if err != nil {
		t.Fatal(err)
	}
	if m.BedX != 300 || m.BedY != 300 || m.MaxZ != 320 || m.Nozzle != 0.4 || m.Extruders != 1 {
		t.Fatalf("bad machine info: %+v", m)
	}
}

func TestCompatibility(t *testing.T) {
	l := fixture(t)
	if ok, err := l.Compatible("Acme Big 0.4 nozzle", "process", "0.12mm Fine @Acme"); !ok || err != nil {
		t.Fatal("fine should fit big 0.4", ok, err)
	}
	if ok, _ := l.Compatible("Acme Mini 0.6 nozzle", "process", "0.12mm Fine @Acme"); ok {
		t.Fatal("fine should not fit 0.6 nozzle")
	}
	if ok, _ := l.Compatible("Acme Big 0.4 nozzle", "filament", "Acme PLA"); ok {
		t.Fatal("PLA is listed only for Mini")
	}
	if got := len(l.CompatibleList("Acme Mini 0.6 nozzle", "filament")); got != 1 {
		t.Fatal("want 1 filament for mini, got", got)
	}
	if len(l.List("process")) != 1 {
		t.Fatal("abstract presets must not be listed")
	}
}

func TestVendors(t *testing.T) {
	l := fixture(t)
	tree := l.Vendors()
	if len(tree["Acme"]) != 2 || tree["Acme"]["Acme Big"][0] != "Acme Big 0.4 nozzle" {
		t.Fatalf("bad tree %v", tree)
	}
}

func TestConditions(t *testing.T) {
	vars := map[string]any{
		"printer_notes":   "PRINTER_VENDOR_X PRINTER_HAS_BOWDEN",
		"nozzle_diameter": []any{"0.4", "0.6"},
		"printer_model":   "X 1",
	}
	cases := map[string]bool{
		`nozzle_diameter[0]==0.4`:                          true,
		`nozzle_diameter[1] > 0.5 && printer_model=="X 1"`: true,
		`! (printer_notes=~/.*PRINTER_HAS_BOWDEN.*/)`:      false,
		`printer_notes!~/.*BAMBU.*/ and not false`:         true,
		`printer_model=="Y" or nozzle_diameter[0]<=0.4`:    true,
		`(printer_model=="Y" or nozzle_diameter[0]<0.4)`:   false,
		`missing_key==""`:                                  true,
	}
	for expr, want := range cases {
		got, err := EvalCondition(expr, vars)
		if err != nil || got != want {
			t.Errorf("%s: got %v err %v, want %v", expr, got, err, want)
		}
	}
	if _, err := EvalCondition(`nozzle_diameter[0] = 0.4`, vars); err == nil {
		t.Error("single = should be an error")
	}
}

func TestApplyOverrides(t *testing.T) {
	r := map[string]any{"nozzle_temperature": []any{"210"}, "layer_height": "0.2"}
	ApplyOverrides(r, map[string]string{"nozzle_temperature": "220", "layer_height": "0.16"})
	if r["nozzle_temperature"].([]any)[0] != "220" || r["layer_height"] != "0.16" {
		t.Fatal(r)
	}
}
