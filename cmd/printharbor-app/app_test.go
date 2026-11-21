package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/appver"
)

func TestBootScript(t *testing.T) {
	js := bootScript("http://localhost:7755")
	if strings.Contains(js, "__PH_") {
		t.Error("a placeholder was left in the script")
	}
	if !strings.Contains(js, "var PH_LIB = 'http://localhost:7755/?app=1#library';") {
		t.Error("the Library address doesn't use the configured port")
	}
	if strings.Contains(js, "7621") {
		t.Error("the default port is still hard-coded in the script")
	}
	if !strings.Contains(js, "window.__phAppBuild = "+strconv.Itoa(appver.Build)+";") {
		t.Error("the window's build number isn't set")
	}
	// The flag and build number must be set before the script leaves
	// PrintHarbor's own pages alone.
	if strings.Index(js, "window.__phAppBuild") > strings.Index(js, "PrintHarbor's own UI") {
		t.Error("the build number is set after the early return")
	}
}

func TestLoadAppConfig(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	os.MkdirAll(dist, 0o755)
	// No config anywhere: the defaults, data next to dist.
	c := loadAppConfig(dist, nil)
	if c.Port != "7621" || c.DataDir != filepath.Join(root, "data") {
		t.Errorf("defaults: %+v", c)
	}
	// config.json one folder up (the normal layout), relative data_dir.
	os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"listen":"0.0.0.0:8123","data_dir":"mydata"}`), 0o644)
	c = loadAppConfig(dist, nil)
	if c.Port != "8123" || c.DataDir != filepath.Join(root, "mydata") {
		t.Errorf("parent config: %+v", c)
	}
	// One next to the program wins; absolute data_dir; listen without a host.
	abs := filepath.Join(root, "elsewhere")
	os.WriteFile(filepath.Join(dist, "config.json"), []byte(`{"listen":":9000","data_dir":`+strconv.Quote(abs)+`}`), 0o644)
	c = loadAppConfig(dist, nil)
	if c.Port != "9000" || c.DataDir != abs {
		t.Errorf("own config: %+v", c)
	}
	// -config beats both.
	other := filepath.Join(root, "other.json")
	os.WriteFile(other, []byte(`{"listen":"127.0.0.1:7700"}`), 0o644)
	c = loadAppConfig(dist, []string{"-config", other})
	if c.Port != "7700" || c.DataDir != filepath.Join(root, "data") {
		t.Errorf("-config: %+v", c)
	}
	// Nonsense never produces a nonsense port.
	for _, bad := range []string{`{"listen":"7621"}`, `{"listen":":0"}`, `{"listen":":99999"}`, `{"listen":":abc"}`, `not json`, `{}`} {
		os.WriteFile(other, []byte(bad), 0o644)
		if c := loadAppConfig(dist, []string{"-config", other}); c.Port != "7621" {
			t.Errorf("%s gave port %q", bad, c.Port)
		}
	}
}

func TestAppendCapped(t *testing.T) {
	p := filepath.Join(t.TempDir(), "app-log.txt")
	for i := 0; i < 400; i++ {
		appendCapped(p, "line "+strconv.Itoa(i)+" "+strings.Repeat("x", 40), 4096, 1024)
	}
	b, _ := os.ReadFile(p)
	if len(b) > 4096+200 {
		t.Errorf("the log grew to %d bytes", len(b))
	}
	s := string(b)
	if !strings.HasSuffix(s, "line 399 "+strings.Repeat("x", 40)+"\n") {
		t.Error("the newest line is missing")
	}
	if !strings.HasPrefix(s, "(older lines removed)\nline ") {
		t.Errorf("the cut isn't at a line start: %q", s[:60])
	}
	if strings.Contains(s, "line 0 ") {
		t.Error("the oldest line is still there")
	}
}

func TestReplaced(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "printharbor-app.exe")
	os.WriteFile(p, []byte("version one"), 0o755)
	was, ok := stampOf(p)
	if !ok {
		t.Fatal("no stamp")
	}
	if replaced(p, was) {
		t.Error("an untouched file reads as replaced")
	}
	// The server's swap: the running program is renamed away…
	os.Rename(p, p+".old")
	if replaced(p, was) {
		t.Error("a missing file (mid-swap) reads as replaced")
	}
	// …and the new one put in its place.
	os.WriteFile(p, []byte("version two!"), 0o755)
	if !replaced(p, was) {
		t.Error("the swap wasn't noticed")
	}
	// Same size, different time counts too.
	os.WriteFile(p, []byte("version one"), 0o755)
	os.Chtimes(p, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	if !replaced(p, was) {
		t.Error("a same-size replacement wasn't noticed")
	}
	if _, ok := stampOf(dir); ok {
		t.Error("a folder has no stamp")
	}
}

func TestSafeToReopen(t *testing.T) {
	noticed := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		ownPage time.Time
		want    bool
	}{
		"the page never loaded here (browsing a model site)":   {time.Time{}, false},
		"the page loaded an hour ago, a design may be open":    {noticed.Add(-time.Hour), false},
		"the page loaded 10 seconds before":                    {noticed.Add(-10 * time.Second), false},
		"the page reloaded just before the update was noticed": {noticed.Add(-2 * time.Second), true},
		"the page reloads after the restart":                   {noticed.Add(3 * time.Second), true},
		"the page loads much later (back from a model site)":   {noticed.Add(2 * time.Hour), true},
	} {
		if got := safeToReopen(noticed, c.ownPage); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
