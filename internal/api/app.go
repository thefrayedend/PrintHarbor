// Package api wires PrintHarbor together and serves the HTTP API and web UI.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thefrayedend/printharbor/internal/ai"
	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/extension"
	"github.com/thefrayedend/printharbor/internal/library"
	"github.com/thefrayedend/printharbor/internal/plugins"
	"github.com/thefrayedend/printharbor/internal/printers"
	"github.com/thefrayedend/printharbor/internal/profiles"
	"github.com/thefrayedend/printharbor/internal/slicer"
	"github.com/thefrayedend/printharbor/internal/store"
	"github.com/thefrayedend/printharbor/internal/threemf"
)

const Version = "0.4.2"

// Build says exactly which build of the server this is. It is the number of
// the versions\ folder the exe was saved into, so Settings ("Version 0.4.2,
// build 56") tells you which backup matches the program that is running.
// Bump it for every build that is handed over (see docs/CODE-LAYOUT.md).
const Build = 90

// VersionLabel is the version with its build number, as shown to people.
func VersionLabel() string { return Version + " (build " + strconv.Itoa(Build) + ")" }

// Project is an uploaded 3MF, or one or more STL/OBJ files. The original
// files are never modified.
type Project struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Uploaded time.Time        `json:"uploaded"`
	Size     int64            `json:"size"`
	Kind     string           `json:"kind"` // "3mf", "mesh" or "slicer" (made in the slicer page)
	Data     *threemf.Project `json:"data"`
	// Slicer: the plates as saved from the slicer page. When set, the queue
	// slices those plate files with those settings (see slicerproj.go).
	Slicer *SlicerProject `json:"slicer,omitempty"`
	// Mesh projects (STL/OBJ): each part is a file on a plate the user picks.
	MeshParts  []MeshPart `json:"mesh_parts,omitempty"`
	PlateCount int        `json:"plate_count,omitempty"`
	// Dispatch and tracking.
	Copies       map[int]int `json:"copies,omitempty"` // plate → copies needed (default 1)
	AutoDispatch bool        `json:"auto_dispatch,omitempty"`
	Goal         string      `json:"goal,omitempty"`  // small | fast | loaded
	Locks        map[int]int `json:"locks,omitempty"` // project filament → Spoolman spool ID
}

// Upload is one uploaded file waiting in a temp location.
type Upload struct {
	Name string
	Tmp  string
}

type App struct {
	// Cfg is the settings. They are read all over the place and from several
	// goroutines at once (requests, the printer poll, the queue, housekeeping)
	// while the Settings page changes them, so nothing reads or writes this
	// field directly: read with cfg() (a copy), write with setCfg, and hold
	// cfgEdit across a read-change-save so two saves can't overwrite each other.
	Cfg          config.Config
	cfgMu        sync.RWMutex
	cfgEdit      sync.Mutex
	backupMu     sync.Mutex // one backup at a time
	backupWarned time.Time  // when a failed backup was last reported (only the housekeeping goroutine uses it)
	CfgPath      string     // where Cfg is saved when settings change
	// Restart, set by main, restarts the program (and installs a downloaded
	// update waiting next to it). Nil when restarting isn't possible.
	Restart func()
	upd     updState // the newest published build (see selfupd.go)
	// StartedAt is when this copy of PrintHarbor started.
	StartedAt   time.Time
	Library     *library.Library
	render      chan struct{} // limits concurrent preview renders (CPU)
	Store       *store.Store
	Printers    *printers.Registry
	Queue       *slicer.Queue
	Slicer      slicer.Slicer // what the queue uses: workers first, then local
	localSlicer slicer.Slicer

	libMu sync.RWMutex
	lib   *profiles.Library

	projMu   sync.Mutex
	projects map[string]*Project

	runsMu     sync.Mutex
	runs       []*Run
	ended      map[string]printEnded // printer → how its last print ended (runsMu)
	dispatchMu sync.Mutex

	spoolMu    sync.Mutex
	spoolCache []Spool
	spoolAt    time.Time

	alerts    alertLog
	assistant assistantState
	aim       aiMonitor
	// AI is the built-in failure-detection service (Python + Obico's model).
	AI *ai.Service
	// Plugins is the Python plugin host (model-site plugins, and more later).
	Plugins *plugins.Manager
	runCtx  context.Context // set once by Run; read with runCtxOr (cfgMu)
	// extSeen: when the browser extension last reported in.
	extMu   sync.Mutex
	extSeen time.Time

	// captures: running protocol captures, keyed by printer ID.
	capMu    sync.Mutex
	captures map[string]*printers.Capture
}

