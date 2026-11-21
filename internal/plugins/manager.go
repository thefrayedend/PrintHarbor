// Package plugins runs PrintHarbor's Python plugin host and adapts the plugins
// it loads to PrintHarbor's own interfaces. Today that means model-site plugins
// (they implement internal/sites.Site); the same host is where other plugin
// kinds will live. See docs/plugins/.
//
// Layout on disk, under <data>/plugins:
//
//	.runtime/host.py, phlib.py   the host and the helper library plugins import
//	example/, thingiverse/, …    one folder per plugin (plugin.json + code)
//
// The host is a small local HTTP server (127.0.0.1); PrintHarbor calls it over
// JSON-RPC. If Python isn't installed the manager stays dormant and the
// built-in Go sites carry on — plugins are an enhancement, never required.
package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed runtime builtin
var assets embed.FS

// SettingField describes one configurable value a plugin exposes, both for a
// plugin's own settings and for a tool plugin's run() parameters. The UI renders
// a form from these; the values are handed back to the plugin via ctx["settings"]
// (settings) or as run() params.
type SettingField struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"` // text, password, number, bool, select
	Default any      `json:"default"`
	Help    string   `json:"help"`
	Options []string `json:"options"` // for type "select"
	Secret  bool     `json:"secret"`  // render masked; a password is secret by default
}

// Manifest is a plugin's plugin.json plus what the host learned about it.
type Manifest struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Kind      string         `json:"kind"` // "site" or "tool"
	URL       string         `json:"url"`
	Version   string         `json:"version"`    // the plugin's own version, e.g. "1.2.0"
	UpdateURL string         `json:"update_url"` // https URL to a .zip or a {version,zip} JSON manifest
	NeedsKey  bool           `json:"needs_key"`
	Key       string         `json:"key"` // which Settings key it reads (e.g. "thingiverse_token")
	KeyHelp   string         `json:"key_help"`
	CanDL     bool           `json:"can_download"`
	Disabled  bool           `json:"disabled"` // off by default in its manifest (e.g. the example template)
	Off       bool           `json:"off"`      // effective: user turned it off, else the manifest default
	Builtin   bool           `json:"builtin"`  // ships with PrintHarbor: can be turned off, not removed
	Match     []string       `json:"match"`    // URL regexes, group 1 = model id
	Note      string         `json:"note"`
	Settings  []SettingField `json:"settings"`  // the plugin's own configurable values
	Params    []SettingField `json:"params"`    // a tool plugin's run() inputs
	Values    map[string]any `json:"values"`    // current settings values (defaults merged with the user's saved choices)
	Functions []string       `json:"functions"` // which of search/model/download/match/run/status/… it defines
	Events    []string       `json:"events"`    // which lifecycle events it wants (empty = all; "*" = all); needs an on_event function
	// Permissions is what this plugin can touch, for the user to see before turning
	// it on. Declared in plugin.json ("permissions": ["network", ...]); PrintHarbor
	// also infers the obvious ones (a printer plugin controls printers; an on_event
	// plugin sees print events). Transparency, not a sandbox — a plugin runs Python.
	Permissions []string `json:"permissions"`
	// Driver and Output are for kind "printer": how the Add-printer screen shows
	// it, and the sliced-file format it wants ("gcode" or "gcode.3mf").
	Driver *DriverMeta `json:"driver"`
	Output string      `json:"output"`
	Error  string      `json:"error"` // set when the plugin failed to load
	Dir    string      `json:"dir"`
}

// DriverMeta is a printer plugin's entry in the Add-printer screen.
type DriverMeta struct {
	AddressHint string `json:"address_hint"`
	NeedsKey    bool   `json:"needs_key"`
	KeyLabel    string `json:"key_label"`
	NeedsSerial bool   `json:"needs_serial"`
	Help        string `json:"help"`
}

// Manager owns the host process and the loaded plugins.
type Manager struct {
	dir      string // <data>/plugins
	client   *http.Client
	mu       sync.RWMutex
	port     int
	proc     *exec.Cmd
	man      []Manifest
	keys     map[string]string
	userOff  map[string]bool           // plugin id → user turned it off (overrides the manifest default)
	settings map[string]map[string]any // plugin id → saved settings values
	builtin  map[string]bool           // ids that ship with PrintHarbor (from the embedded set)
	started  bool
	off      string // why plugins are unavailable (e.g. no Python), for the UI

	// Events wait here for the one worker that delivers them in order (events.go).
	evOnce  sync.Once
	evQueue chan map[string]any

	// life serialises Start/Stop/Reload: two installs at once must not leave
	// two plugin hosts running.
	life sync.Mutex
}

