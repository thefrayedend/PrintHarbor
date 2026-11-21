package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/printers"
	"github.com/thefrayedend/printharbor/internal/slicer"
)

func profileApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.OrcaPath = "/none"
	cfg.OrcaProfilesDir = filepath.Join(dir, "profiles")
	cfg.OrcaUserPresetsDir = ""
	cfg.BackupKeepDays = 0 // the tests make their own
	write(t, filepath.Join(dir, "profiles", "Acme", "machine", "mini.json"),
		`{"type":"machine","name":"Acme Mini 0.4 nozzle","instantiation":"true","printer_model":"Acme Mini","nozzle_diameter":["0.4"],"printable_area":["0x0","220x0","220x220","0x220"],"printable_height":"250","default_print_profile":"0.20 Standard @Acme"}`)
	for name, extra := range map[string]string{"0.20 Standard @Acme": `"layer_height":"0.2"`, "0.28 Draft @Acme": `"layer_height":"0.28"`} {
		write(t, filepath.Join(dir, "profiles", "Acme", "process", name+".json"),
			`{"type":"process","name":"`+name+`","instantiation":"true","support_type":"normal(auto)","wall_loops":"2","sparse_infill_density":"15%",`+extra+`,"compatible_printers":["Acme Mini 0.4 nozzle"]}`)
	}
	write(t, filepath.Join(dir, "profiles", "Acme", "filament", "pla.json"),
		`{"type":"filament","name":"Generic PLA @Acme","instantiation":"true","filament_type":["PLA"],"nozzle_temperature":["210"],"hot_plate_temp":["60"],"compatible_printers":["Acme Mini 0.4 nozzle"]}`)
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.CfgPath = filepath.Join(dir, "config.json")
	return a, dir
}