func New(cfg config.Config) (*App, error) {
	// Orca runs inside each job's folder, so every path handed to it must be absolute.
	if abs, err := filepath.Abs(cfg.DataDir); err == nil {
		cfg.DataDir = abs
	}
	t0 := time.Now()
	step := func(what string) { // startup timing, to see what is slow on a given PC
		if d := time.Since(t0); d > 500*time.Millisecond {
			log.Printf("Startup: %s took %v", what, d.Round(time.Millisecond))
		}
		t0 = time.Now()
	}
	st, err := store.New(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	reg, err := printers.NewRegistry(st)
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, Store: st, Printers: reg, projects: map[string]*Project{}, render: make(chan struct{}, 2)}
	a.aim.states = map[string]*aiPrinter{}
	a.AI = ai.New(filepath.Join(cfg.DataDir, "ai"), 7623)
	a.Plugins = plugins.New(cfg.DataDir)
	a.Plugins.SetKeys(a.pluginKeys())
	a.assistant.pending = map[string]PendingAction{}
	_ = os.MkdirAll(cfg.ModelsDir(), 0o755)
	step("printers")
	if a.Library, err = library.Open(filepath.Join(cfg.DataDir, "library"), cfg.ScanFolders()); err != nil {
		return nil, err
	}
	step("library")
	a.ReloadProfiles()
	step("Orca profiles")
	if err := extension.WriteTo(ExtensionDir(), listenPort(cfg.Listen)); err != nil {
		log.Printf("browser extension: %v", err)
	}
	step("browser extension")
	var saved []*Project
	if err := st.Load("projects", &saved); err != nil {
		return nil, err
	}
	for _, p := range saved {
		a.projects[p.ID] = p
	}
	a.localSlicer = &slicer.Orca{Path: cfg.OrcaPath, Args: cfg.OrcaSliceArgs}
	a.Slicer = &workerSlicer{a}
	q, err := slicer.NewQueue(st, a.Slicer, a.prepare, a.send, cfg.MaxConcurrentSlices,
		time.Duration(cfg.SliceTimeoutMinutes)*time.Minute)
	if err != nil {
		return nil, err
	}
	a.Queue = q
	a.loadRuns()
	step("projects and queue")
	reg.OnTransition = a.onPrinterTransition
	reg.OnPrintEnd = a.onPrintEnd
	return a, nil
}

// Run starts background work (printer polling) until ctx ends.
func (a *App) Run(ctx context.Context) {
	a.cfgMu.Lock()
	a.runCtx = ctx
	a.cfgMu.Unlock()
	go a.Plugins.Start(ctx)
	go a.cameraLoop(ctx)
	go a.aiLoop(ctx)
	go a.housekeeping(ctx)
	go a.profLoop(ctx)
	go a.updLoop(ctx)
	go a.libBackupLoop(ctx)
	a.Printers.Run(ctx, time.Duration(a.cfg().PollSeconds)*time.Second)
	a.Plugins.Stop()
}

// ReloadProfiles (re)reads Orca's system presets, Orca user presets and
// PrintHarbor's own saved presets.
func (a *App) ReloadProfiles() *profiles.Library {
	l := profiles.NewLibrary()
	t0 := time.Now()
	cached, err := l.LoadDirCached(a.systemProfilesDir(), "system", filepath.Join(a.Store.Dir(), "cache", "orca-profiles.json"))
	if err != nil {
		l.Errors = append(l.Errors, "Orca profiles: "+err.Error())
	}
	how := "read the profile files"
	if cached {
		how = "from the cache"
	}
	log.Printf("Profiles: %s in %v", how, time.Since(t0).Round(time.Millisecond))
	if a.cfg().OrcaUserPresetsDir != "" {
		if err := l.LoadDir(a.cfg().OrcaUserPresetsDir, "orca-user"); err != nil {
			l.Errors = append(l.Errors, "Orca user presets: "+err.Error())
		}
	}
	harbor := a.presetsDir()
	_ = os.MkdirAll(harbor, 0o755)
	_ = l.LoadDir(harbor, "harbor")
	a.applySavedProfiles(l) // profiles kept from before an update, and the locked ones
	a.libMu.Lock()
	a.lib = l
	a.libMu.Unlock()
	return l
}

