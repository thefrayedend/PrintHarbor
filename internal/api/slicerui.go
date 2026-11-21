package api

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thefrayedend/printharbor/internal/library"
	"github.com/thefrayedend/printharbor/internal/printers"
	"github.com/thefrayedend/printharbor/internal/profiles"
	"github.com/thefrayedend/printharbor/internal/slicer"
)

// The slicer page (web/slicer: FlashBridge's Prepare / Paint / Preview front
// end) talks to these /api/slicer addresses, the same ones FlashBridge's board
// served. Here they are answered with PrintHarbor's OrcaSlicer and its
// profiles: the page builds each plate as a 3MF (models placed, colours,
// supports and seams painted) and PrintHarbor slices it with the chosen
// printer, quality and filament presets plus the settings you changed.

//go:embed orca-settings.json
var orcaSettingsJSON []byte

type pageSliceJob struct {
	Name       string         `json:"name"`
	Plate      int            `json:"plate"`
	Printer    string         `json:"printer"`
	State      string         `json:"state"` // slicing | done | error | cancelled
	Percent    float64        `json:"percent"`
	Message    string         `json:"message"`
	Error      string         `json:"error,omitempty"`
	Warnings   []string       `json:"warnings"`
	Stats      map[string]any `json:"stats,omitempty"`
	Seconds    int            `json:"seconds"`
	HasProject bool           `json:"hasProject"`

	started time.Time
	gcode   string
	sliced  string
	cancel  context.CancelFunc
}

type slicerUI struct {
	mu        sync.Mutex
	jobs      map[int]*pageSliceJob
	running   int
	inbox     map[string]inboxFile
	schema    []map[string]any
	schemaLib *profiles.Library // the profile set the schema was worked out from
}

type inboxFile struct {
	path, name string
	at         time.Time
}

var sui = &slicerUI{jobs: map[int]*pageSliceJob{}, inbox: map[string]inboxFile{}}

func (j *pageSliceJob) view() pageSliceJob {
	c := *j
	if c.State == "slicing" {
		c.Seconds = int(time.Since(j.started).Seconds())
	}
	if c.Warnings == nil {
		c.Warnings = []string{}
	}
	return c
}

func slicerFail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"ok": false, "message": msg})
}

func (a *App) slicerDir() string {
	d := filepath.Join(a.cfg().DataDir, "slicer")
	if abs, err := filepath.Abs(d); err == nil {
		return abs
	}
	return d
}