func postJSON(t *testing.T, url string, v any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(v)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// Each printer can have its own print defaults and its own first quality
// profile. They go on top of the general defaults, for that printer only.
func TestPerPrinterDefaults(t *testing.T) {
	a, _ := profileApp(t)
	one, _ := a.Printers.Put(printersPrinter("One", "Acme Mini 0.4 nozzle"))
	two, _ := a.Printers.Put(printersPrinter("Two", "Acme Mini 0.4 nozzle"))
	srv := httptest.NewServer(a.Handler(nil))
	defer srv.Close()
	val := func(printer, key string) string {
		p, err := a.resolveProcess(a.Lib(), "0.20 Standard @Acme", printer)
		if err != nil {
			t.Fatal(err)
		}
		s, _ := p[key].(string)
		return s
	}
	if c, _ := postJSON(t, srv.URL+"/api/print-defaults", map[string]any{"defaults": map[string]string{"wall_loops": "3", "sparse_infill_density": "20"}}); c != 200 {
		t.Fatal(c)
	}
	if c, out := postJSON(t, srv.URL+"/api/print-defaults", map[string]any{"printer": one.ID, "default_process": "0.28 Draft @Acme",
		"defaults": map[string]string{"support_type": "tree(auto)", "wall_loops": "4"}}); c != 200 {
		t.Fatal(c, out)
	}
	// Printer One: its own values, and the general one where it has none.
	if val(one.ID, "support_type") != "tree(auto)" || val(one.ID, "wall_loops") != "4" || val(one.ID, "sparse_infill_density") != "20%" {
		t.Errorf("printer One: %s, %s walls, %s", val(one.ID, "support_type"), val(one.ID, "wall_loops"), val(one.ID, "sparse_infill_density"))
	}
	// Printer Two and "no printer": only the general ones.
	for _, id := range []string{two.ID, ""} {
		if val(id, "support_type") != "normal(auto)" || val(id, "wall_loops") != "3" {
			t.Errorf("printer %q got another printer's defaults: %s, %s walls", id, val(id, "support_type"), val(id, "wall_loops"))
		}
	}
	// The quality profile first offered: your choice for One, the printer profile's for Two.
	for id, want := range map[string]string{one.ID: "0.28 Draft @Acme", two.ID: "0.20 Standard @Acme"} {
		info, err := a.slicerPrinterInfo(id, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if info["defaultProcess"] != want {
			t.Errorf("slicer page, printer %s: starts on %v, want %s", id, info["defaultProcess"], want)
		}
	}
	// Listed back for Settings, with the profiles that fit.
	resp, _ := http.Get(srv.URL + "/api/print-defaults?printer=" + one.ID)
	var got struct {
		Defaults  map[string]string `json:"defaults"`
		General   map[string]string `json:"general"`
		Processes []string          `json:"processes"`
		Default   string            `json:"default_process"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got.Defaults["wall_loops"] != "4" || got.General["wall_loops"] != "3" || len(got.Processes) != 2 || got.Default != "0.28 Draft @Acme" {
		t.Errorf("listed: %+v", got)
	}
	// Refused: a profile that isn't for this printer, a bad value, an unknown printer. Nothing changes.
	for name, bad := range map[string]map[string]any{
		"not its profile": {"printer": one.ID, "default_process": "0.20 Standard @Other"},
		"bad value":       {"printer": one.ID, "defaults": map[string]string{"support_type": "banana"}},
		"no such printer": {"printer": "nope", "defaults": map[string]string{}},
	} {
		if c, _ := postJSON(t, srv.URL+"/api/print-defaults", bad); c == 200 {
			t.Errorf("%s: accepted", name)
		}
	}
	if val(one.ID, "wall_loops") != "4" {
		t.Error("a refused save changed the printer's defaults")
	}
	// Editing the printer (name, address…) keeps them; so does a restart (they are in printers.json).
	raw, _ := a.Printers.Raw(one.ID)
	if raw.DefaultProcess != "0.28 Draft @Acme" || raw.PrintDefaults["support_type"] != "tree(auto)" {
		t.Errorf("stored printer: %+v", raw)
	}
}

// Your own filament and quality profiles can be listed, changed and removed.
func TestMyProfiles(t *testing.T) {
	a, _ := profileApp(t)
	srv := httptest.NewServer(a.Handler(nil))
	defer srv.Close()
	save := func(typ, name, base string, ov map[string]string) int {
		c, _ := postJSON(t, srv.URL+"/api/profiles/save", map[string]any{"type": typ, "name": name, "inherits": base, "overrides": ov})
		return c
	}
	mine := func(typ string) []MyPreset {
		resp, err := http.Get(srv.URL + "/api/profiles/mine?type=" + typ)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out []MyPreset
		json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	if len(mine("filament")) != 0 {
		t.Fatal("profiles before any were made")
	}
	if c := save("filament", "My silk PLA", "Generic PLA @Acme", map[string]string{"nozzle_temperature": "225"}); c != 200 {
		t.Fatal(c)
	}
	got := mine("filament")
	if len(got) != 1 || got[0].Name != "My silk PLA" || got[0].Inherits != "Generic PLA @Acme" || got[0].Values["nozzle_temperature"] != "225" || len(got[0].Values) != 1 {
		t.Fatalf("after saving: %+v", got)
	}
	f, _ := a.Lib().Resolve("filament", "My silk PLA")
	if profiles := f["nozzle_temperature"].([]any); profiles[0] != "225" || f["hot_plate_temp"].([]any)[0] != "60" {
		t.Errorf("resolved: %v %v", f["nozzle_temperature"], f["hot_plate_temp"])
	}
	// Saved again under the same name: changed, not doubled.
	save("filament", "My silk PLA", "Generic PLA @Acme", map[string]string{"nozzle_temperature": "230", "hot_plate_temp": "65"})
	if got = mine("filament"); len(got) != 1 || got[0].Values["nozzle_temperature"] != "230" || got[0].Values["hot_plate_temp"] != "65" {
		t.Errorf("after changing: %+v", got)
	}
	// Removing: yours yes, a stock one no.
	del := func(typ, name string) int {
		req, _ := http.NewRequest("DELETE", srv.URL+"/api/profiles/mine?type="+typ+"&name="+name, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := del("filament", "Generic%20PLA%20@Acme"); c == 200 || a.Lib().Get("filament", "Generic PLA @Acme") == nil {
		t.Error("a stock profile could be removed")
	}
	if c := del("filament", "My%20silk%20PLA"); c != 200 || len(mine("filament")) != 0 || a.Lib().Get("filament", "My silk PLA") != nil {
		t.Errorf("removing my own profile: %d, %v", c, mine("filament"))
	}
}

// A backup holds what can't be made again and leaves out what can. Unchanged
// files are not stored twice. Old backups go, the last three never.
func TestBackups(t *testing.T) {
	a, dir := profileApp(t)
	data := a.Store.Dir()
	os.WriteFile(a.CfgPath, []byte(`{"listen":":1"}`), 0o644)
	write(t, filepath.Join(data, "projects.json"), `[{"id":"p1"}]`)
	write(t, filepath.Join(data, "projects", "p1", "model.3mf"), "a big painted project")
	write(t, filepath.Join(data, "presets", "PrintHarbor", "filament", "mine.json"), `{}`)
	write(t, filepath.Join(data, "jobs", "aaaaaaaaaaaa", "out", "plate_1.gcode"), "G28")
	write(t, filepath.Join(data, "cache", "x.bin"), "x")
	write(t, filepath.Join(data, "printharbor.log"), "log")
	root := filepath.Join(dir, "elsewhere")
	c := a.cfg()
	c.BackupDir, c.BackupKeepDays = root, 14
	a.setCfg(c)
	t0 := time.Date(2026, 11, 1, 3, 0, 0, 0, time.Local)
	first, err := a.backupNow(t0)
	if err != nil {
		t.Fatal(err)
	}
	in := func(b BackupInfo, rel string) string { return filepath.Join(root, b.Name, filepath.FromSlash(rel)) }
	for _, rel := range []string{"data/projects.json", "data/projects/p1/model.3mf", "data/presets/PrintHarbor/filament/mine.json", "config.json", "HOW TO RESTORE.txt"} {
		if _, err := os.Stat(in(first, rel)); err != nil {
			t.Errorf("not in the backup: %s", rel)
		}
	}
	for _, rel := range []string{"data/jobs", "data/cache", "data/printharbor.log", "data/backups"} {
		if _, err := os.Stat(in(first, rel)); err == nil {
			t.Errorf("in the backup but shouldn't be: %s", rel)
		}
	}
	if first.Files < 4 || first.Bytes == 0 {
		t.Errorf("first backup: %+v", first)
	}
	// The next day one file changed: it is copied, the unchanged one is the same data as yesterday's copy.
	write(t, filepath.Join(data, "projects.json"), `[{"id":"p1"},{"id":"p2"}]`)
	second, err := a.backupNow(t0.Add(24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	a1, _ := os.Stat(in(first, "data/projects/p1/model.3mf"))
	a2, _ := os.Stat(in(second, "data/projects/p1/model.3mf"))
	if !os.SameFile(a1, a2) {
		t.Error("an unchanged file was stored a second time")
	}
	b1, _ := os.ReadFile(in(first, "data/projects.json"))
	b2, _ := os.ReadFile(in(second, "data/projects.json"))
	if string(b1) != `[{"id":"p1"}]` || string(b2) != `[{"id":"p1"},{"id":"p2"}]` {
		t.Errorf("a changed file: first %s, second %s", b1, b2)
	}
	// Changing the live file later must not reach into a backup.
	os.WriteFile(filepath.Join(data, "projects", "p1", "model.3mf"), []byte("overwritten in place!"), 0o644)
	if b, _ := os.ReadFile(in(second, "data/projects/p1/model.3mf")); string(b) != "a big painted project" {
		t.Errorf("a backup changed when the live file did: %q", b)
	}
	// Old ones are removed, but the newest three are always kept.
	for i := 2; i < 6; i++ {
		if _, err := a.backupNow(t0.Add(time.Duration(i) * 24 * time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(a.backups()); n != 6 {
		t.Fatalf("%d backups, want 6 (all inside 14 days)", n)
	}
	if _, err := a.backupNow(t0.Add(60 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	left := a.backups()
	if len(left) != 3 || !left[0].At.Equal(t0.Add(60*24*time.Hour)) {
		t.Errorf("after two months: %d backups left, newest %v", len(left), left[0].At)
	}
	// Switched off: nothing is made by itself.
	c = a.cfg()
	c.BackupKeepDays = 0
	a.setCfg(c)
	a.backupIfDue(t0.Add(90 * 24 * time.Hour))
	if len(a.backups()) != 3 {
		t.Error("a backup was made with backups switched off")
	}
	// Due and on: made; not due: not made again.
	c.BackupKeepDays = 14
	a.setCfg(c)
	a.backupIfDue(t0.Add(90 * 24 * time.Hour))
	a.backupIfDue(t0.Add(90*24*time.Hour + time.Hour))
	if n := len(a.backups()); n != 3 || !a.backups()[0].At.Equal(t0.Add(90*24*time.Hour)) {
		t.Errorf("daily backup: %d, newest %v", n, a.backups()[0].At)
	}
}

// What "Completed prints" shows is kept with the run, because the job itself
// is cleaned up after a few days.
func TestRunKeepsTimeAndWeight(t *testing.T) {
	a := guardApp(t)
	a.startRun(&slicer.Job{ID: "dddddddddd01", ProjectID: "p", ProjectName: "Dragon", Plate: 1, PrinterID: "x", PrinterName: "U1", PrintTimeSec: 3600, FilamentG: 40})
	r := a.Runs("x", "")[0]
	if r.PrintTimeSec != 3600 || r.FilamentG != 40 || r.ProjectName != "Dragon" {
		t.Errorf("run: %+v", r)
	}
}

// A camera that has gone to sleep keeps handing out its last picture (the
// Snapmaker U1 does). The bed check must look at the bed as it is now, and
// "This is my empty bed" must not learn the same old picture twice.
func TestBedCheckWaitsForAFreshPicture(t *testing.T) {
	var mu sync.Mutex
	calls, wakeAfter := 0, 3
	old, now := bedJPEG(true, 10), bedJPEG(false, 3) // asleep: a part from days ago; awake: the empty bed
	cam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		img := old
		if calls > wakeAfter {
			img = bedJPEG(false, 3+calls%2) // awake: a little noise in every picture
			_ = now
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(img)
	}))
	defer cam.Close()
	a := guardApp(t)
	p, _ := a.Printers.Put(printers.Printer{Name: "Sleepy", Driver: "mock", Address: "60", SnapshotURL: cam.URL})
	ctx := context.Background()
	img, fresh, err := a.freshSnapshot(ctx, p.ID)
	if err != nil || !fresh || bytes.Equal(img, old) {
		t.Fatalf("got the old picture from a camera that was waking up (fresh=%v, err=%v)", fresh, err)
	}
	// Learn the empty bed (camera awake now), then let it fall asleep on that same picture.
	if n, err := a.SaveEmptyBed(ctx, p.ID); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	saved, _ := filepath.Glob(filepath.Join(a.bedDir(p.ID), "*.jpg"))
	frozen, _ := os.ReadFile(saved[0])
	mu.Lock()
	old, wakeAfter = frozen, 1<<30 // asleep for good, showing the picture already learned
	mu.Unlock()
	if n, err := a.SaveEmptyBed(ctx, p.ID); err == nil || n != 1 {
		t.Errorf("the same old picture was learned a second time (%d pictures, err %v)", n, err)
	}
	if _, fresh, _ := a.freshSnapshot(ctx, p.ID); fresh {
		t.Error("a frozen camera's picture was called fresh")
	}
}