// presetsDir holds presets saved from the web UI, laid out like Orca's:
// presets/PrintHarbor/<type>/<name>.json
func (a *App) presetsDir() string { return filepath.Join(a.Store.Dir(), "presets") }

// SavePreset stores a user preset that inherits from an existing one and
// changes only the given settings, exactly like an Orca user preset.
func (a *App) SavePreset(typ, name, inherits string, overrides map[string]string) error {
	name = strings.TrimSpace(name)
	if name == "" || typ == "" || inherits == "" {
		return fmt.Errorf("type, name and base preset are required")
	}
	if typ != "process" && typ != "filament" && typ != "machine" {
		return fmt.Errorf("bad preset type %q", typ)
	}
	lib := a.Lib()
	base, err := lib.Resolve(typ, inherits)
	if err != nil {
		return err
	}
	raw := map[string]any{"type": typ, "name": name, "inherits": inherits, "from": "User", "instantiation": "true"}
	tmp := map[string]any{}
	for k := range overrides {
		tmp[k] = base[k]
	}
	profiles.ApplyOverrides(tmp, overrides)
	for k, v := range tmp {
		raw[k] = v
	}
	// Keep compatibility rules of the base so the preset shows up in the same places.
	for _, k := range []string{"compatible_printers", "compatible_printers_condition"} {
		if v, ok := base[k]; ok {
			raw[k] = v
		}
	}
	dir := filepath.Join(a.presetsDir(), "PrintHarbor", typ)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(raw, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, unsafeName.ReplaceAllString(name, "_")+".json"), b, 0o644); err != nil {
		return err
	}
	// Saved again on purpose: a lock on the old version must not hide the new one.
	_ = os.Remove(a.profFile("frozen", typ, name))
	a.ReloadProfiles()
	return nil
}

// MyPreset is one of your own profiles as the editor shows it.
type MyPreset struct {
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Inherits string            `json:"inherits"`
	Values   map[string]string `json:"values"` // what it changes from the profile it is based on
}