// machineFor picks the printer's machine preset (or another nozzle of the
// same printer model when the page asks for one).
func (a *App) slicerPrinterInfo(id, machine, model string) (map[string]any, error) {
	pr, ok := a.Printers.Raw(id)
	if !ok {
		return nil, fmt.Errorf("Unknown printer.")
	}
	lib := a.Lib()
	out := map[string]any{"ok": true, "printer": id, "name": pr.Name}
	// Models people can pick when the printer has no preset yet.
	models := map[string]bool{}
	byModel := map[string][]string{}
	for _, p := range lib.List("machine") {
		if !p.Instantiation {
			continue
		}
		r, err := lib.Resolve("machine", p.Name)
		if err != nil {
			continue
		}
		m := profiles.Str(r["printer_model"])
		if m == "" {
			continue
		}
		models[m] = true
		byModel[m] = append(byModel[m], p.Name)
	}
	if model != "" {
		// Picked on the page: remember it for this printer.
		if list := byModel[model]; len(list) > 0 {
			sort.Strings(list)
			pick := list[0]
			for _, n := range list {
				if strings.Contains(n, "0.4") {
					pick = n
					break
				}
			}
			_ = a.Printers.Tweak(id, func(p *printers.Printer) { p.MachinePreset = pick })
			pr.MachinePreset = pick
		}
	}
	base := pr.MachinePreset
	if base == "" || lib.Get("machine", base) == nil {
		names := make([]string, 0, len(models))
		for m := range models {
			names = append(names, m)
		}
		sort.Strings(names)
		out["known"], out["models"] = false, names
		return out, nil
	}
	rb, _ := lib.Resolve("machine", base)
	modelName := profiles.Str(rb["printer_model"])
	nozzles := append([]string{}, byModel[modelName]...)
	if len(nozzles) == 0 {
		nozzles = []string{base}
	}
	sort.Strings(nozzles)
	use := base
	if machine != "" {
		for _, n := range nozzles {
			if n == machine {
				use = machine
			}
		}
	}
	info, err := lib.Machine(use)
	if err != nil {
		return nil, err
	}
	rm, _ := lib.Resolve("machine", use)
	var procs, fils []string
	for _, p := range lib.CompatibleList(use, "process") {
		procs = append(procs, p.Name)
	}
	for _, p := range lib.CompatibleList(use, "filament") {
		fils = append(fils, p.Name)
	}
	sort.Strings(procs)
	sort.Strings(fils)
	def := profiles.Str(rm["default_print_profile"])
	if !contains(procs, def) {
		def = ""
		for _, p := range procs {
			if strings.Contains(p, "0.20mm Standard") {
				def = p
				break
			}
		}
		if def == "" && len(procs) > 0 {
			def = procs[0]
		}
	}
	def = a.defaultProcessFor(id, procs, def) // the one you chose for this printer, when you did
	x0, y0 := 0.0, 0.0
	for i, pt := range info.BedPolygon {
		if i == 0 || pt[0] < x0 {
			x0 = pt[0]
		}
		if i == 0 || pt[1] < y0 {
			y0 = pt[1]
		}
	}
	h := info.MaxZ
	if h <= 0 {
		h = 250
	}
	ext := info.Extruders
	if ext < 1 {
		ext = 1
	}
	single := profiles.Str(rm["single_extruder_multi_material"]) == "1" && ext == 1
	out["known"] = true
	out["model"] = firstNonEmptyStr(modelName, use)
	out["models"] = []string{modelName}
	out["machine"] = use
	out["nozzles"] = nozzles
	out["processes"] = nonNil(procs)
	out["defaultProcess"] = def
	out["filaments"] = nonNil(fils)
	out["defaultBedType"] = profiles.Str(rm["default_bed_type"])
	out["bed"] = map[string]float64{"x0": x0, "y0": y0, "w": info.BedX, "d": info.BedY, "h": h}
	out["extruders"] = ext
	out["singleNozzleMulti"] = single
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// slicerSchema is Orca's own list of print settings (labels, types, ranges),
// limited to the ones a quality preset holds and that can be typed in.
func (a *App) slicerSchema() []map[string]any {
	// The list depends on the Orca profiles, so it is kept only for the profile
	// set it was worked out from: when the profiles are reloaded (the folder was
	// changed in Settings, a preset was saved) it is worked out again. It used to
	// be kept until restart, so a list built before the profiles had loaded, or
	// for the old folder, stayed wrong or nearly empty.
	lib := a.Lib()
	sui.mu.Lock()
	defer sui.mu.Unlock()
	if sui.schema != nil && sui.schemaLib == lib {
		return sui.schema
	}
	var all []map[string]any
	_ = json.Unmarshal(orcaSettingsJSON, &all)
	// Which settings are process-scope — i.e. things a person changes per print,
	// not printer or filament settings. FlashBridge reads scope=="process"
	// straight from the engine; stock Orca doesn't expose scope, so we derive the
	// same set from the process presets. It must be the RESOLVED preset (inherits
	// applied): the raw preset holds only the handful of keys it overrides, so
	// using those dropped every setting left at its profile default — which is why
	// options were missing here that FlashBridge shows. Process presets share a
	// common base that defines every process option, so resolving a sample covers
	// the full set; raw keys are the fallback if a preset won't resolve.
	inProcess := map[string]bool{}
	procs := lib.List("process")
	// A sample spread over the whole list (every vendor, not just the first
	// names in the alphabet): presets share bases, so ~200 cover every option.
	step := len(procs)/200 + 1
	for i, p := range procs {
		if i%step != 0 {
			continue
		}
		if resolved, err := lib.Resolve("process", p.Name); err == nil {
			for k := range resolved {
				inProcess[k] = true
			}
		} else {
			for k := range p.Raw {
				inProcess[k] = true
			}
		}
	}
	ok := map[string]bool{"float": true, "int": true, "bool": true, "enum": true, "percent": true, "float_or_percent": true}
	out := []map[string]any{}
	for _, s := range all {
		k, _ := s["key"].(string)
		t, _ := s["type"].(string)
		if s["vector"] == true || !ok[t] || strings.Contains(k, "gcode") || strings.Contains(k, "post_process") || strings.Contains(k, "filename") {
			continue
		}
		if !inProcess[k] && k != "curr_bed_type" {
			continue
		}
		out = append(out, s)
	}
	if len(procs) > 0 { // nothing loaded yet: answer, but work it out again next time
		sui.schema, sui.schemaLib = out, lib
	}
	return out
}

// resolveForSlicer: machine + process presets with the page's settings applied.
func (a *App) slicerResolved(machine, process, printerID string) (map[string]any, error) {
	lib := a.Lib()
	m, err := lib.Resolve("machine", machine)
	if err != nil {
		return nil, err
	}
	p, err := a.resolveProcess(lib, process, printerID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	for k, v := range p {
		out[k] = v
	}
	return out, nil
}

type slicerOptions struct {
	Printer   string `json:"printer"`
	Machine   string `json:"machine"`
	Process   string `json:"process"`
	Filaments []struct {
		Preset string `json:"preset"`
		Color  string `json:"color"`
	} `json:"filaments"`
	Overrides map[string]string `json:"overrides"`
	Tower     *struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	} `json:"tower"`
	Name string `json:"name"`
	// Auto: overrides the page set only because of the printer it was set up
	// for (no room for the prime tower …); left out on other printers.
	Auto []string `json:"auto,omitempty"`
}

var reHexColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func (a *App) startSlicerJob(plate int, model string, o slicerOptions) (*pageSliceJob, error) {
	lib := a.Lib()
	if _, err := os.Stat(a.cfg().OrcaPath); err != nil {
		return nil, fmt.Errorf("OrcaSlicer wasn't found at %s. Set its path in Settings.", a.cfg().OrcaPath)
	}
	machine, err := lib.Resolve("machine", o.Machine)
	if err != nil {
		return nil, fmt.Errorf("unknown printer profile %q", o.Machine)
	}
	process, err := a.resolveProcess(lib, o.Process, o.Printer)
	if err != nil {
		return nil, fmt.Errorf("unknown quality profile %q", o.Process)
	}
	ov := map[string]string{}
	for k, v := range o.Overrides {
		ov[k] = v
	}
	if o.Tower != nil {
		ov["wipe_tower_x"] = strconv.FormatFloat(o.Tower.X, 'f', 3, 64)
		ov["wipe_tower_y"] = strconv.FormatFloat(o.Tower.Y, 'f', 3, 64)
	}
	profiles.ApplyOverrides(process, ov)
	var fils []map[string]any
	if len(o.Filaments) == 0 || len(o.Filaments) > maxFilaments {
		return nil, fmt.Errorf("pick 1 to %d filaments", maxFilaments)
	}
	for _, f := range o.Filaments {
		r, err := lib.Resolve("filament", f.Preset)
		if err != nil {
			return nil, fmt.Errorf("unknown filament profile %q", f.Preset)
		}
		if reHexColor.MatchString(f.Color) {
			r["filament_colour"] = []any{strings.ToUpper(f.Color)}
		}
		fils = append(fils, r)
	}
	name := strings.TrimSpace(o.Name)
	if name == "" {
		name = fmt.Sprintf("Plate %d", plate)
	}
	j := &pageSliceJob{Name: name + ".gcode", Plate: plate, Printer: o.Printer, State: "slicing", Message: "Slicing…", started: time.Now(), Warnings: []string{}}
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	sui.mu.Lock()
	if sui.running != 0 {
		if r := sui.jobs[sui.running]; r != nil && r.State == "slicing" {
			sui.mu.Unlock()
			cancel()
			return nil, fmt.Errorf("plate %d is still slicing", sui.running)
		}
	}
	sui.jobs[plate], sui.running = j, plate
	sui.mu.Unlock()
	work := filepath.Join(a.slicerDir(), fmt.Sprintf("job-%d", plate))
	go func() {
		defer cancel()
		res, err := a.localSlicer.Slice(ctx, slicer.Request{Inputs: []string{model}, Plate: 1, Machine: machine, Process: process, Filaments: fils, WorkDir: work, KeepPlacement: true})
		sui.mu.Lock()
		defer sui.mu.Unlock()
		if sui.running == plate {
			sui.running = 0
		}
		if j.State == "cancelled" {
			return
		}
		j.Seconds = int(time.Since(j.started).Seconds())
		if err != nil {
			j.State, j.Error, j.Message = "error", err.Error(), "Slicing failed"
			return
		}
		j.gcode, j.sliced = res.GCode, res.Sliced3MF
		j.HasProject = res.Sliced3MF != ""
		if j.HasProject {
			j.Name = name + ".gcode.3mf"
		}
		j.Stats = gcodeSummary(res.GCode)
		j.Warnings = append(j.Warnings, res.Notes...)
		j.State, j.Percent, j.Message = "done", 100, "Done"
	}()
	return j, nil
}

var (
	reFilMM   = regexp.MustCompile(`(?m)^;\s*filament used \[mm\]\s*=\s*([0-9., ]+)`)
	reFilG    = regexp.MustCompile(`(?m)^;\s*filament used \[g\]\s*=\s*([0-9., ]+)`)
	reTotalG  = regexp.MustCompile(`(?m)^;\s*total filament used \[g\]\s*=\s*([0-9.]+)`)
	reLayers  = regexp.MustCompile(`(?m)^;\s*total layer(?:s| number| count)\s*[:=]\s*([0-9]+)`)
	reChanges = regexp.MustCompile(`(?m)^;\s*total filament change\s*[:=]\s*([0-9]+)`)
	reTool    = regexp.MustCompile(`^T([0-9]+)\s*$`)
)

func numList(s string) []float64 {
	var out []float64
	for _, p := range strings.Split(s, ",") {
		if v, err := strconv.ParseFloat(strings.TrimSpace(p), 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// gcodeSummary reads print time, filament per tool, layers and colour
// changes from the G-code (Orca's summary comments, or by counting).
func gcodeSummary(path string) map[string]any {
	st := map[string]any{}
	if path == "" {
		return st
	}
	secs, grams := slicer.GCodeStats(path)
	st["time"] = fmtDuration(secs)
	st["seconds"] = secs
	f, err := os.Open(path)
	if err != nil {
		return st
	}
	defer f.Close()
	fi, _ := f.Stat()
	st["bytes"] = fi.Size()
	layers, changes, lastTool := 0, 0, -1
	var comments strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, ";") {
			if strings.HasPrefix(line, ";LAYER_CHANGE") {
				layers++
			} else if strings.Contains(line, "filament used") || strings.Contains(line, "total layer") || strings.Contains(line, "total filament") {
				comments.WriteString(line + "\n")
			}
			continue
		}
		// Tool switches in the start G-code are not colour changes.
		if m := reTool.FindStringSubmatch(strings.TrimSpace(line)); m != nil && layers > 0 {
			t, _ := strconv.Atoi(m[1])
			if lastTool >= 0 && t != lastTool {
				changes++
			}
			lastTool = t
		} else if m != nil {
			lastTool, _ = strconv.Atoi(m[1])
		}
	}
	c := comments.String()
	if m := reLayers.FindStringSubmatch(c); m != nil {
		layers, _ = strconv.Atoi(m[1])
	}
	if m := reChanges.FindStringSubmatch(c); m != nil {
		changes, _ = strconv.Atoi(m[1])
	}
	gs := []float64{}
	if m := reFilG.FindStringSubmatch(c); m != nil {
		gs = numList(m[1])
	}
	ms := []string{}
	if m := reFilMM.FindStringSubmatch(c); m != nil {
		for _, v := range numList(m[1]) {
			ms = append(ms, strconv.FormatFloat(v/1000, 'f', 2, 64))
		}
	}
	total := grams
	if m := reTotalG.FindStringSubmatch(c); m != nil {
		total, _ = strconv.ParseFloat(m[1], 64)
	}
	if len(gs) == 0 && total > 0 {
		gs = []float64{total}
	}
	st["grams"], st["meters"], st["totalGrams"], st["layers"], st["changes"] = gs, ms, total, layers, changes
	return st
}

func fmtDuration(s int) string {
	if s <= 0 {
		return "–"
	}
	h, m := s/3600, (s%3600)/60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%ds", s)
}

func newInboxID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// addInbox keeps a file for the slicer page to open (kept a day, at most 8).
func (a *App) addInbox(path, name string) string {
	id := newInboxID()
	sui.mu.Lock()
	defer sui.mu.Unlock()
	for k, f := range sui.inbox {
		if time.Since(f.at) > 24*time.Hour {
			os.Remove(f.path)
			delete(sui.inbox, k)
		}
	}
	for len(sui.inbox) >= 8 {
		oldest := ""
		for k, f := range sui.inbox {
			if oldest == "" || f.at.Before(sui.inbox[oldest].at) {
				oldest = k
			}
		}
		os.Remove(sui.inbox[oldest].path)
		delete(sui.inbox, oldest)
	}
	sui.inbox[id] = inboxFile{path: path, name: name, at: time.Now()}
	return id
}

// inboxFiles packs files for the slicer: one file as it is, several as a ZIP.
func (a *App) inboxFiles(title string, paths []string) (string, string, error) {
	dir := filepath.Join(a.slicerDir(), "inbox")
	_ = os.MkdirAll(dir, 0o755)
	if len(paths) == 1 {
		dst := filepath.Join(dir, newInboxID()+filepath.Ext(paths[0]))
		if err := copyFile(paths[0], dst); err != nil {
			return "", "", err
		}
		return dst, filepath.Base(paths[0]), nil
	}
	dst := filepath.Join(dir, newInboxID()+".zip")
	out, err := os.Create(dst)
	if err != nil {
		return "", "", err
	}
	zw := zip.NewWriter(out)
	used := map[string]bool{}
	for _, p := range paths {
		name := filepath.Base(p)
		for i := 2; used[strings.ToLower(name)]; i++ {
			name = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)), i, filepath.Ext(p))
		}
		used[strings.ToLower(name)] = true
		wr, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err == nil {
			if in, err := os.Open(p); err == nil {
				_, _ = io.Copy(wr, in)
				in.Close()
			}
		}
	}
	zw.Close()
	out.Close()
	return dst, safeName(title, "models") + ".zip", nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func plateParam(r *http.Request) int {
	p, _ := strconv.Atoi(r.URL.Query().Get("plate"))
	if p < 1 {
		p = 1
	}
	if p > 99 {
		p = 99
	}
	return p
}

