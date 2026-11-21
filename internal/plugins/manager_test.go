package plugins

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
	"sync"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/printers"
	"github.com/thefrayedend/printharbor/internal/sites"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return b.Bytes()
}

func TestInstall(t *testing.T) {
	data := t.TempDir()
	m := New(data)
	dir := filepath.Join(data, "plugins")
	// Something already in the plugins folder that an install must never touch.
	os.MkdirAll(filepath.Join(dir, ".runtime"), 0o755)
	os.WriteFile(filepath.Join(dir, ".runtime", "host.py"), []byte("host"), 0o644)
	os.MkdirAll(filepath.Join(dir, "other"), 0o755)
	os.WriteFile(filepath.Join(dir, "other", "plugin.py"), []byte("other"), 0o644)

	id, err := m.Install(zipOf(t, map[string]string{"my-site/plugin.json": `{"id":"my-site"}`, "my-site/plugin.py": "v1", "my-site/lib/x.py": "x"}))
	if err != nil || id != "my-site" {
		t.Fatalf("install: %q %v", id, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "my-site", "lib", "x.py")); string(b) != "x" {
		t.Fatal("files not unpacked")
	}

	// Bad ids: "." used to wipe the whole plugins folder, ".runtime" the host.
	for _, bad := range []string{".", "..", ".runtime", "a/b", `a\b`, "", "con:1", "x y", "example"} {
		_, err := m.Install(zipOf(t, map[string]string{"plugin.json": `{"id":"` + strings.ReplaceAll(bad, `\`, `\\`) + `"}`, "plugin.py": "evil"}))
		if err == nil {
			t.Errorf("id %q was accepted", bad)
		}
	}
	// A zip that reaches into a neighbour's folder, or fails half way, leaves
	// everything as it was — including the copy already installed.
	if _, err := m.Install(zipOf(t, map[string]string{"plugin.json": `{"id":"my-site"}`, "../other/plugin.py": "evil"})); err == nil {
		t.Error("zip slip accepted")
	}
	for path, want := range map[string]string{".runtime/host.py": "host", "other/plugin.py": "other", "my-site/plugin.py": "v1"} {
		if b, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path))); string(b) != want {
			t.Errorf("%s = %q, want %q", path, b, want)
		}
	}
	// Replacing works, and leaves no staging folders behind.
	if _, err := m.Install(zipOf(t, map[string]string{"plugin.json": `{"id":"my-site"}`, "plugin.py": "v2"})); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "my-site", "plugin.py")); string(b) != "v2" {
		t.Error("not replaced")
	}
	if _, err := os.Stat(filepath.Join(dir, "my-site", "lib")); err == nil {
		t.Error("old files left behind")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".install-") {
			t.Errorf("left %s behind", e.Name())
		}
	}
	// Uninstall: user plugins only, never a path.
	for _, bad := range []string{".runtime", "..", "", "example", "a/b"} {
		if err := m.Uninstall(bad); err == nil {
			t.Errorf("uninstall %q accepted", bad)
		}
	}
	if err := m.Uninstall("my-site"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "my-site")); err == nil {
		t.Error("not removed")
	}
	if _, err := os.Stat(filepath.Join(dir, ".runtime", "host.py")); err != nil {
		t.Error("host removed")
	}
}