// myPresets lists the profiles made in PrintHarbor (typ "filament" or
// "process"), by name.
func (a *App) myPresets(typ string) []MyPreset {
	out := []MyPreset{}
	skip := map[string]bool{"type": true, "name": true, "inherits": true, "from": true, "instantiation": true,
		"compatible_printers": true, "compatible_printers_condition": true}
	for _, p := range a.Lib().List(typ) {
		if p.Source != "harbor" {
			continue
		}
		m := MyPreset{Name: p.Name, Type: p.Type, Inherits: p.Inherits, Values: map[string]string{}}
		for k, v := range p.Raw {
			if !skip[k] {
				m.Values[k] = profiles.Str(v)
			}
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// deletePreset removes one of your own profiles. Stock profiles, and ones
// that came from desktop Orca, can't be removed here.
func (a *App) deletePreset(typ, name string) error {
	p := a.Lib().Get(typ, name)
	if p == nil || p.Source != "harbor" {
		return fmt.Errorf("%q is not one of your own profiles", name)
	}
	dir := filepath.Join(a.presetsDir(), "PrintHarbor", typ)
	file := filepath.Join(dir, unsafeName.ReplaceAllString(name, "_")+".json")
	if p.File != "" && filepath.Dir(p.File) == dir {
		file = p.File
	}
	if err := os.Remove(file); err != nil {
		return err
	}
	a.ReloadProfiles()
	return nil
}

// resolveProcess is a quality preset as PrintHarbor uses it for a printer: the
// preset with its parents, then your print defaults in place of the stock
// values: the general ones (Settings → Printing), then that printer's own on
// top (printerID "" = no printer in particular). A setting one of your own
// presets sets is left as you saved it, and whatever a job or plate changes is
// applied after this, so it wins.
func (a *App) resolveProcess(lib *profiles.Library, name, printerID string) (map[string]any, error) {
	process, err := lib.Resolve("process", name)
	if err != nil {
		return nil, err
	}
	use := map[string]string{}
	for k, v := range a.cfg().PrintDefaults {
		use[k] = v
	}
	if pr, ok := a.Printers.Raw(printerID); ok && printerID != "" {
		for k, v := range pr.PrintDefaults {
			use[k] = v
		}
	}
	if len(use) > 0 {
		for k := range lib.OwnKeys("process", name) {
			delete(use, k)
		}
		profiles.ApplyOverrides(process, use)
	}
	return process, nil
}

// defaultProcessFor is the quality profile first offered for a printer: the
// one you chose for it when that still fits the printer, else fallback.
func (a *App) defaultProcessFor(printerID string, fits []string, fallback string) string {
	if pr, ok := a.Printers.Raw(printerID); ok && pr.DefaultProcess != "" && contains(fits, pr.DefaultProcess) {
		return pr.DefaultProcess
	}
	return fallback
}

// printDefaultSettings are the settings that can be given a default of your
// own: the quality settings of the friendly editor.
func printDefaultSettings() []profiles.Setting {
	out := []profiles.Setting{}
	for _, s := range profiles.Catalog {
		if s.For == "process" {
			out = append(out, s)
		}
	}
	return out
}

// cleanPrintDefaults checks a set of print defaults and returns it tidied
// (empty values dropped). Only known settings with sensible values pass: these
// go into every slice.
func cleanPrintDefaults(in map[string]string) (map[string]string, error) {
	known := map[string]profiles.Setting{}
	for _, s := range printDefaultSettings() {
		known[s.Key] = s
	}
	out := map[string]string{}
	for k, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		s, ok := known[k]
		if !ok {
			return nil, fmt.Errorf("%q is not a setting that can have a default", k)
		}
		switch s.Kind {
		case "bool":
			if v != "0" && v != "1" {
				return nil, fmt.Errorf("%s must be on or off", s.Label)
			}
		case "choice":
			ok := false
			for _, c := range s.Choices {
				ok = ok || c.Value == v
			}
			if !ok {
				return nil, fmt.Errorf("%s: %q is not one of the choices", s.Label, v)
			}
		case "percent":
			n, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64)
			if err != nil || n < 0 || n > 100 {
				return nil, fmt.Errorf("%s must be 0 to 100 %%", s.Label)
			}
			v = strconv.FormatFloat(n, 'f', -1, 64) + "%"
		case "number":
			n, err := strconv.ParseFloat(v, 64)
			if err != nil || n < 0 || n > 100000 {
				return nil, fmt.Errorf("%s must be a number", s.Label)
			}
			v = strconv.FormatFloat(n, 'f', -1, 64)
		}
		out[k] = v
	}
	return out, nil
}

func (a *App) Lib() *profiles.Library {
	a.libMu.RLock()
	defer a.libMu.RUnlock()
	return a.lib
}

// listenPort pulls the port out of a listen address like ":7621" or
// "0.0.0.0:7621" (7621 when it has none).
func listenPort(listen string) string {
	if _, p, err := net.SplitHostPort(listen); err == nil && p != "" {
		return p
	}
	return extension.DefaultPort
}

// cfg returns the current settings (a copy: change it and nothing happens;
// use setCfg). The slices in it are never changed in place, only replaced.
func (a *App) cfg() config.Config {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	return a.Cfg
}

// setCfg replaces the settings. Hold cfgEdit when it is a read-change-write.
func (a *App) setCfg(c config.Config) {
	a.cfgMu.Lock()
	a.Cfg = c
	a.cfgMu.Unlock()
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *App) saveProjectsLocked() error {
	list := make([]*Project, 0, len(a.projects))
	for _, p := range a.projects {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Uploaded.After(list[j].Uploaded) })
	return a.Store.Save("projects", list)
}

func (a *App) projectDir(id string) string {
	return filepath.Join(a.Store.Dir(), "projects", id)
}

// projectFile is the original 3MF of a 3MF project.
func (a *App) projectFile(id string) string { return filepath.Join(a.projectDir(id), "original.3mf") }

