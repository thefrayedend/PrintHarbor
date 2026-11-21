package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/thefrayedend/printharbor/internal/config"
)

const fakeOrca = `#!/bin/sh
out=""; plate=""
while [ $# -gt 0 ]; do
  case "$1" in --export-3mf) out="$2"; shift ;; --outputdir) outdir="$2"; shift ;; --slice) plate="$2"; shift ;; esac; shift
done
grep -q '"layer_height": "0.28"' process.json && echo "override applied" > override.txt
mkdir -p t/Metadata && echo "G28 ; plate $plate" > t/Metadata/plate_$plate.gcode
cd t && python3 -c "import zipfile,sys;z=zipfile.ZipFile(sys.argv[1],'w');z.write('Metadata/plate_'+sys.argv[2]+'.gcode');z.close()" "$outdir/$out" "$plate"
`

func write(t *testing.T, p, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func project3mf(t *testing.T) []byte {
	box := func(id int, x, y, z float64) string {
		return fmt.Sprintf(`<object id="%d"><mesh><vertices><vertex x="0" y="0" z="0"/><vertex x="%g" y="%g" z="%g"/></vertices></mesh></object>`, id, x, y, z)
	}
	files := map[string]string{
		"3D/3dmodel.model": `<model><resources>` + box(1, 50, 50, 20) + box(2, 280, 100, 30) +
			`</resources><build><item objectid="1"/><item objectid="2"/></build></model>`,
		"Metadata/model_settings.config": `<config>
		  <object id="1"><metadata key="name" value="Clip"/></object><object id="2"><metadata key="name" value="Rail"/></object>
		  <plate><metadata key="plater_id" value="1"/><model_instance><metadata key="object_id" value="1"/><metadata key="instance_id" value="0"/></model_instance></plate>
		  <plate><metadata key="plater_id" value="2"/><model_instance><metadata key="object_id" value="2"/><metadata key="instance_id" value="0"/></model_instance></plate>
		</config>`,
		"Metadata/project_settings.config": `{"filament_colour":["#000000"],"filament_type":["PLA"],"print_settings_id":"0.20mm Standard @Acme"}`,
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, b := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(b))
	}
	zw.Close()
	return buf.Bytes()
}

func TestEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	dir := t.TempDir()
	prof := filepath.Join(dir, "profiles", "Acme")
	write(t, filepath.Join(prof, "machine", "small.json"), `{"type":"machine","name":"Acme Mini 0.4 nozzle","instantiation":"true","printer_model":"Acme Mini",
	  "nozzle_diameter":["0.4"],"printable_area":["0x0","220x0","220x220","0x220"],"printable_height":"250"}`)
	write(t, filepath.Join(prof, "process", "std.json"), `{"type":"process","name":"0.20mm Standard @Acme","instantiation":"true","layer_height":"0.2","wall_loops":"2"}`)
	write(t, filepath.Join(prof, "process", "draft.json"), `{"type":"process","name":"0.28mm Draft @Acme","instantiation":"true","layer_height":"0.28"}`)
	write(t, filepath.Join(prof, "filament", "pla.json"), `{"type":"filament","name":"Generic PLA @Acme","instantiation":"true","filament_type":["PLA"],"nozzle_temperature":["210"]}`)
	orca := filepath.Join(dir, "orca-slicer")
	write(t, orca, fakeOrca)

	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.OrcaPath = orca
	cfg.OrcaProfilesDir = filepath.Join(dir, "profiles")
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler(fstest.MapFS{"index.html": {Data: []byte("ui")}}))
	defer srv.Close()

	call := func(method, path string, in any, out any) {
		t.Helper()
		var body io.Reader
		if in != nil {
			b, _ := json.Marshal(in)
			body = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, body)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
		}
		if out != nil {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatal(err, string(b))
			}
		}
	}

	// Printer with the Orca profile, simulated connection.
	var pr struct{ ID string }
	call("POST", "/api/printers", map[string]any{"name": "Mini", "driver": "mock", "address": "1", "machine_preset": "Acme Mini 0.4 nozzle"}, &pr)

	// Upload the project.
	var mbuf bytes.Buffer
	mw := multipart.NewWriter(&mbuf)
	fw, _ := mw.CreateFormFile("file", "Shelf kit.3mf")
	fw.Write(project3mf(t))
	mw.Close()
	resp, err := http.Post(srv.URL+"/api/projects", mw.FormDataContentType(), &mbuf)
	if err != nil {
		t.Fatal(err)
	}
	var proj Project
	json.NewDecoder(resp.Body).Decode(&proj)
	resp.Body.Close()
	if proj.Name != "Shelf kit" || len(proj.Data.Plates) != 2 {
		t.Fatalf("project: %+v", proj)
	}

	// Plate 2's 280 mm rail can't fit the 220 mm bed; plate 1 is ready.
	var fits []struct {
		Level   string
		Reasons []struct{ Kind, Text string }
	}
	call("GET", "/api/projects/"+proj.ID+"/plates/2/fit", nil, &fits)
	if fits[0].Level != "cannot" || !strings.Contains(fits[0].Reasons[0].Text, "Rail") {
		t.Fatalf("plate 2 fit: %+v", fits)
	}
	call("GET", "/api/projects/"+proj.ID+"/plates/1/fit", nil, &fits)
	if fits[0].Level != "ready" {
		t.Fatalf("plate 1 fit: %+v", fits)
	}

	// Suggestions pick the project's layer height and a generic PLA.
	var sug Suggestion
	call("GET", "/api/projects/"+proj.ID+"/suggest?printer="+pr.ID, nil, &sug)
	if sug.Process != "0.20mm Standard @Acme" || len(sug.Filaments) != 1 || sug.Filaments[0] != "Generic PLA @Acme" {
		t.Fatalf("suggest: %+v", sug)
	}

	// Slice and print with an override.
	var job struct{ ID string }
	call("POST", "/api/jobs", map[string]any{"project_id": proj.ID, "plate": 1, "printer_id": pr.ID,
		"process_preset": sug.Process, "filament_presets": sug.Filaments, "overrides": map[string]string{"layer_height": "0.28"},
		"send": true, "start": true}, &job)
	var jobs []struct{ ID, State, Error, GCode string }
	for i := 0; i < 100; i++ {
		call("GET", "/api/jobs", nil, &jobs)
		if jobs[0].State == "done" || jobs[0].State == "failed" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if jobs[0].State != "done" {
		t.Fatalf("job: %+v", jobs[0])
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(jobs[0].GCode)), "override.txt")); err != nil {
		t.Fatal("override did not reach Orca's process.json")
	}
	var printers []struct {
		Status struct{ State string }
		Ready  bool
	}
	call("GET", "/api/printers", nil, &printers)
	if printers[0].Status.State != "printing" || printers[0].Ready {
		t.Fatalf("printer should be printing: %+v", printers[0])
	}

	// Save a user preset and see it offered.
	call("POST", "/api/profiles/save", map[string]any{"type": "process", "name": "My fast", "inherits": "0.20mm Standard @Acme",
		"overrides": map[string]string{"wall_loops": "3"}}, nil)
	var res map[string]any
	call("GET", "/api/profiles/resolve?type=process&name=My%20fast", nil, &res)
	if res["wall_loops"] != "3" || res["layer_height"] != "0.2" {
		t.Fatalf("saved preset: %v", res)
	}
	call("GET", "/api/projects/"+proj.ID+"/suggest?printer="+pr.ID, nil, &sug)
	found := false
	for _, p := range sug.Processes {
		found = found || p == "My fast"
	}
	if !found {
		t.Fatal("saved preset not offered", sug.Processes)
	}
}

func TestPickProcess(t *testing.T) {
	names := []string{"0.12 Fine @Snapmaker U1 (0.4 nozzle)", "0.20 Strength @Snapmaker U1 (0.4 nozzle)", "0.20 Standard @Snapmaker U1 (0.4 nozzle)", "0.24 Draft @Snapmaker U1 (0.4 nozzle)"}
	cases := map[string]string{
		"0.20mm Standard @BBL X1C": "0.20 Standard @Snapmaker U1 (0.4 nozzle)",
		"0.12mm Fine @BBL X1C":     "0.12 Fine @Snapmaker U1 (0.4 nozzle)",
		"0.24 Draft @Other":        "0.24 Draft @Snapmaker U1 (0.4 nozzle)",
		"":                         "0.20 Standard @Snapmaker U1 (0.4 nozzle)",
		"0.32mm Ultra Draft":       "0.20 Standard @Snapmaker U1 (0.4 nozzle)",
	}
	for src, want := range cases {
		if got := pickProcess(names, src); got != want {
			t.Errorf("%q: got %q want %q", src, got, want)
		}
	}
}

