package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/thefrayedend/printharbor/internal/appver"
	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/slicer"
)

// PrintHarbor updating itself.
//
// Every build made from the repository is published as the release "latest":
// the programs plus printharbor.json, which says which build they are and
// gives each file's SHA-256. PrintHarbor reads that file a couple of minutes
// after it starts and every few hours. When the build there is newer it
// downloads its own program to <program>.new next to itself, checks the
// SHA-256, and restarts; the swap on start (see cmd/printharbor) does the
// rest and keeps the old program as <program>.old.
//
// update_mode in the config: "ask" (default) only offers the update, "auto"
// installs it when nothing is being sliced or sent, "off" never looks.
// In a container the program is part of the image, so there it only says a
// new image is out.

// DefaultUpdateURL is where the releases are published.
const DefaultUpdateURL = "https://github.com/thefrayedend/PrintHarbor/releases/download/latest"

var updClient = func() *http.Client { return publicClient(10 * time.Minute) }

type updFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type updManifest struct {
	Build    int                `json:"build"`
	Version  string             `json:"version"`
	AppBuild int                `json:"app_build,omitempty"` // build of the desktop window in this release
	Notes    string             `json:"notes,omitempty"`
	Files    map[string]updFile `json:"files"` // "windows-amd64", "linux-amd64", "app-windows-amd64", …
}

// UpdateStatus is what the web page shows.
type UpdateStatus struct {
	Mode       string    `json:"mode"`      // ask | auto | off
	Build      int       `json:"build"`     // this program
	Latest     int       `json:"latest"`    // newest published build (0: not known yet)
	Version    string    `json:"version"`   // of the newest
	Notes      string    `json:"notes"`     // what changed
	Available  bool      `json:"available"` // a newer build is out
	CanInstall bool      `json:"can_install"`
	Why        string    `json:"why"` // why it can't install itself, when it can't
	CheckedAt  time.Time `json:"checked_at"`
	Error      string    `json:"error"`
	Installing bool      `json:"installing"`
}

type updState struct {
	mu         sync.Mutex
	m          updManifest
	checked    time.Time
	err        string
	installing bool
}

func (a *App) updMode() string {
	switch m := strings.ToLower(strings.TrimSpace(a.cfg().UpdateMode)); m {
	case "auto", "off":
		return m
	}
	return "ask"
}

func inContainer() bool {
	if os.Getenv("PRINTHARBOR_CONTAINER") != "" {
		return true
	}
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

func updPlatform() string { return runtime.GOOS + "-" + runtime.GOARCH }

func (a *App) UpdateStatus() UpdateStatus {
	a.upd.mu.Lock()
	defer a.upd.mu.Unlock()
	s := UpdateStatus{Mode: a.updMode(), Build: Build, Latest: a.upd.m.Build, Version: a.upd.m.Version, Notes: a.upd.m.Notes,
		Available: a.upd.m.Build > Build, CheckedAt: a.upd.checked, Error: a.upd.err, Installing: a.upd.installing}
	switch {
	case inContainer():
		s.Why = "This PrintHarbor runs in a container: pull the new image and start it again to update."
	case a.Restart == nil:
		s.Why = "PrintHarbor can't restart itself here. Download the new program and restart the service."
	case a.upd.m.Files[updPlatform()].Name == "":
		if s.Available {
			s.Why = "The new build has no program for this kind of computer (" + updPlatform() + ")."
		}
	default:
		s.CanInstall = true
	}
	return s
}

// CheckUpdate reads the published build number.
func (a *App) CheckUpdate(ctx context.Context) error {
	base := strings.TrimRight(strings.TrimSpace(a.cfg().UpdateURL), "/")
	if base == "" {
		base = DefaultUpdateURL
	}
	var m updManifest
	err := getInto(ctx, updClient(), base+"/printharbor.json", func(r io.Reader) error {
		return json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&m)
	})
	if err == nil && m.Build <= 0 {
		err = fmt.Errorf("the release description was not understood")
	}
	a.upd.mu.Lock()
	defer a.upd.mu.Unlock()
	a.upd.checked = time.Now()
	if err != nil {
		a.upd.err = "Could not look for a new PrintHarbor: " + err.Error()
		return fmt.Errorf("%s", a.upd.err)
	}
	a.upd.err, a.upd.m = "", m
	return nil
}

// fetchVerified downloads url to dst (through dst+".part") and refuses it
// unless it has the expected SHA-256.
func fetchVerified(ctx context.Context, url, dst string, f updFile) error {
	if len(f.SHA256) != 64 {
		return fmt.Errorf("the release gives no checksum for %s", f.Name)
	}
	part := dst + ".part"
	defer os.Remove(part)
	h := sha256.New()
	var n int64
	err := getInto(ctx, updClient(), url, func(r io.Reader) error {
		out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		n, err = io.Copy(io.MultiWriter(out, h), io.LimitReader(r, 500<<20))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("could not download %s: %w", f.Name, err)
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), f.SHA256) || (f.Size > 0 && n != f.Size) {
		return fmt.Errorf("%s did not match its checksum, so it was not installed", f.Name)
	}
	_ = os.Remove(dst)
	return os.Rename(part, dst)
}