// AddProject stores and parses uploads: either one 3MF, or any number of
// STL/OBJ files (which start a new project with them on plate 1).
func (a *App) AddProject(ups []Upload) (*Project, error) {
	if len(ups) == 0 {
		return nil, fmt.Errorf("no file uploaded")
	}
	var meshes, threes int
	for _, u := range ups {
		ext := strings.ToLower(filepath.Ext(u.Name))
		switch {
		case ext == ".3mf":
			threes++
		case threemf.MeshFormats[ext]:
			meshes++
		default:
			return nil, fmt.Errorf("%s: PrintHarbor opens .3mf, .stl and .obj files", u.Name)
		}
	}
	if threes > 1 || (threes == 1 && meshes > 0) {
		return nil, fmt.Errorf("open one 3MF project at a time, or any number of STL/OBJ files")
	}
	base := strings.TrimSuffix(filepath.Base(ups[0].Name), filepath.Ext(ups[0].Name))
	if meshes > 0 {
		name := base
		if len(ups) > 1 {
			name = fmt.Sprintf("%s + %d more", base, len(ups)-1)
		}
		p, err := a.NewMeshProject(name)
		if err != nil {
			return nil, err
		}
		return a.AddMeshFiles(p.ID, 1, ups)
	}

	f, err := os.Open(ups[0].Tmp)
	if err != nil {
		return nil, err
	}
	fi, _ := f.Stat()
	data, err := threemf.Read(f, fi.Size())
	f.Close()
	if err != nil {
		return nil, err
	}
	p := &Project{ID: newID(), Name: base, Uploaded: time.Now(), Kind: "3mf", Data: data, Size: fi.Size()}
	dst := a.projectFile(p.ID)
	_ = os.MkdirAll(a.projectDir(p.ID), 0o755)
	if err := moveFile(ups[0].Tmp, dst); err != nil {
		return nil, err
	}
	_ = os.Chmod(dst, 0o444) // the original stays untouched
	a.projMu.Lock()
	defer a.projMu.Unlock()
	a.projects[p.ID] = p
	return p.clone(), a.saveProjectsLocked()
}

// moveFile moves a file, also between drives (a temp folder on one drive to a models folder on another),
// where a plain rename isn't possible. The copy is streamed: uploads and
// downloads can be gigabytes, far too big to hold in memory.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	part := dst + ".part"
	out, err := os.Create(part)
	if err != nil {
		in.Close()
		return err
	}
	_, err = io.Copy(out, in)
	in.Close()
	if e := out.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = replaceFile(part, dst)
	}
	if err != nil {
		os.Remove(part)
		return err
	}
	return os.Remove(src)
}

// replaceFile puts tmp in dst's place. The old file is only removed first when
// a straight swap isn't possible (a read-only file on Windows), so a failure
// never costs you the file you already had.
func replaceFile(tmp, dst string) error {
	err := os.Rename(tmp, dst)
	if err == nil {
		return nil
	}
	if _, serr := os.Stat(dst); serr != nil {
		return err
	}
	if rerr := os.Remove(dst); rerr != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// clone returns a copy of the project that shares nothing with it.
//
// The projects in a.projects are changed by many things at once (the page,
// auto-dispatch, the slicer saving, printers finishing), always under projMu.
// Nothing outside that lock may hold one of them: it would be reading maps
// and slices while they are written, which loses changes and can crash the
// whole program. So everything that hands a project out hands out a clone.
func (p *Project) clone() *Project {
	if p == nil {
		return nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		cp := *p
		return &cp
	}
	out := &Project{}
	if json.Unmarshal(b, out) != nil {
		cp := *p
		return &cp
	}
	return out
}

// Project returns a copy of a project as it is now. Changing the copy changes
// nothing: use the methods that change a project (they hold the lock).
func (a *App) Project(id string) (*Project, bool) {
	a.projMu.Lock()
	defer a.projMu.Unlock()
	p, ok := a.projects[id]
	return p.clone(), ok
}

// Projects returns copies of every project, newest first.
func (a *App) Projects() []*Project {
	a.projMu.Lock()
	defer a.projMu.Unlock()
	out := make([]*Project, 0, len(a.projects))
	for _, p := range a.projects {
		out = append(out, p.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Uploaded.After(out[j].Uploaded) })
	return out
}

func (a *App) DeleteProject(id string) error {
	a.projMu.Lock()
	defer a.projMu.Unlock()
	if _, ok := a.projects[id]; !ok {
		return fmt.Errorf("project not found")
	}
	delete(a.projects, id)
	dir := a.projectDir(id)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		_ = os.Chmod(filepath.Join(dir, e.Name()), 0o644) // read-only files block removal on Windows
	}
	_ = os.RemoveAll(dir)
	return a.saveProjectsLocked()
}