// The real thing: start the Python plugin host, load the built-in plugins and
// one of our own, and call it the way the Library page does. Skipped on a
// computer without Python (plugins are off there too).
func TestHostAndSitePlugin(t *testing.T) {
	if py, _ := findPython(context.Background()); py == "" {
		t.Skip("Python 3 isn't installed")
	}
	// A file server standing in for a model site's download address.
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing.stl" {
			w.WriteHeader(404)
			return
		}
		io.WriteString(w, "solid from "+r.Header.Get("X-Plugin")+" ua="+r.Header.Get("User-Agent"))
	}))
	defer files.Close()

	data := t.TempDir()
	m := New(data)
	dir := filepath.Join(data, "plugins")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("mysite/plugin.json", `{"id":"mysite","name":"My Site","kind":"site","url":"https://mysite.example","needs_key":true,"key":"mysite_key",
		"key_help":"get a key","can_download":true,"match":["mysite\\.example/m/([0-9]+)","("]}`)
	write("mysite/plugin.py", `
def search(ctx, query=None):
    q = query or {}
    if q.get("text") == "boom":
        raise RuntimeError("the site is closed today")
    if q.get("text") == "bug":
        return {}["nope"]
    return {"items": [{"site": "mysite", "id": "7", "title": "Boat for " + ctx["keys"].get("mysite_key", "nobody"), "author": "Ann", "free": True,
                       "likes": q.get("page", 0), "url": "https://mysite.example/m/7"}], "more": True, "cursor": "c2"}

def model(ctx, id=None):
    return {"id": id, "title": "Boat " + id, "author": "Ann", "description": "floats", "tags": ["boat"],
            "files": [{"id": "url", "name": "boat.stl", "kind": "model"}, {"id": "text", "name": "notes.txt"}, {"id": "gone", "name": "missing.stl"}, {"id": "none", "name": "x.stl"}]}

def download(ctx, id=None, file_id=None):
    if file_id == "url":
        return {"url": "`+files.URL+`/boat.stl", "headers": {"X-Plugin": "mysite"}}
    if file_id == "gone":
        return {"url": "`+files.URL+`/missing.stl"}
    if file_id == "text":
        return {"text": "hello from the plugin", "name": "notes.txt"}
    return {}
`)
	write("broken/plugin.json", `{"id":"broken","name":"Broken"}`)
	write("broken/plugin.py", "def search(ctx, query=None):\n    return (\n")
	write(".install-123/plugin.json", `{"id":"halfdone","name":"Left by a crash"}`)
	write(".install-123/plugin.py", "x = 1\n")
	write("not-a-plugin/readme.txt", "no plugin.json here")

	m.SetKeys(map[string]string{"mysite_key": ""})
	m.Start(context.Background())
	defer m.Stop()
	if why := m.Off(); why != "" {
		t.Fatalf("plugins are off: %s", why)
	}
	byID := map[string]Manifest{}
	for _, man := range m.List() {
		byID[man.ID] = man
	}
	for _, id := range []string{"example", "printables", "thingiverse", "makerworld", "cults"} {
		if man, ok := byID[id]; !ok || !man.Builtin || man.Error != "" {
			t.Errorf("built-in %s: %+v", id, man)
		}
	}
	if man := byID["mysite"]; man.Builtin || man.Error != "" || strings.Join(man.Functions, ",") != "search,model,download" || man.Dir == "" {
		t.Errorf("mysite: %+v", man)
	}
	if man, ok := byID["broken"]; !ok || man.Error == "" {
		t.Errorf("a plugin that fails to load isn't reported: %+v", man)
	}
	if _, ok := byID["halfdone"]; ok {
		t.Error("a half-finished install was loaded as a plugin")
	}
	// 8 built-ins (example, printables, thingiverse, makerworld, cults, filamentcost,
	// example-printer, notify) plus mysite and broken; the half-finished install and
	// non-plugin folder are skipped.
	if len(byID) != 10 {
		t.Errorf("%d plugins listed, want 10: %v", len(byID), byID)
	}
	// The example ships turned off; the rest are on.
	if !byID["example"].Off || byID["mysite"].Off || !m.OffIDs()["example"] {
		t.Errorf("on/off defaults: example off=%v, mysite off=%v", byID["example"].Off, byID["mysite"].Off)
	}

	site := func() sites.Site {
		t.Helper()
		for _, s := range m.Sites() {
			if s.Info().ID == "mysite" {
				return s
			}
		}
		t.Fatal("mysite isn't offered as a model site")
		return nil
	}
	for _, s := range m.Sites() {
		if id := s.Info().ID; id == "example" || id == "broken" {
			t.Errorf("%s is offered as a model site", id)
		}
	}
	s := site()
	if i := s.Info(); i.Ready || !i.NeedsKey || !i.CanDownload || i.Name != "My Site" || i.KeyHelp != "get a key" {
		t.Errorf("info without a key: %+v", i)
	}
	// Keys from Settings reach the plugin, on the next call.
	m.SetKeys(map[string]string{"mysite_key": "Sam"})
	if !site().Info().Ready {
		t.Error("not ready although its key is set")
	}
	ctx := context.Background()
	pg, err := s.Search(ctx, sites.Query{Text: "boat", Page: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(pg.Items) != 1 || pg.Items[0].Title != "Boat for Sam" || pg.Items[0].Likes != 3 || !pg.More || pg.Cursor != "c2" {
		t.Errorf("search: %+v", pg)
	}
	// A plugin's own message is shown as it is; a bug keeps its type so it can be found.
	if _, err := s.Search(ctx, sites.Query{Text: "boom"}); err == nil || err.Error() != "the site is closed today" {
		t.Errorf("plugin error: %v", err)
	}
	if pg, err := s.Search(ctx, sites.Query{Text: "bug"}); err == nil || !strings.Contains(err.Error(), "KeyError") || pg.Items == nil {
		t.Errorf("plugin bug: %v (items nil: %v)", err, pg.Items == nil)
	}
	mo, err := s.Model(ctx, "7")
	if err != nil {
		t.Fatal(err)
	}
	if mo.Site != "mysite" || mo.Title != "Boat 7" || len(mo.Files) != 4 {
		t.Errorf("model: %+v", mo)
	}
	read := func(f sites.File) (string, error) {
		rc, err := s.Download(ctx, mo, f)
		if err != nil {
			return "", err
		}
		defer rc.Close()
		b, _ := io.ReadAll(rc)
		return string(b), nil
	}
	if got, err := read(mo.Files[0]); err != nil || !strings.HasPrefix(got, "solid from mysite ua=PrintHarbor") {
		t.Errorf("download by address: %q %v", got, err)
	}
	if got, err := read(mo.Files[1]); err != nil || got != "hello from the plugin" {
		t.Errorf("download as text: %q %v", got, err)
	}
	if _, err := read(mo.Files[2]); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("dead download: %v", err)
	}
	if _, err := read(mo.Files[3]); err == nil || !strings.Contains(err.Error(), "no download address") {
		t.Errorf("no address: %v", err)
	}
	// Links are recognised from the manifest alone; a bad pattern is skipped.
	if id, mid, ok := m.Match("https://mysite.example/m/42?x=1"); !ok || id != "mysite" || mid != "42" {
		t.Errorf("match: %q %q %v", id, mid, ok)
	}
	if _, _, ok := m.Match("https://elsewhere.example/m/42"); ok {
		t.Error("matched a link of another site")
	}

	// Turned off: gone from the sites, and remembered across a restart of the host.
	m.SetOff("mysite", true)
	for _, x := range m.Sites() {
		if x.Info().ID == "mysite" {
			t.Error("still offered after being turned off")
		}
	}
	if _, _, ok := m.Match("https://mysite.example/m/42"); ok {
		t.Error("a plugin that is off still claims its links")
	}
	m.Reload(ctx)
	if why := m.Off(); why != "" {
		t.Fatalf("after reload: %s", why)
	}
	for _, man := range m.List() {
		if man.ID == "mysite" && !man.Off {
			t.Error("the off switch was forgotten by the reload")
		}
	}
	m.SetOff("mysite", false)
	if _, err := site().Search(ctx, sites.Query{}); err != nil {
		t.Errorf("after turning it back on: %v", err)
	}
	// Uninstalling removes it for good; the host is restarted without it.
	if err := m.Uninstall("mysite"); err != nil {
		t.Fatal(err)
	}
	m.Reload(ctx)
	for _, man := range m.List() {
		if man.ID == "mysite" {
			t.Error("still listed after uninstall")
		}
	}
	// Stopped: calls fail cleanly instead of hanging.
	m.Stop()
	m.client.Timeout = 2 * time.Second
	if err := m.rpc("example", "search", nil, nil); err == nil {
		t.Error("the host still answers after Stop")
	}
}