func (a *App) busyJobs() int {
	n := 0
	for _, j := range a.Queue.List() {
		if j.State == slicer.Slicing || j.State == slicer.Sending {
			n++
		}
	}
	return n
}

// InstallUpdate downloads the newest build next to the running program and
// restarts into it.
func (a *App) InstallUpdate(ctx context.Context, force bool) error {
	st := a.UpdateStatus()
	if !st.Available {
		return fmt.Errorf("this is already the newest build")
	}
	if !st.CanInstall {
		return fmt.Errorf("%s", st.Why)
	}
	if !force && a.busyJobs() > 0 {
		return fmt.Errorf("a plate is being sliced or sent right now; updating restarts PrintHarbor and stops it")
	}
	a.upd.mu.Lock()
	if a.upd.installing {
		a.upd.mu.Unlock()
		return fmt.Errorf("the update is already being installed")
	}
	a.upd.installing = true
	m := a.upd.m
	a.upd.mu.Unlock()
	done := func() { a.upd.mu.Lock(); a.upd.installing = false; a.upd.mu.Unlock() }

	exe, err := os.Executable()
	if err == nil {
		if e2, err2 := filepath.EvalSymlinks(exe); err2 == nil {
			exe = e2
		}
	}
	if err != nil {
		done()
		return err
	}
	base := strings.TrimRight(strings.TrimSpace(a.cfg().UpdateURL), "/")
	if base == "" {
		base = DefaultUpdateURL
	}
	ext := filepath.Ext(exe) // the waiting copy is <name>.new<ext>, as cmd/printharbor expects
	// The desktop window is its own program. Fetch it too when the release
	// carries a newer one than this server came with. A problem with it does
	// not stop the update.
	if af, ok := m.Files["app-"+updPlatform()]; ok && af.Name != "" && m.AppBuild > appver.Build {
		appExe := filepath.Join(filepath.Dir(exe), "printharbor-app"+ext)
		if err := fetchVerified(ctx, base+"/"+af.Name, strings.TrimSuffix(appExe, ext)+".new"+ext, af); err != nil {
			log.Printf("Update: the app window was not updated: %v", err)
		}
	}
	f := m.Files[updPlatform()]
	if err := fetchVerified(ctx, base+"/"+f.Name, strings.TrimSuffix(exe, ext)+".new"+ext, f); err != nil {
		done()
		return err
	}
	log.Printf("Update: build %d downloaded and checked; restarting into it", m.Build)
	go func() {
		time.Sleep(500 * time.Millisecond) // let the answer reach the browser
		a.Restart()
	}()
	return nil
}

// updLoop looks for a new build soon after start and every six hours.
func (a *App) updLoop(ctx context.Context) {
	wait := 2 * time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = 6 * time.Hour
		if a.updMode() == "off" {
			continue
		}
		c, cancel := context.WithTimeout(ctx, time.Minute)
		err := a.CheckUpdate(c)
		cancel()
		if err != nil {
			log.Printf("Update: %v", err)
			continue
		}
		st := a.UpdateStatus()
		if !st.Available {
			continue
		}
		log.Printf("Update: build %d is available (this is build %d)", st.Latest, Build)
		if st.Mode == "auto" && st.CanInstall {
			if a.busyJobs() > 0 {
				wait = 10 * time.Minute // try again when the slicer is free
				continue
			}
			c, cancel := context.WithTimeout(ctx, 15*time.Minute)
			if err := a.InstallUpdate(c, false); err != nil {
				log.Printf("Update: %v", err)
			}
			cancel()
		}
	}
}

func (a *App) updRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/update", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, a.UpdateStatus())
	})
	mux.HandleFunc("POST /api/update/check", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		if err := a.CheckUpdate(ctx); err != nil {
			fail(w, 502, err)
			return
		}
		writeJSON(w, 200, a.UpdateStatus())
	})
	mux.HandleFunc("POST /api/update/install", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := a.InstallUpdate(ctx, r.URL.Query().Get("force") == "1"); err != nil {
			fail(w, 409, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// How updates arrive: PrintHarbor itself (ask | auto | off) and the
	// profiles (newest | off).
	mux.HandleFunc("POST /api/update/mode", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Mode     string `json:"mode"`
			Profiles string `json:"profiles"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		a.cfgEdit.Lock()
		defer a.cfgEdit.Unlock()
		c := a.cfg()
		reload := false
		switch in.Mode {
		case "":
		case "ask", "auto", "off":
			c.UpdateMode = in.Mode
		default:
			fail(w, 400, fmt.Errorf("mode must be ask, auto or off"))
			return
		}
		switch in.Profiles {
		case "":
		case "newest", "off":
			reload = c.ProfilesChannel != in.Profiles
			c.ProfilesChannel = in.Profiles
		default:
			fail(w, 400, fmt.Errorf("profiles must be newest or off"))
			return
		}
		a.setCfg(c)
		if a.CfgPath != "" {
			if err := config.Save(a.CfgPath, c); err != nil {
				fail(w, 500, err)
				return
			}
		}
		if reload {
			a.ReloadProfiles()
		}
		writeJSON(w, 200, map[string]any{"update": a.UpdateStatus(), "profiles": a.ProfileUpdates()})
	})
}