// Suggestion is a sensible default process and filament presets for slicing
// a plate on a printer, so the user can just press "Send".
type Suggestion struct {
	Machine   string   `json:"machine_preset"`
	Process   string   `json:"process_preset"`
	Processes []string `json:"processes"`
	Filaments []string `json:"filament_presets"` // one per project filament
	Choices   []string `json:"filament_choices"`
}

func (a *App) Suggest(p *Project, printerID string) (Suggestion, error) {
	pr, ok := a.Printers.Get(printerID)
	if !ok {
		return Suggestion{}, fmt.Errorf("printer not found")
	}
	if pr.MachinePreset == "" {
		return Suggestion{}, fmt.Errorf("printer has no Orca profile")
	}
	lib := a.Lib()
	s := Suggestion{Machine: pr.MachinePreset, Processes: []string{}, Filaments: []string{}, Choices: []string{}}
	for _, x := range lib.CompatibleList(pr.MachinePreset, "process") {
		s.Processes = append(s.Processes, x.Name)
	}
	s.Process = pickProcess(s.Processes, p.Data.SourceProcess)
	if p.Data.SourceProcess == "" { // the project doesn't say: your choice for this printer
		s.Process = a.defaultProcessFor(printerID, s.Processes, s.Process)
	}
	fils := lib.CompatibleList(pr.MachinePreset, "filament")
	for _, f := range fils {
		s.Choices = append(s.Choices, f.Name)
	}
	for _, pf := range p.Data.Filaments {
		s.Filaments = append(s.Filaments, pickFilament(lib, fils, pf))
	}
	return s, nil
}

var layerRe = regexp.MustCompile(`^(\d+\.\d+)\s*(mm)?\b`)

func pickProcess(names []string, source string) string {
	if len(names) == 0 {
		return ""
	}
	layer := func(n string) string {
		if m := layerRe.FindStringSubmatch(n); m != nil {
			return m[1]
		}
		return ""
	}
	std := func(n string) bool { return strings.Contains(strings.ToLower(n), "standard") }
	want := layer(source)
	if want == "" {
		want = "0.20"
	}
	// Same layer height as the project (Standard first), then any 0.20 Standard.
	for _, l := range []string{want, "0.20"} {
		for _, n := range names {
			if layer(n) == l && std(n) {
				return n
			}
		}
		for _, n := range names {
			if layer(n) == l {
				return n
			}
		}
	}
	return names[0]
}

func pickFilament(lib *profiles.Library, fils []*profiles.Preset, want threemf.Filament) string {
	if want.Name != "" {
		for _, f := range fils {
			if f.Name == want.Name {
				return f.Name
			}
		}
	}
	best, bestScore := "", -1
	for _, f := range fils {
		r, err := lib.Resolve("filament", f.Name)
		if err != nil {
			continue
		}
		t := profiles.Str(r["filament_type"])
		if !strings.EqualFold(t, want.Type) {
			continue
		}
		score := 1
		low := strings.ToLower(f.Name)
		if strings.Contains(low, "generic") {
			score += 2
		}
		if strings.Contains(low, "basic") {
			score++
		}
		if score > bestScore {
			best, bestScore = f.Name, score
		}
	}
	if best == "" && len(fils) > 0 {
		return fils[0].Name
	}
	return best
}

