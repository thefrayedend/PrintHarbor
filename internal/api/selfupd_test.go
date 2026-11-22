package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/config"
)

// A newer published build is downloaded next to the program, checked against
// its SHA-256 and only then restarted into; a wrong file is never left there.
func TestSelfUpdate(t *testing.T) {
	prog := []byte(strings.Repeat("new program ", 500))
	sum := sha256.Sum256(prog)
	m := updManifest{Build: Build + 1, Version: "9.9.9", Files: map[string]updFile{
		updPlatform(): {Name: "ph.bin", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(prog))}}}
	serve := prog
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/printharbor.json":
			json.NewEncoder(w).Encode(m)
		case "/ph.bin":
			w.Write(serve)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	updClient = func() *http.Client { return srv.Client() }
	defer func() { updClient = func() *http.Client { return publicClient(10 * time.Minute) } }()

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir, cfg.OrcaPath, cfg.OrcaProfilesDir = filepath.Join(dir, "data"), "/none", filepath.Join(dir, "none")
	cfg.UpdateURL = srv.URL
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	ext := filepath.Ext(exe)
	next := strings.TrimSuffix(exe, ext) + ".new" + ext
	defer os.Remove(next)

	if err := a.InstallUpdate(context.Background(), false); err == nil {
		t.Fatal("installed before anything was known to be newer")
	}
	if err := a.CheckUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := a.UpdateStatus(); !st.Available || st.CanInstall {
		t.Fatalf("without a way to restart it must be offered but not installable: %+v", st)
	}
	restarted := make(chan bool, 1)
	a.Restart = func() { restarted <- true }

	// a download that is not the published file is refused
	serve = []byte(strings.Repeat("something else ", 400))
	if err := a.InstallUpdate(context.Background(), false); err == nil {
		t.Fatal("a file with the wrong checksum was installed")
	}
	if _, err := os.Stat(next); err == nil {
		t.Fatal("the bad download was left where the next start would install it")
	}
	select {
	case <-restarted:
		t.Fatal("restarted after a bad download")
	case <-time.After(700 * time.Millisecond):
	}

	serve = prog
	if err := a.InstallUpdate(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(next)
	if string(got) != string(prog) {
		t.Fatal("the update was not put next to the program")
	}
	select {
	case <-restarted:
	case <-time.After(3 * time.Second):
		t.Fatal("no restart after the update was downloaded")
	}

	// the same build again: nothing to do
	m.Build = Build
	a.CheckUpdate(context.Background())
	if a.UpdateStatus().Available {
		t.Error("the build that is running was offered as an update")
	}
}