// Two reloads at once must end with exactly one host, not two.
func TestReloadsDontPileUp(t *testing.T) {
	if py, _ := findPython(context.Background()); py == "" {
		t.Skip("Python 3 isn't installed")
	}
	m := New(t.TempDir())
	m.Start(context.Background())
	defer m.Stop()
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.Reload(context.Background()) }()
	}
	wg.Wait()
	if why := m.Off(); why != "" {
		t.Fatalf("plugins are off: %s", why)
	}
	if n := len(m.List()); n != 8 {
		t.Errorf("%d plugins after the reloads, want 8", n)
	}
	var out struct {
		Items []sites.Card `json:"items"`
	}
	if err := m.rpc("example", "search", map[string]any{"query": map[string]any{"text": "cube"}}, &out); err != nil || len(out.Items) != 1 {
		t.Errorf("the host left running doesn't answer: %v %+v", err, out)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0}, {"1.2.0", "1.1.9", 1}, {"1.9", "1.10", -1},
		{"2.0", "1.9.9", 1}, {"v1.1", "1.0", 1}, {"", "1.0", -1}, {"1.0", "", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestToolSettingsAndUpdate(t *testing.T) {
	if py, _ := findPython(context.Background()); py == "" {
		t.Skip("Python 3 isn't installed")
	}
	// An https server (self-signed) standing in for a plugin's update source.
	zipV2 := zipOf(t, map[string]string{
		"tool1/plugin.json": `{"id":"tool1","name":"Tool One","kind":"tool","version":"2.0.0","entry":"plugin.py"}`,
		"tool1/plugin.py":   "def run(ctx, params=None):\n    return {\"v\": 2}\n",
	})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/u.json":
			io.WriteString(w, `{"version":"2.0.0","zip":"https://`+r.Host+`/p.zip","notes":"faster"}`)
		case "/p.zip":
			w.Write(zipV2)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	data := t.TempDir()
	m := New(data)
	dir := filepath.Join(data, "plugins")
	p := filepath.Join(dir, "tool1")
	os.MkdirAll(p, 0o755)
	os.WriteFile(filepath.Join(p, "plugin.json"), []byte(`{"id":"tool1","name":"Tool One","kind":"tool","version":"1.0.0","entry":"plugin.py",
		"settings":[{"key":"unit","type":"text","default":"mm"}],"params":[{"key":"x","type":"number"}],
		"update_url":"`+srv.URL+`/u.json"}`), 0o644)
	os.WriteFile(filepath.Join(p, "plugin.py"), []byte(
		"def run(ctx, params=None):\n"+
			"    return {\"x\": (params or {}).get(\"x\"), \"unit\": (ctx.get(\"settings\") or {}).get(\"unit\")}\n"), 0o644)

	m.Start(context.Background())
	defer m.Stop()
	if why := m.Off(); why != "" {
		t.Fatalf("plugins off: %s", why)
	}
	// Use the test server's client so the self-signed https cert is trusted.
	m.client = srv.Client()
	m.client.Timeout = 15 * time.Second

	// Tools() lists the tool; its run function was detected.
	tools := m.Tools()
	var found *Manifest
	for i := range tools {
		if tools[i].ID == "tool1" {
			found = &tools[i]
		}
	}
	if found == nil {
		t.Fatalf("tool1 not in Tools(): %+v", tools)
	}
	if strings.Join(found.Functions, ",") != "run" {
		t.Errorf("functions = %v", found.Functions)
	}
	if v := found.Values["unit"]; v != "mm" {
		t.Errorf("default setting value = %v, want mm", v)
	}

	// Run with a param; settings default flows through as ctx["settings"].
	raw, err := m.RunTool("tool1", map[string]any{"x": 5})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("result not json: %v (%s)", err, raw)
	}
	if res["x"] != float64(5) || res["unit"] != "mm" {
		t.Errorf("run result = %s", raw)
	}
	// Change a setting; it reaches the plugin.
	m.SetSettings("tool1", map[string]any{"unit": "cm", "bogus": "dropped"})
	raw, _ = m.RunTool("tool1", map[string]any{"x": 9})
	json.Unmarshal(raw, &res)
	if res["unit"] != "cm" {
		t.Errorf("after SetSettings result = %s", raw)
	}
	// A declared-away key is not persisted.
	if _, ok := m.settingsFor("tool1")["bogus"]; ok {
		t.Error("undeclared setting key was kept")
	}

	// Update check sees 2.0.0 offered; Update installs it, and a reload shows it.
	info, err := m.CheckUpdate(context.Background(), "tool1")
	if err != nil || !info.Available || info.Latest != "2.0.0" {
		t.Fatalf("check-update: %+v %v", info, err)
	}
	if _, err := m.Update(context.Background(), "tool1"); err != nil {
		t.Fatalf("update: %v", err)
	}
	m.Reload(context.Background())
	for _, man := range m.List() {
		if man.ID == "tool1" && man.Version != "2.0.0" {
			t.Errorf("after update version = %q, want 2.0.0", man.Version)
		}
	}
}

