package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/library"
	"github.com/thefrayedend/printharbor/internal/sites"
)

// freshImports empties the list of imports for one test. The list is shared by
// the whole program (there is only ever one PrintHarbor), so without this a
// test would see the imports an earlier test left behind.
func freshImports(t *testing.T) {
	clear := func() {
		imports.mu.Lock()
		imports.jobs = map[string]*ImportJob{}
		imports.mu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

type fakeSite struct{}

func (fakeSite) Info() sites.Info {
	return sites.Info{ID: "fake", Name: "Fake", Ready: true, CanDownload: true}
}
func (fakeSite) Search(ctx context.Context, q sites.Query) (sites.Page, error) {
	return sites.Page{Items: []sites.Card{{Site: "fake", ID: "7", Title: "Benchy", Author: "Ann", URL: "https://fake.test/m/7"}}}, nil
}
func (fakeSite) Model(ctx context.Context, id string) (sites.Model, error) {
	return sites.Model{Card: sites.Card{Site: "fake", ID: id, Title: "Benchy: Boat?", Author: "Ann/B", URL: "https://fake.test/m/7"},
		Description: "A boat", Tags: []string{"boat"}, License: "CC-BY", Category: "Toys",
		Files: []sites.File{{ID: "a", Name: "benchy.stl", Size: 84, Kind: "model"}, {ID: "z", Name: "extra.zip", Kind: "model"}, {ID: "g", Name: "x.gcode", Kind: "gcode"}}}, nil
}
func (fakeSite) Download(ctx context.Context, m sites.Model, f sites.File) (io.ReadCloser, error) {
	if f.ID == "z" {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		w, _ := zw.Create("Benchy/parts/mast.stl")
		w.Write([]byte("solid mast"))
		w, _ = zw.Create("../evil.stl")
		w.Write([]byte("x"))
		zw.Close()
		return io.NopCloser(&b), nil
	}
	return io.NopCloser(strings.NewReader("solid benchy")), nil
}

func TestSiteImport(t *testing.T) {
	freshImports(t)
	testSites = []sites.Site{fakeSite{}}
	defer func() { testSites = nil }()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.ModelDir = filepath.Join(dir, "Models")
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler(fstest.MapFS{"index.html": {Data: []byte("ui")}}))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/sites/fake/models/7/import", "application/json", strings.NewReader(`{"files":["a","z"]}`))
	if err != nil || resp.StatusCode != 202 {
		t.Fatal(err, resp.Status)
	}
	var j ImportJob
	for i := 0; i < 50; i++ {
		r, _ := http.Get(srv.URL + "/api/sites/imports")
		var js []ImportJob
		json.NewDecoder(r.Body).Decode(&js)
		r.Body.Close()
		if len(js) == 1 && js[0].State != "running" {
			j = js[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if j.State != "done" {
		t.Fatalf("import: %+v", j)
	}
	folder := filepath.Join(cfg.ModelDir, "Ann B", "Benchy Boat")
	for _, f := range []string{"benchy.stl", "parts/mast.stl", "printharbor.json"} {
		if _, err := os.Stat(filepath.Join(folder, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(folder, "x.gcode")); err == nil {
		t.Error("gcode was not picked")
	}
	if _, err := os.Stat(filepath.Join(cfg.ModelDir, "Ann B", "evil.stl")); err == nil {
		t.Error("zip escaped its folder")
	}
	it, ok := a.Library.Get(j.ItemID)
	if !ok {
		t.Fatalf("not in library: %+v", j)
	}
	if it.Title != "Benchy: Boat?" || it.Designer != "Ann/B" || it.Category != "Toys" || it.SourceURL != "https://fake.test/m/7" || len(it.Files) != 2 {
		t.Errorf("item: %+v", it)
	}
	r, _ := http.Get(srv.URL + "/api/sites/fake/search")
	var pg struct{ Items []cardOut }
	json.NewDecoder(r.Body).Decode(&pg)
	if len(pg.Items) != 1 || pg.Items[0].InLibrary != it.ID {
		t.Errorf("in_library badge: %+v", pg.Items)
	}
}

func TestParseLink(t *testing.T) {
	for u, want := range map[string]string{
		"https://www.printables.com/model/3161-3d-benchy":                "printables:3161",
		"https://www.printables.com/de/model/3161-x":                     "printables:3161",
		"https://www.thingiverse.com/thing:763622":                       "thingiverse:763622",
		"https://makerworld.com/en/models/12345?from=search#profileId-1": "makerworld:12345",
		"https://cults3d.com/en/3d-model/various/benchy-the-boat":        "cults:benchy-the-boat",
	} {
		s, id, ok := sites.ParseLink(u)
		if !ok || s+":"+id != want {
			t.Errorf("%s → %s:%s", u, s, id)
		}
	}
}

func TestCaptureFiled(t *testing.T) {
	freshImports(t)
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.ModelDir = filepath.Join(dir, "Models")
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler(fstest.MapFS{"index.html": {Data: []byte("ui")}}))
	defer srv.Close()
	tmp := filepath.Join(dir, "Downloads", "boaty.stl")
	os.MkdirAll(filepath.Dir(tmp), 0o755)
	os.WriteFile(tmp, []byte("solid x"), 0o644)
	body, _ := json.Marshal(captureReq{Path: tmp, Page: &capturePage{URL: "https://cults3d.com/en/3d-model/various/boaty", Title: "Free STL file Boaty・3D printer model・Cults", Designer: "@Ann", Category: "Toys", Tags: []string{"boat"}}})
	post := func(ext bool) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/capture", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if ext {
			req.Header.Set("X-PrintHarbor", "1")
			req.Header.Set("Origin", "chrome-extension://abc")
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if post(false) != 403 {
		t.Fatal("a web page could report downloads")
	}
	if c := post(true); c != 202 {
		t.Fatal(c)
	}
	var j ImportJob
	for i := 0; i < 50 && j.State != "done"; i++ {
		time.Sleep(50 * time.Millisecond)
		for _, x := range imports.snapshot() {
			if x.Site == "cults" {
				j = x
			}
		}
	}
	if j.State != "done" || j.ItemID == "" {
		t.Fatalf("%+v", j)
	}
	if _, err := os.Stat(filepath.Join(cfg.ModelDir, "Ann", "Boaty", "boaty.stl")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); err == nil {
		t.Error("download was copied, not moved")
	}
	it, _ := a.Library.Get(j.ItemID)
	if it.Designer != "Ann" || it.Category != "Toys" || it.Title != "Boaty" {
		t.Errorf("%+v", it)
	}
}

func TestRefileOnEdit(t *testing.T) {
	testSites = []sites.Site{fakeSite{}}
	defer func() { testSites = nil }()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.ModelDir = filepath.Join(dir, "Models")
	a, _ := New(cfg)
	old := filepath.Join(cfg.ModelDir, "Ann", "Wrong")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "x.stl"), []byte("solid"), 0o644)
	os.WriteFile(filepath.Join(old, "printharbor.json"), []byte(`{"title":"Wrong","designer":"Ann","site":"printables","site_id":"1","imported":"2026-01-01T00:00:00Z"}`), 0o644)
	a.Library.Rescan()
	items, _ := a.Library.Search(library.Query{Limit: 10})
	title := "Caterpillar"
	it, err := a.Library.Update(items[0].ID, library.Meta{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	moved, ok := a.refileImported(it)
	if !ok || !sameFolder(moved.Path, filepath.Join(cfg.ModelDir, "Ann", "Caterpillar")) || moved.Title != "Caterpillar" {
		t.Fatalf("%v %+v", ok, moved)
	}
	// Your own folder (not imported): the details are written into it, the folder keeps its name.
	mine := filepath.Join(cfg.ModelDir, "Titan3D", "Turkey")
	os.MkdirAll(filepath.Join(mine, "Legs"), 0o755)
	os.WriteFile(filepath.Join(mine, "Legs", "leg.stl"), []byte("solid"), 0o644)
	a.Library.Rescan()
	got, _ := a.Library.Search(library.Query{Text: "turkey"})
	if len(got) != 1 || got[0].Designer != "Titan3D" || got[0].Files[0].Name != "Legs/leg.stl" {
		t.Fatalf("turkey: %+v", got)
	}
	nt := "Thanksgiving Turkey"
	it, _ = a.Library.Update(got[0].ID, library.Meta{Title: &nt})
	if _, ok := a.refileImported(it); ok {
		t.Fatal("own folder must not be renamed")
	}
	b, err := os.ReadFile(filepath.Join(mine, "printharbor.json"))
	if err != nil || !strings.Contains(string(b), "Thanksgiving Turkey") || !strings.Contains(string(b), "Titan3D") {
		t.Fatalf("details not saved with the files: %s %v", b, err)
	}
}

func TestJamesPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.ModelDir = filepath.Join(dir, "Models")
	a, _ := New(cfg)
	_, full, err := a.jamesPath("designs", "Bracket.3dj")
	if err != nil || full != filepath.Join(cfg.ModelDir, "My designs", "Bracket", "Bracket.3dj") {
		t.Fatal(full, err)
	}
	if f, p, ok := a.jamesLoc(full); !ok || f != "designs" || p != "Bracket.3dj" {
		t.Fatal(f, p, ok)
	}
	if _, _, err := a.jamesPath("models", "../x.stl"); err == nil {
		t.Fatal("escaped the folder")
	}
}