func TestMeshProjects(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	dir := t.TempDir()
	prof := filepath.Join(dir, "profiles", "Acme")
	write(t, filepath.Join(prof, "machine", "m.json"), `{"type":"machine","name":"Acme 0.4","instantiation":"true","nozzle_diameter":["0.4"],"printable_area":["0x0","200x0","200x200","0x200"],"printable_height":"200"}`)
	write(t, filepath.Join(prof, "process", "p.json"), `{"type":"process","name":"0.20mm Standard @Acme","instantiation":"true","layer_height":"0.2"}`)
	write(t, filepath.Join(prof, "filament", "f.json"), `{"type":"filament","name":"Generic PLA","instantiation":"true","filament_type":["PLA"]}`)
	orca := filepath.Join(dir, "orca-slicer")
	// Records the input files and plate, then produces G-code.
	write(t, orca, `#!/bin/sh
echo "$@" > args.txt
mkdir -p out; echo G28 > out/plate.gcode
for a in "$@"; do case "$a" in --outputdir) ;; esac; done
outdir=""; prev=""; for a in "$@"; do [ "$prev" = "--outputdir" ] && outdir="$a"; prev="$a"; done
echo G28 > "$outdir/plate_1.gcode"
`)
	cfg := config.Defaults()
	cfg.DataDir, cfg.OrcaPath, cfg.OrcaProfilesDir = filepath.Join(dir, "data"), orca, filepath.Join(dir, "profiles")
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stl := func(x, y, z float64) string {
		return fmt.Sprintf("solid s\nvertex 0 0 0\nvertex %g %g %g\nendsolid s\n", x, y, z)
	}
	up := func(name, body string) Upload {
		f, _ := os.CreateTemp(dir, "u")
		f.WriteString(body)
		f.Close()
		return Upload{Name: name, Tmp: f.Name()}
	}

	// Dropping STLs on the start screen makes a project with them on plate 1.
	p, err := a.AddProject([]Upload{up("Arm.stl", stl(100, 20, 10)), up("Gear.stl", stl(30, 30, 8))})
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != "mesh" || len(p.Data.Plates) != 1 || len(p.Data.Plates[0].Parts) != 2 || p.Name != "Arm + 1 more" {
		t.Fatalf("%+v", p)
	}
	// Add a big part on a new plate, then move the gear there too.
	if p, err = a.AddMeshFiles(p.ID, 0, []Upload{up("Base.stl", stl(180, 150, 5))}); err != nil {
		t.Fatal(err)
	}
	if p, err = a.MovePart(p.ID, 2, 2); err != nil {
		t.Fatal(err)
	}
	if len(p.Data.Plates) != 2 || len(p.Data.Plates[0].Parts) != 1 || len(p.Data.Plates[1].Parts) != 2 || p.Data.Plates[1].MaxX != 180 {
		t.Fatalf("plates: %+v", p.Data.Plates)
	}
	// Empty plate 3 can be added and removed; plate 1 can't be removed while it has parts.
	if p, err = a.AddPlate(p.ID); err != nil || p.PlateCount != 3 {
		t.Fatal(err)
	}
	if _, err = a.RemovePlate(p.ID, 1); err == nil {
		t.Fatal("removing a plate with parts must fail")
	}
	if p, err = a.RemovePlate(p.ID, 3); err != nil || p.PlateCount != 2 {
		t.Fatal(err)
	}

	// Slicing plate 2 hands Orca only plate 2's files, as Orca plate 1.
	pr, err := a.Printers.Put(printersPrinter("Box", "Acme 0.4"))
	if err != nil {
		t.Fatal(err)
	}
	j, err := a.Queue.Add(slicerJob(p.ID, 2, pr.ID))
	if err != nil {
		t.Fatal(err)
	}
	var job struct{ State, Error string }
	for i := 0; i < 100; i++ {
		jj, _ := a.Queue.Get(j.ID)
		job.State, job.Error = string(jj.State), jj.Error
		if job.State == "done" || job.State == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if job.State != "done" {
		t.Fatalf("job %+v", job)
	}
	args, _ := os.ReadFile(filepath.Join(cfg.DataDir, "jobs", j.ID, "args.txt"))
	s := string(args)
	if !strings.Contains(s, "--slice 1") || !strings.Contains(s, "Gear.stl") || !strings.Contains(s, "Base.stl") || strings.Contains(s, "Arm.stl") {
		t.Fatalf("orca args: %s", s)
	}
	if _, err := a.RemovePart(p.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.projectDir(p.ID)); !os.IsNotExist(err) {
		t.Fatal("project folder should be gone")
	}
}

func TestLibraryAPI(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir, cfg.OrcaPath, cfg.OrcaProfilesDir = filepath.Join(dir, "data"), "/none", filepath.Join(dir, "none")
	// A folder on "your drive" with a Printables-style download.
	shelf := filepath.Join(dir, "models", "Home", "Shelf Bracket")
	os.MkdirAll(filepath.Join(shelf, "files"), 0o755)
	cube := "solid c\n"
	for _, tri := range [][9]float64{{0, 0, 0, 40, 0, 0, 0, 20, 0}, {0, 0, 10, 40, 0, 10, 0, 20, 10}, {0, 0, 0, 40, 0, 0, 0, 0, 10}} {
		cube += fmt.Sprintf("facet normal 0 0 1\nouter loop\nvertex %g %g %g\nvertex %g %g %g\nvertex %g %g %g\nendloop\nendfacet\n", tri[0], tri[1], tri[2], tri[3], tri[4], tri[5], tri[6], tri[7], tri[8])
	}
	cube += "endsolid c\n"
	os.WriteFile(filepath.Join(shelf, "files", "bracket.stl"), []byte(cube), 0o644)
	cfg.LibraryFolders = []string{filepath.Join(dir, "models")}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.CfgPath = filepath.Join(dir, "config.json")
	srv := httptest.NewServer(a.Handler(fstest.MapFS{"index.html": {Data: []byte("ui")}}))
	defer srv.Close()
	get := func(path string) (*http.Response, []byte) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}
	post := func(path string, body any) []byte {
		b, _ := json.Marshal(body)
		resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, out)
		}
		return out
	}
	post("/api/library/rescan", nil)
	var list struct {
		Items []struct {
			ID, Title, Category, Designer string
		}
		Total int
	}
	_, b := get("/api/library?q=bracket")
	json.Unmarshal(b, &list)
	if list.Total != 1 || list.Items[0].Designer != "Home" {
		t.Fatalf("search: %s", b)
	}
	id := list.Items[0].ID
	resp, png := get("/api/library/" + id + "/cover")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || len(png) < 200 {
		t.Fatalf("cover: %d %s %d", resp.StatusCode, resp.Header.Get("Content-Type"), len(png))
	}
	resp, mb := get("/api/library/" + id + "/mesh?name=files/bracket.stl")
	if resp.StatusCode != 200 || len(mb) != 4+3*9*4 {
		t.Fatalf("mesh: %d %d", resp.StatusCode, len(mb))
	}

	// Upload a managed item with a model and a photo.
	var mbuf bytes.Buffer
	mw := multipart.NewWriter(&mbuf)
	mw.WriteField("title", "Cable clip")
	mw.WriteField("category", "Desk")
	mw.WriteField("tags", "cable, desk")
	fw, _ := mw.CreateFormFile("file", "clip.stl")
	fw.Write([]byte(cube))
	fw, _ = mw.CreateFormFile("photo", "clip.jpg")
	fw.Write([]byte("jpeg-bytes"))
	mw.Close()
	r2, err := http.Post(srv.URL+"/api/library", mw.FormDataContentType(), &mbuf)
	if err != nil {
		t.Fatal(err)
	}
	var item struct {
		ID          string
		Tags        []string
		AddedPhotos []string `json:"added_photos"`
	}
	json.NewDecoder(r2.Body).Decode(&item)
	r2.Body.Close()
	if len(item.Tags) != 2 || len(item.AddedPhotos) != 1 {
		t.Fatalf("create: %+v", item)
	}
	resp, _ = get("/api/library/" + item.ID + "/cover")
	if resp.Request.URL.Path != "/api/library/"+item.ID+"/raw/added" {
		t.Fatalf("cover should be the photo, got %s", resp.Request.URL)
	}

	// Print it: becomes an STL project; then add the bracket to a new plate of it.
	var proj Project
	json.Unmarshal(post("/api/library/"+item.ID+"/print", map[string]any{}), &proj)
	if proj.Kind != "mesh" || proj.Name != "Cable clip" || len(proj.MeshParts) != 1 {
		t.Fatalf("print: %+v", proj)
	}
	json.Unmarshal(post("/api/library/"+id+"/print", map[string]any{"project_id": proj.ID, "plate": 0}), &proj)
	if proj.PlateCount != 2 || len(proj.MeshParts) != 2 {
		t.Fatalf("add to plate: %+v", proj)
	}
	resp, png = get(fmt.Sprintf("/api/projects/%s/plates/2/thumb", proj.ID))
	if resp.StatusCode != 200 || len(png) < 200 {
		t.Fatal("plate preview", resp.StatusCode)
	}
	it, _ := a.Library.Get(id)
	if it.PrintCount != 1 || it.Files[0].SizeMM[0] != 40 {
		t.Fatalf("count/size: %+v", it)
	}
	// Save the project back to the library.
	var saved struct{ ID, Title string }
	json.Unmarshal(post("/api/projects/"+proj.ID+"/to-library", map[string]any{}), &saved)
	if saved.Title != "Cable clip" {
		t.Fatalf("to-library: %+v", saved)
	}
	// Folder settings are validated and saved to config.
	b, _ = json.Marshal(map[string]any{"folders": []string{filepath.Join(dir, "nope")}})
	r3, _ := http.Post(srv.URL+"/api/library/folders", "application/json", bytes.NewReader(b))
	if r3.StatusCode != 400 {
		t.Fatal("missing folder should be rejected")
	}
	post("/api/library/folders", map[string]any{"folders": []string{filepath.Join(dir, "models")}})
	if c, _ := os.ReadFile(a.CfgPath); !strings.Contains(string(c), "models") {
		t.Fatal("folders not saved to config")
	}
}