func (m *Manager) setOff(why string) {
	m.mu.Lock()
	m.off = why
	m.mu.Unlock()
}

// reID is what a plugin id may look like. It becomes a folder name, so no
// dots-only names, separators, drive letters or the reserved ".runtime".
var reID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidID reports whether id is usable as a plugin id (and folder name).
func ValidID(id string) bool { return reID.MatchString(id) }

func New(dataDir string) *Manager {
	m := &Manager{
		dir:      filepath.Join(dataDir, "plugins"),
		client:   &http.Client{Timeout: 45 * time.Second},
		keys:     map[string]string{},
		userOff:  map[string]bool{},
		settings: map[string]map[string]any{},
		builtin:  map[string]bool{},
	}
	if entries, err := fs.ReadDir(assets, "builtin"); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				m.builtin[e.Name()] = true
			}
		}
	}
	return m
}

// SetKeys gives plugins the per-site credentials from Settings.
func (m *Manager) SetKeys(k map[string]string) {
	m.mu.Lock()
	m.keys = k
	m.mu.Unlock()
}

func (m *Manager) keysCopy() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string, len(m.keys))
	for k, v := range m.keys {
		out[k] = v
	}
	return out
}

// Start writes the bundled runtime and plugins, finds Python, launches the
// host and loads the plugin list. A missing Python is not an error.
func (m *Manager) Start(ctx context.Context) {
	m.life.Lock()
	defer m.life.Unlock()
	m.start(ctx)
}