func (a *App) slicerUIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/slicer", func(w http.ResponseWriter, r *http.Request) {
		_, err := os.Stat(a.cfg().OrcaPath)
		sui.mu.Lock()
		jobs := map[string]pageSliceJob{}
		for k, j := range sui.jobs {
			jobs[strconv.Itoa(k)] = j.view()
		}
		sui.mu.Unlock()
		out := map[string]any{"ok": true, "enabled": true, "installed": err == nil, "engine": "OrcaSlicer", "supported": true,
			"jobs": jobs, "memTotalMB": totalMemoryMB()}
		if err != nil {
			out["message"] = "OrcaSlicer wasn't found at " + a.cfg().OrcaPath + "."
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/slicer/printer", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		out, err := a.slicerPrinterInfo(q.Get("id"), q.Get("machine"), q.Get("model"))
		if err != nil {
			slicerFail(w, 200, err.Error())
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/slicer/resolved", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		v, err := a.slicerResolved(q.Get("machine"), q.Get("process"), q.Get("id"))
		if err != nil {
			slicerFail(w, 200, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "values": v})
	})
	mux.HandleFunc("GET /api/slicer/schema", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "settings": a.slicerSchema()})
	})
	mux.HandleFunc("POST /api/slicer/slice", func(w http.ResponseWriter, r *http.Request) {
		plate := plateParam(r)
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			slicerFail(w, 200, "Could not read the plate: "+err.Error())
			return
		}
		defer r.MultipartForm.RemoveAll()
		var o slicerOptions
		if err := json.Unmarshal([]byte(r.FormValue("options")), &o); err != nil {
			slicerFail(w, 200, "Bad slicing options.")
			return
		}
		fh := r.MultipartForm.File["model"]
		if len(fh) == 0 {
			slicerFail(w, 200, "No plate received.")
			return
		}
		work := filepath.Join(a.slicerDir(), fmt.Sprintf("job-%d", plate))
		_ = os.RemoveAll(work)
		_ = os.MkdirAll(work, 0o755)
		model := filepath.Join(work, "plate.3mf")
		src, err := fh[0].Open()
		if err != nil {
			slicerFail(w, 200, err.Error())
			return
		}
		out, err := os.Create(model)
		if err == nil {
			_, err = io.Copy(out, src)
			out.Close()
		}
		src.Close()
		if err != nil {
			slicerFail(w, 200, err.Error())
			return
		}
		j, err := a.startSlicerJob(plate, model, o)
		if err != nil {
			slicerFail(w, 200, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "job": j.view()})
	})
	mux.HandleFunc("GET /api/slicer/job", func(w http.ResponseWriter, r *http.Request) {
		plate := plateParam(r)
		sui.mu.Lock()
		defer sui.mu.Unlock()
		var job any
		if j := sui.jobs[plate]; j != nil {
			job = j.view()
		}
		var running any
		if sui.running != 0 {
			running = sui.running
		}
		writeJSON(w, 200, map[string]any{"ok": true, "job": job, "running": running})
	})
	mux.HandleFunc("POST /api/slicer/job/cancel", func(w http.ResponseWriter, r *http.Request) {
		sui.mu.Lock()
		if j := sui.jobs[sui.running]; j != nil && j.State == "slicing" {
			j.State, j.Message = "cancelled", "Cancelled"
			if j.cancel != nil {
				j.cancel()
			}
		}
		sui.running = 0
		sui.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/slicer/gcode", func(w http.ResponseWriter, r *http.Request) {
		plate := plateParam(r)
		sui.mu.Lock()
		j := sui.jobs[plate]
		var view pageSliceJob
		if j != nil {
			view = j.view()
			view.gcode, view.sliced = j.gcode, j.sliced
		}
		sui.mu.Unlock()
		if j == nil || view.State != "done" || view.gcode == "" {
			slicerFail(w, 404, "Nothing sliced yet.")
			return
		}
		path, ctype := view.gcode, "text/plain; charset=utf-8"
		dl := r.URL.Query().Get("download")
		w.Header().Set("Cache-Control", "no-store")
		if dl == "3mf" && view.sliced != "" {
			path, ctype = view.sliced, "application/octet-stream"
			w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(view.Name, `"`, "")+`"`)
		} else if dl != "" {
			w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(strings.TrimSuffix(strings.TrimSuffix(view.Name, ".3mf"), ".gcode"), `"`, "")+`.gcode"`)
		}
		w.Header().Set("Content-Type", ctype)
		http.ServeFile(w, r, path)
	})
	// "Print" on the page: checks the slice; the page then asks before sending.
	mux.HandleFunc("POST /api/slicer/send", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Printer string `json:"printer"`
			Plate   int    `json:"plate"`
		}
		_ = readJSON(r, &in)
		sui.mu.Lock()
		j := sui.jobs[in.Plate]
		sui.mu.Unlock()
		switch {
		case j == nil || j.State != "done":
			slicerFail(w, 200, "Slice the plate first.")
		case j.Printer != in.Printer:
			slicerFail(w, 200, "This plate was sliced for another printer.")
		default:
			writeJSON(w, 200, map[string]any{"ok": true, "file": strconv.Itoa(in.Plate) + "|" + j.Name})
		}
	})
	mux.HandleFunc("POST /api/slicer/print", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Printer string `json:"printer"`
			Plate   int    `json:"plate"`
		}
		_ = readJSON(r, &in)
		sui.mu.Lock()
		j := sui.jobs[in.Plate]
		var gcode, sliced, name, printer string
		if j != nil {
			gcode, sliced, name, printer = j.gcode, j.sliced, j.Name, j.Printer
		}
		sui.mu.Unlock()
		if j == nil || gcode == "" {
			slicerFail(w, 200, "Slice the plate first.")
			return
		}
		if printer != in.Printer {
			slicerFail(w, 200, "This plate was sliced for another printer.")
			return
		}
		path, ext := gcode, ".gcode"
		if a.Printers.OutputFormat(in.Printer) == "gcode.3mf" && sliced != "" {
			path, ext = sliced, ".gcode.3mf"
		}
		f, err := os.Open(path)
		if err != nil {
			slicerFail(w, 200, "The sliced file is missing. Slice again.")
			return
		}
		defer f.Close()
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".3mf"), ".gcode")
		file := unsafeName.ReplaceAllString(base, "_") + ext
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		if err := a.Printers.Send(ctx, in.Printer, file, f, true); err != nil {
			slicerFail(w, 200, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "file": file})
	})
	// Files for the page to open: uploads, or library / project files.
	mux.HandleFunc("POST /api/slicer/inbox", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			slicerFail(w, 200, "Expected a file upload.")
			return
		}
		defer r.MultipartForm.RemoveAll()
		var fh = r.MultipartForm.File["file"]
		if len(fh) == 0 {
			slicerFail(w, 200, "No file received.")
			return
		}
		ext := strings.ToLower(filepath.Ext(fh[0].Filename))
		if ext != ".3mf" && ext != ".stl" && ext != ".obj" && ext != ".zip" {
			slicerFail(w, 200, "Only 3MF, STL, OBJ (or a ZIP of them) can be opened in the slicer.")
			return
		}
		dir := filepath.Join(a.slicerDir(), "inbox")
		_ = os.MkdirAll(dir, 0o755)
		dst := filepath.Join(dir, newInboxID()+ext)
		src, _ := fh[0].Open()
		out, err := os.Create(dst)
		if err == nil {
			_, err = io.Copy(out, src)
			out.Close()
		}
		src.Close()
		if err != nil {
			slicerFail(w, 200, err.Error())
			return
		}
		id := a.addInbox(dst, filepath.Base(fh[0].Filename))
		writeJSON(w, 200, map[string]any{"ok": true, "id": id, "name": filepath.Base(fh[0].Filename), "open": "#slicer/prepare/inbox=" + id})
	})
	mux.HandleFunc("POST /api/slicer/inbox/library", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Item  string   `json:"item"`
			Files []string `json:"files"`
		}
		if err := readJSON(r, &in); err != nil {
			slicerFail(w, 400, err.Error())
			return
		}
		it, ok := a.Library.Get(in.Item)
		if !ok {
			slicerFail(w, 404, "That model isn't in the library any more.")
			return
		}
		var paths []string
		for _, n := range in.Files {
			if !library.Printable(n) {
				slicerFail(w, 400, n+" can't be opened in the slicer (STL, OBJ or 3MF only).")
				return
			}
			p, err := a.Library.LocalPath(in.Item, n)
			if err != nil {
				slicerFail(w, 400, err.Error())
				return
			}
			paths = append(paths, p)
		}
		if len(paths) == 0 {
			slicerFail(w, 400, "No files chosen.")
			return
		}
		path, name, err := a.inboxFiles(it.Title, paths)
		if err != nil {
			slicerFail(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "id": a.addInbox(path, name)})
	})
	mux.HandleFunc("POST /api/slicer/inbox/project", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Project string `json:"project"`
			Plate   int    `json:"plate"`
		}
		if err := readJSON(r, &in); err != nil {
			slicerFail(w, 400, err.Error())
			return
		}
		p, ok := a.Project(in.Project)
		if !ok {
			slicerFail(w, 404, "That project is gone.")
			return
		}
		var paths []string
		if p.Kind == "mesh" {
			paths = a.meshInputs(p, in.Plate)
		} else {
			paths = []string{a.projectFile(p.ID)}
		}
		if len(paths) == 0 {
			slicerFail(w, 400, "That plate is empty.")
			return
		}
		path, name, err := a.inboxFiles(p.Name, paths)
		if err != nil {
			slicerFail(w, 500, err.Error())
			return
		}
		if len(paths) == 1 && p.Kind != "mesh" {
			name = safeName(p.Name, "project") + ".3mf"
		}
		writeJSON(w, 200, map[string]any{"ok": true, "id": a.addInbox(path, name)})
	})
	mux.HandleFunc("GET /api/slicer/inbox/{id}", func(w http.ResponseWriter, r *http.Request) {
		sui.mu.Lock()
		f, ok := sui.inbox[r.PathValue("id")]
		sui.mu.Unlock()
		if !ok {
			slicerFail(w, 404, "That file is gone (files sent to the slicer are kept for a day).")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-File-Name", url.PathEscape(f.name))
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, f.path)
	})
}