func TestDispatchTrackingRepack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	dir := t.TempDir()
	prof := filepath.Join(dir, "profiles", "Acme")
	write(t, filepath.Join(prof, "machine", "big.json"), `{"type":"machine","name":"Big","instantiation":"true","nozzle_diameter":["0.4"],"printable_area":["0x0","300x0","300x300","0x300"],"printable_height":"300"}`)
	write(t, filepath.Join(prof, "machine", "small.json"), `{"type":"machine","name":"Small","instantiation":"true","nozzle_diameter":["0.4"],"printable_area":["0x0","200x0","200x200","0x200"],"printable_height":"200"}`)
	write(t, filepath.Join(prof, "process", "p.json"), `{"type":"process","name":"0.20mm Standard","instantiation":"true","layer_height":"0.2"}`)
	write(t, filepath.Join(prof, "filament", "f.json"), `{"type":"filament","name":"Generic PLA","instantiation":"true","filament_type":["PLA"]}`)
	orca := filepath.Join(dir, "orca-slicer")
	write(t, orca, "#!/bin/sh\noutdir=\"\"; prev=\"\"; for a in \"$@\"; do [ \"$prev\" = \"--outputdir\" ] && outdir=\"$a\"; prev=\"$a\"; done\necho G28 > \"$outdir/plate_1.gcode\"\n")
	spool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"id":7,"remaining_weight":800,"filament":{"name":"PLA Red","material":"PLA","color_hex":"ff0000","vendor":{"name":"Polymaker"}}},
		  {"id":8,"archived":true,"filament":{"name":"old"}}]`)
	}))
	defer spool.Close()
	cfg := config.Defaults()
	cfg.DataDir, cfg.OrcaPath, cfg.OrcaProfilesDir, cfg.SpoolmanURL = filepath.Join(dir, "data"), orca, filepath.Join(dir, "profiles"), spool.URL
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stl := func(x, y, z float64) string {
		return fmt.Sprintf("solid s\nvertex 0 0 0\nvertex %g %g %g\nendsolid s\n", x, y, z)
	}
	up := func(name, body string) Upload {
		f, _ := os.CreateTemp(dir, "u")
		f.WriteString(body)
		f.Close()
		return Upload{Name: name, Tmp: f.Name()}
	}
	p, _ := a.AddProject([]Upload{up("Base.stl", stl(250, 150, 10))})
	p, _ = a.AddMeshFiles(p.ID, 0, []Upload{up("Arm.stl", stl(120, 30, 20))})
	p, _ = a.AddMeshFiles(p.ID, 0, []Upload{up("Clip.stl", stl(20, 10, 5))})
	if p, err = a.SetCopies(p.ID, 3, 2); err != nil {
		t.Fatal(err)
	}
	big, _ := a.Printers.Put(printersPrinterSecs("Big", "Big", "1"))
	small, _ := a.Printers.Put(printersPrinterSecs("Small", "Small", "1"))
	a.Printers.PollOnce(context.Background())

	as, err := a.Dispatch(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Base (250 mm) only fits Big; the next plate goes to Small.
	if len(as) != 2 || as[0].Plate != 1 || as[0].PrinterID != big.ID || as[1].PrinterID != small.ID || as[0].EstimateSec <= 0 {
		t.Fatalf("assignments %+v", as)
	}
	// A second dispatch while both are busy sends nothing.
	if more, _ := a.Dispatch(p.ID); len(more) != 0 {
		t.Fatalf("printers are busy: %+v", more)
	}
	waitFor := func(cond func() bool) {
		for i := 0; i < 200 && !cond(); i++ {
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor(func() bool { return len(a.Runs("", p.ID)) == 2 }) // jobs sliced + sent
	if len(a.Runs("", p.ID)) != 2 {
		t.Fatal("runs not started")
	}
	prog := a.Progress(p)
	if prog[0].Printing != 1 || prog[2].Needed != 2 || prog[2].Remaining != 2 {
		t.Fatalf("progress %+v", prog)
	}
	// The printers are seen printing (the poll does this every few seconds)...
	a.Printers.PollOnce(context.Background())
	// ...Mock prints take 1 s; then the printers report finished.
	time.Sleep(1200 * time.Millisecond)
	a.Printers.PollOnce(context.Background())
	waitFor(func() bool {
		for _, r := range a.Runs("", p.ID) {
			if r.State != "printed" {
				return false
			}
		}
		return true
	})
	runs := a.Runs("", p.ID)
	if runs[0].State != "printed" {
		t.Fatalf("runs %+v", runs)
	}
	a.ReviewRun(runs[0].ID, true)
	a.ReviewRun(runs[1].ID, false)
	prog = a.Progress(p)
	acc, failed := 0, 0
	for _, pp := range prog {
		acc += pp.Accepted
		failed += pp.Failed
	}
	if acc != 1 || failed != 1 {
		t.Fatalf("review: %+v", prog)
	}

	// Repack for the small printer: Base doesn't fit → clear error.
	if _, err := a.Repack(p.ID, small.ID); err == nil || !strings.Contains(err.Error(), "Base") {
		t.Fatalf("want doesn't-fit error, got %v", err)
	}
	a.RemovePart(p.ID, 1)
	p, err = a.Repack(p.ID, small.ID)
	if err != nil || p.PlateCount != 1 || len(p.Data.Plates[0].Parts) != 2 {
		t.Fatalf("repack: %v %+v", err, p.Data.Plates)
	}

	// Spoolman lock: filament 1 must come from spool #7.
	spools, err := a.Spools(context.Background())
	if err != nil || len(spools) != 1 || spools[0].Label() != "#7 Polymaker PLA Red" || spools[0].Color != "#ff0000" {
		t.Fatalf("spools %+v %v", spools, err)
	}
	p, _ = a.SetLock(p.ID, 1, 7)
	locks := a.locksFor(p)
	if locks[1].Label != "#7 Polymaker PLA Red" {
		t.Fatalf("locks %+v", locks)
	}
}