func (m *Manager) start(ctx context.Context) {
	m.setOff("") // a reload that works clears an earlier problem
	if err := m.writeAssets(); err != nil {
		m.setOff("couldn't set up the plugins folder: " + err.Error())
		return
	}
	m.loadOff()
	m.loadSettings()
	py, args := findPython(ctx)
	if py == "" {
		m.setOff("Python 3 isn't installed, so plugins are off. Install Python 3 (python.org) to use model-site and other plugins.")
		return
	}
	port, err := freePort()
	if err != nil {
		m.setOff(err.Error())
		return
	}
	hostPy := filepath.Join(m.dir, ".runtime", "host.py")
	cmd := exec.Command(py, append(append([]string{}, args...), hostPy, "--port", fmt.Sprint(port), "--plugins", m.dir)...)
	hidePyWindow(cmd)
	if err := cmd.Start(); err != nil {
		m.setOff("couldn't start the plugin host: " + err.Error())
		return
	}
	m.mu.Lock()
	m.proc, m.port, m.started = cmd, port, true
	m.mu.Unlock()
	go func() { _ = cmd.Wait() }()
	// Wait for the host to answer, then load the manifests.
	for i := 0; i < 60; i++ {
		if m.load() {
			log.Printf("Plugins: loaded %d from %s", len(m.List()), m.dir)
			RegisterPrinterDrivers(m) // printer plugins become selectable printer drivers
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	m.setOff("the plugin host didn't start in time")
	m.stop() // don't leave a half-started host behind
}

// Stop ends the host process.
func (m *Manager) Stop() {
	m.life.Lock()
	defer m.life.Unlock()
	m.stop()
}

func (m *Manager) stop() {
	m.mu.Lock()
	p := m.proc
	m.proc = nil
	m.mu.Unlock()
	if p != nil && p.Process != nil {
		_ = p.Process.Kill()
	}
}

func (m *Manager) baseURL() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return fmt.Sprintf("http://127.0.0.1:%d", m.port)
}

// load fetches /health and caches the manifests. Returns true on success.
func (m *Manager) load() bool {
	req, _ := http.NewRequest("GET", m.baseURL()+"/health", nil)
	resp, err := m.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		OK      bool       `json:"ok"`
		Plugins []Manifest `json:"plugins"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || !out.OK {
		return false
	}
	for i := range out.Plugins {
		out.Plugins[i].Permissions = effectivePermissions(out.Plugins[i])
	}
	m.mu.Lock()
	m.man = out.Plugins
	m.mu.Unlock()
	return true
}

// KnownPermissions maps each permission a plugin can declare to a short, plain
// description shown to the user. Unknown permissions are still shown as-is.
var KnownPermissions = map[string]string{
	"network":  "Reach the internet and your local network",
	"files":    "Read and write files on your computer",
	"printers": "Control your printers (send prints, pause, cancel)",
	"camera":   "Use your printer cameras",
	"events":   "See when prints start, finish and fail",
	"settings": "Read your PrintHarbor settings and site keys",
}

// permOrder is the stable display order.
var permOrder = []string{"printers", "camera", "network", "files", "events", "settings"}

// effectivePermissions is what a plugin can touch: what it declared, plus the
// obvious inferences (a printer plugin controls printers; an on_event plugin
// sees events; a plugin that reads a site key reads settings). Deduped, ordered.
func effectivePermissions(man Manifest) []string {
	set := map[string]bool{}
	for _, p := range man.Permissions {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			set[p] = true
		}
	}
	if man.Kind == "printer" {
		set["printers"] = true
	}
	for _, fn := range man.Functions {
		switch fn {
		case "on_event":
			set["events"] = true
		case "snapshot":
			set["camera"] = true
		}
	}
	if man.NeedsKey {
		set["settings"] = true
	}
	var out []string
	for _, p := range permOrder {
		if set[p] {
			out = append(out, p)
			delete(set, p)
		}
	}
	for p := range set { // any unknown ones the author declared, after the known set
		out = append(out, p)
	}
	return out
}

// Reload rescans the plugins folder (after a plugin is added or changed).
func (m *Manager) Reload(ctx context.Context) {
	m.life.Lock()
	defer m.life.Unlock()
	m.stop()
	m.mu.Lock()
	m.started = false
	m.man = nil
	m.mu.Unlock()
	// Not the caller's context: a reload started by a web request must not be
	// abandoned half way when that browser tab goes away.
	m.start(context.WithoutCancel(ctx))
}

// List returns every plugin the host reported (including ones that failed),
// each with Off set to its effective state (user override, else manifest default).
func (m *Manager) List() []Manifest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Manifest, len(m.man))
	for i, man := range m.man {
		if v, ok := m.userOff[man.ID]; ok {
			man.Off = v
		} else {
			man.Off = man.Disabled
		}
		man.Builtin = m.builtin[man.ID]
		man.Values = mergeValues(man.Settings, m.settings[man.ID])
		out[i] = man
	}
	return out
}

// Uninstall removes a user-installed plugin's folder. Built-in plugins can't be
// removed (turn them off instead).
func (m *Manager) Uninstall(id string) error {
	// A plugin that failed to load is listed under its folder name, which may
	// not be a tidy id, so this is looser than ValidID: just never a path.
	if id == "" || strings.HasPrefix(id, ".") || strings.ContainsAny(id, `/\:`) {
		return fmt.Errorf("that isn't a plugin id")
	}
	if m.builtin[id] {
		return fmt.Errorf("built-in plugins can be turned off, not removed")
	}
	m.mu.RLock()
	dir := ""
	for _, man := range m.man {
		if man.ID == id {
			dir = man.Dir
		}
	}
	m.mu.RUnlock()
	if dir == "" {
		dir = filepath.Join(m.dir, id)
	}
	base, _ := filepath.Abs(m.dir)
	abs, _ := filepath.Abs(dir)
	if abs == base || !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to remove that folder")
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.userOff, id)
	m.mu.Unlock()
	return nil
}

// Install unpacks a plugin .zip into the plugins folder. The zip must contain a
// plugin.json; the plugin's id becomes its folder. Returns the installed id.
func (m *Manager) Install(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("that isn't a valid .zip file")
	}
	manPath := ""
	for _, f := range zr.File {
		if filepath.Base(f.Name) == "plugin.json" {
			if manPath == "" || strings.Count(f.Name, "/") < strings.Count(manPath, "/") {
				manPath = f.Name
			}
		}
	}
	if manPath == "" {
		return "", fmt.Errorf("the zip has no plugin.json — it isn't a PrintHarbor plugin")
	}
	prefix := ""
	if i := strings.LastIndex(manPath, "/"); i >= 0 {
		prefix = manPath[:i+1]
	}
	mf, err := zr.Open(manPath)
	if err != nil {
		return "", err
	}
	mb, _ := io.ReadAll(io.LimitReader(mf, 1<<20))
	mf.Close()
	var meta struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(mb, &meta)
	id := strings.TrimSpace(meta.ID)
	// The id becomes a folder under the plugins folder. Anything but a plain
	// name is refused: "." would wipe the whole plugins folder and ".runtime"
	// would replace the plugin host itself.
	if !ValidID(id) {
		return "", fmt.Errorf("the plugin's id is missing or invalid (use letters, digits, - and _)")
	}
	if m.builtin[id] {
		return "", fmt.Errorf("%q is a built-in plugin; turn it off instead of replacing it", id)
	}
	// Unpack next to the final place first and swap it in only when the whole
	// zip came out: a bad zip must not destroy the copy that's already installed.
	dest := filepath.Join(m.dir, id)
	stage, err := os.MkdirTemp(m.dir, ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	root, _ := filepath.Abs(stage)
	var total int64
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, prefix) {
			continue
		}
		rel := f.Name[len(prefix):]
		if rel == "" {
			continue
		}
		target := filepath.Join(stage, filepath.FromSlash(rel))
		abs, _ := filepath.Abs(target)
		// Inside this plugin's own folder, not just somewhere under plugins\
		// (which would let a zip overwrite another plugin).
		if !strings.HasPrefix(abs, root+string(os.PathSeparator)) || strings.Contains(rel, ":") {
			return "", fmt.Errorf("the zip tried to write outside its own plugin folder")
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if total += int64(f.UncompressedSize64); f.UncompressedSize64 > maxPluginFile || total > 4*maxPluginFile {
			return "", fmt.Errorf("the plugin is too big (over %d MB unpacked)", 4*maxPluginFile>>20)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return "", err
		}
		_, cerr := io.Copy(out, io.LimitReader(rc, maxPluginFile+1))
		if e := out.Close(); cerr == nil {
			cerr = e
		}
		rc.Close()
		if cerr != nil {
			return "", cerr
		}
	}
	if _, err := os.Stat(filepath.Join(stage, "plugin.json")); err != nil {
		return "", fmt.Errorf("the zip has no plugin.json — it isn't a PrintHarbor plugin")
	}
	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("couldn't replace the installed copy: %w", err)
	}
	// Windows can refuse a rename for a moment while a virus scanner looks at
	// the new files; try for a couple of seconds before giving up.
	for i := 0; ; i++ {
		if err = os.Rename(stage, dest); err == nil || i >= 8 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		return "", fmt.Errorf("couldn't put the plugin in place: %w", err)
	}
	return id, nil
}

// maxPluginFile is the biggest single file a plugin zip may hold.
const maxPluginFile = 50 << 20

// SetOff turns a plugin on or off for the user, and remembers it on disk.
func (m *Manager) SetOff(id string, off bool) {
	m.mu.Lock()
	if m.userOff == nil {
		m.userOff = map[string]bool{}
	}
	m.userOff[id] = off
	cp := make(map[string]bool, len(m.userOff))
	for k, v := range m.userOff {
		cp[k] = v
	}
	m.mu.Unlock()
	b, _ := json.MarshalIndent(cp, "", "  ")
	_ = os.WriteFile(filepath.Join(m.dir, "off.json"), b, 0o644)
}

// OffIDs is the set of plugin ids that are off right now — the user turned them
// off, or (when the user hasn't chosen) the manifest ships them off. Used to
// hide a site entirely when its plugin is off, instead of falling back to the
// built-in Go site (that fallback is only for when a plugin fails or Python is
// missing, not when the user deliberately turns it off).
func (m *Manager) OffIDs() map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]bool{}
	for id, off := range m.userOff {
		if off {
			out[id] = true
		}
	}
	for _, man := range m.man {
		if _, ok := m.userOff[man.ID]; !ok && man.Disabled {
			out[man.ID] = true
		}
	}
	return out
}

// loadOff reads the user's on/off choices from disk.
func (m *Manager) loadOff() {
	b, err := os.ReadFile(filepath.Join(m.dir, "off.json"))
	if err != nil {
		return
	}
	var mp map[string]bool
	if json.Unmarshal(b, &mp) == nil && mp != nil {
		m.mu.Lock()
		m.userOff = mp
		m.mu.Unlock()
	}
}

// mergeValues returns the effective settings values: each field's saved value,
// or its manifest default when the user hasn't set one. Unknown saved keys are
// dropped so a renamed field can't linger.
func mergeValues(fields []SettingField, saved map[string]any) map[string]any {
	out := map[string]any{}
	for _, f := range fields {
		if v, ok := saved[f.Key]; ok {
			out[f.Key] = v
		} else if f.Default != nil {
			out[f.Key] = f.Default
		}
	}
	return out
}

// settingsFor is the values map handed to a plugin at call time.
func (m *Manager) settingsFor(id string) map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var fields []SettingField
	for _, man := range m.man {
		if man.ID == id {
			fields = man.Settings
			break
		}
	}
	return mergeValues(fields, m.settings[id])
}

// SetSettings saves a plugin's settings values (only keys it declares are kept),
// and persists them. Returns the merged effective values.
func (m *Manager) SetSettings(id string, values map[string]any) map[string]any {
	m.mu.Lock()
	var fields []SettingField
	for _, man := range m.man {
		if man.ID == id {
			fields = man.Settings
			break
		}
	}
	keep := map[string]any{}
	for _, f := range fields {
		if v, ok := values[f.Key]; ok {
			keep[f.Key] = v
		}
	}
	if m.settings == nil {
		m.settings = map[string]map[string]any{}
	}
	m.settings[id] = keep
	cp := make(map[string]map[string]any, len(m.settings))
	for k, v := range m.settings {
		cp[k] = v
	}
	out := mergeValues(fields, keep)
	m.mu.Unlock()
	b, _ := json.MarshalIndent(cp, "", "  ")
	_ = os.WriteFile(filepath.Join(m.dir, "settings.json"), b, 0o644)
	return out
}

// loadSettings reads saved per-plugin settings from disk.
func (m *Manager) loadSettings() {
	b, err := os.ReadFile(filepath.Join(m.dir, "settings.json"))
	if err != nil {
		return
	}
	var mp map[string]map[string]any
	if json.Unmarshal(b, &mp) == nil && mp != nil {
		m.mu.Lock()
		m.settings = mp
		m.mu.Unlock()
	}
}

// Off explains why plugins are unavailable, or "" when they're fine.
func (m *Manager) Off() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.off
}

// rpc calls a plugin function on the host.
func (m *Manager) rpc(plugin, fn string, args map[string]any, out any) error {
	return m.rpcWithin(context.Background(), 0, plugin, fn, args, out)
}

// rpcWithin is rpc with the caller's context and its own time limit. limit 0
// is the usual one (the client's, 45 s), right for a search or a status. Sending
// a sliced file to a printer can take many minutes over Wi-Fi and gets a long
// limit; an event handler gets a short one so a stuck plugin can't hold up the
// events behind it.
func (m *Manager) rpcWithin(ctx context.Context, limit time.Duration, plugin, fn string, args map[string]any, out any) error {
	client := m.client
	if limit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
		client = &http.Client{Transport: m.client.Transport} // no fixed limit: the context decides
	}
	body, _ := json.Marshal(map[string]any{
		"plugin": plugin, "fn": fn,
		"ctx":  map[string]any{"keys": m.keysCopy(), "settings": m.settingsFor(plugin)},
		"args": args,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", m.baseURL()+"/rpc", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("the plugin took too long to answer (%s)", fn)
		}
		return fmt.Errorf("plugin host not responding: %w", err)
	}
	defer resp.Body.Close()
	var env struct {
		OK     bool            `json:"ok"`
		Error  string          `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if !env.OK {
		return fmt.Errorf("%s", env.Error)
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// writeAssets lays the bundled runtime and plugins onto disk. Bundled plugins
// are refreshed each start; any other folder (a user's own plugin) is left be.
func (m *Manager) writeAssets() error {
	writeTree := func(srcDir, dstDir string) error {
		return fs.WalkDir(assets, srcDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(srcDir, p)
			dst := filepath.Join(dstDir, rel)
			if d.IsDir() {
				return os.MkdirAll(dst, 0o755)
			}
			b, err := assets.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(dst, b, 0o644)
		})
	}
	if err := writeTree("runtime", filepath.Join(m.dir, ".runtime")); err != nil {
		return err
	}
	// builtin/<id>/... → <dir>/<id>/...
	return writeTree("builtin", m.dir)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// findPython returns a Python 3 command (and any leading args), or "".
func findPython(ctx context.Context) (string, []string) {
	type cand struct {
		name string
		args []string
	}
	var cands []cand
	if runtime.GOOS == "windows" {
		cands = []cand{{"py", []string{"-3"}}, {"python", nil}, {"python3", nil}}
	} else {
		cands = []cand{{"python3", nil}, {"python", nil}}
	}
	for _, c := range cands {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.CommandContext(cctx, c.name, append(append([]string{}, c.args...), "--version")...)
		hidePyWindow(cmd)
		err := cmd.Run()
		cancel()
		if err == nil {
			return c.name, c.args
		}
	}
	return "", nil
}

// Dir is the plugins folder on disk, for the UI ("add your plugin here").
func (m *Manager) Dir() string { return m.dir }