func TestPrinterPlugin(t *testing.T) {
	if py, _ := findPython(context.Background()); py == "" {
		t.Skip("Python 3 isn't installed")
	}
	data := t.TempDir()
	m := New(data)
	dir := filepath.Join(data, "plugins")
	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("vprinter/plugin.json", `{"id":"vprinter","name":"V Printer","kind":"printer","output":"gcode",
		"driver":{"address_hint":"1.2.3.4","needs_key":true,"key_label":"Token"}}`)
	write("vprinter/plugin.py", `
seen = {}
def status(ctx, printer=None):
    return {"state": "printing" if seen.get("f") else "idle", "nozzle_c": 210, "bed_c": 60,
            "job_name": seen.get("f",""), "progress": 0.5}
def upload(ctx, printer=None, file=None, name=None, start=False):
    import os
    seen["f"] = name
    seen["key"] = (printer or {}).get("api_key")
    seen["bytes"] = os.path.getsize(file)
    return {"ok": True}
def pause(ctx, printer=None): seen["paused"] = True
def cancel(ctx, printer=None): seen.clear()
`)
	m.Start(context.Background())
	defer m.Stop()
	if why := m.Off(); why != "" {
		t.Fatalf("plugins off: %s", why)
	}

	// The printer plugin registered as a selectable driver with its DriverInfo.
	var di *printers.DriverInfo
	for _, d := range printers.Drivers() {
		if d.Name == "vprinter" {
			dd := d
			di = &dd
		}
	}
	if di == nil || di.Label != "V Printer" || di.AddressHint != "1.2.3.4" || !di.NeedsKey || di.KeyLabel != "Token" {
		t.Fatalf("driver info = %+v", di)
	}

	var man Manifest
	for _, mm := range m.List() {
		if mm.ID == "vprinter" {
			man = mm
		}
	}
	d := &pyPrinter{m: m, id: "vprinter", man: man, p: printers.Printer{ID: "p1", Name: "Mine", Address: "1.2.3.4", APIKey: "secret"}}

	c := d.Capabilities()
	if !c.Status || !c.Upload || !c.StartPrint || !c.Pause || c.Cancel != true || c.Resume {
		t.Errorf("caps = %+v", c)
	}
	st, err := d.Status(context.Background())
	if err != nil || st.State != printers.StateIdle || st.Nozzle != 210 {
		t.Fatalf("status: %+v %v", st, err)
	}
	if err := d.Upload(context.Background(), "dragon.gcode", strings.NewReader("G28\nG1 X1\n"), true); err != nil {
		t.Fatalf("upload: %v", err)
	}
	st, _ = d.Status(context.Background())
	if st.State != printers.StatePrinting || st.JobName != "dragon.gcode" {
		t.Errorf("after upload: %+v", st)
	}
	if err := d.Pause(context.Background()); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := d.Cancel(context.Background()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if st, _ := d.Status(context.Background()); st.State != printers.StateIdle {
		t.Errorf("after cancel: %+v", st)
	}
}