// prepare resolves a job's presets into a slice request.
func (a *App) prepare(j *slicer.Job, work string) (slicer.Request, error) {
	lib := a.Lib()
	proj, ok := a.Project(j.ProjectID)
	if !ok {
		return slicer.Request{}, fmt.Errorf("project %s is gone", j.ProjectID)
	}
	machine, err := lib.Resolve("machine", j.Machine)
	if err != nil {
		return slicer.Request{}, err
	}
	process, err := a.resolveProcess(lib, j.Process, j.PrinterID)
	if err != nil {
		return slicer.Request{}, err
	}
	procOv, filOv := splitOverrides(j.Overrides)
	profiles.ApplyOverrides(process, procOv)
	inputs, orcaPlate := []string{a.projectFile(proj.ID)}, j.Plate
	keep := false
	if sp, ok := slicerPlate(proj, j.Plate); ok {
		// Set up in the slicer page: that plate's own file, painted as saved. On the
		// printer profile it was set up for the models stay where they were placed;
		// on any other printer Orca arranges them on that bed.
		inputs, orcaPlate, keep = []string{a.slicerFile(proj, fmt.Sprintf("plate-%d.3mf", j.Plate))}, 1, j.Machine == sp.Opts.Machine
	} else if proj.Slicer != nil {
		return slicer.Request{}, fmt.Errorf("plate %d is no longer in the project (it was changed in the slicer)", j.Plate)
	} else if proj.Kind == "mesh" {
		// Only this plate's files go to Orca, as its single plate.
		inputs, orcaPlate = a.meshInputs(proj, j.Plate), 1
		if len(inputs) == 0 {
			return slicer.Request{}, fmt.Errorf("plate %d is empty", j.Plate)
		}
	}
	req := slicer.Request{Inputs: inputs, Plate: orcaPlate, Machine: machine, Process: process, WorkDir: work, KeepPlacement: keep}
	if j.Level {
		// Native-leveling drivers (FlashForge) and always-leveling ones (Bambu)
		// handle it through their print API at send time; only g-code printers
		// get the command baked into the start g-code here.
		if pr, ok := a.Printers.Raw(j.PrinterID); ok && !printers.NativeLevel(pr.Driver) && !printers.AlwaysLevels(pr.Driver) {
			req.LevelGcode = printers.LevelGcodeFor(pr)
		}
	}
	sp, saved := slicerPlate(proj, j.Plate)
	for i, name := range j.Filaments {
		f, err := lib.Resolve("filament", name)
		if err != nil {
			return slicer.Request{}, err
		}
		profiles.ApplyOverrides(f, filOv)
		if saved && i < len(sp.Opts.Filaments) && reHexColor.MatchString(sp.Opts.Filaments[i].Color) {
			f["filament_colour"] = []any{strings.ToUpper(sp.Opts.Filaments[i].Color)}
		}
		req.Filaments = append(req.Filaments, f)
	}
	return req, nil
}

// splitOverrides routes each override to the process or the filaments, using
// the settings catalog; unknown keys go to the process (the "All settings"
// editor works on the process).
func splitOverrides(ov map[string]string) (proc, fil map[string]string) {
	proc, fil = map[string]string{}, map[string]string{}
	forKey := map[string]string{}
	for _, s := range profiles.Catalog {
		forKey[s.Key] = s.For
	}
	for k, v := range ov {
		if strings.HasPrefix(k, "filament:") {
			fil[strings.TrimPrefix(k, "filament:")] = v
		} else if forKey[k] == "filament" {
			fil[k] = v
		} else {
			proc[strings.TrimPrefix(k, "process:")] = v
		}
	}
	return
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (a *App) send(ctx context.Context, j *slicer.Job) error {
	format := a.Printers.OutputFormat(j.PrinterID)
	// Klipper toolchangers / multi-extruders: point each color at the tool
	// that holds it (Bambu and FlashForge take the mapping natively).
	if pr, ok := a.Printers.Raw(j.PrinterID); ok && pr.Driver == "moonraker" && format == "gcode" && j.GCode != "" {
		if tools := slicer.ToolMap(j.SlotMap); tools != nil {
			if m, err := a.Lib().Machine(pr.MachinePreset); err == nil && m.Extruders > 1 {
				if _, err := slicer.RemapTools(j.GCode, tools); err != nil {
					return fmt.Errorf("couldn't remap tools: %w", err)
				}
			}
		}
	}
	path := j.GCode
	ext := ".gcode"
	if format == "gcode.3mf" {
		path, ext = j.Sliced3MF, ".gcode.3mf"
	}
	if path == "" {
		return fmt.Errorf("Orca did not produce a %s file", format)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	name := unsafeName.ReplaceAllString(fmt.Sprintf("%s_plate%d", j.ProjectName, j.Plate), "_") + ext
	sendCtx := printers.WithLevel(printers.WithSlotMap(ctx, j.SlotMap), j.Level)
	if err := a.Printers.Send(sendCtx, j.PrinterID, name, f, j.Start); err != nil {
		return err
	}
	if j.Start {
		a.startRun(j)
	}
	return nil
}
