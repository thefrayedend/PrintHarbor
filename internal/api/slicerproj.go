package api

// Projects and the slicer page are one workflow: a project holds the plates,
// the slicer page edits them, and saving stores each plate exactly as it was
// set up there (placement, painting, filaments, printer and quality settings).
// The queue then slices those plate files with those settings, nothing else.
//
// On disk, next to the project's original files (which stay untouched):
//
//	projects/<id>/slicer-<n>/project.3mf   the whole workspace, to reopen it
//	projects/<id>/slicer-<n>/plate-<k>.3mf one plate, packed for slicing
//	projects/<id>/slicer-<n>/plate-<k>.png its picture
//
// Every save writes a new slicer-<n> folder and then switches to it, so a
// plate that is slicing while you save keeps reading the files it started with.

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thefrayedend/printharbor/internal/fit"
	"github.com/thefrayedend/printharbor/internal/printers"
	"github.com/thefrayedend/printharbor/internal/slicer"
	"github.com/thefrayedend/printharbor/internal/threemf"
)

// SlicerProject is what the slicer page saved for a project.
type SlicerProject struct {
	Dir         string          `json:"dir"`          // slicer-<n> folder in the project folder
	Printer     string          `json:"printer"`      // printer the plates were set up for
	PrinterName string          `json:"printer_name"` // its name at the time
	Machine     string          `json:"machine"`      // Orca printer profile used
	Process     string          `json:"process"`      // Orca quality profile used
	Settings    json.RawMessage `json:"settings"`     // the page's own state (filaments, overrides, …)
	Plates      []SlicerPlate   `json:"plates"`
	SavedAt     time.Time       `json:"saved_at"`
}

// SlicerPlate is one plate as the slicer page packed it.
type SlicerPlate struct {
	Index int           `json:"index"`
	Name  string        `json:"name"`
	Opts  slicerOptions `json:"opts"`
	// Tools: the project filament each tool of the plate file prints with
	// (tool k = Tools[k-1]); Opts.Filaments is in the same order.
	Tools     []int          `json:"tools"`
	Parts     []threemf.Part `json:"parts"`
	MaxX      float64        `json:"max_x_mm"`
	MaxY      float64        `json:"max_y_mm"`
	MaxZ      float64        `json:"max_z_mm"`
	Footprint float64        `json:"footprint_mm2"`
	Painted   bool           `json:"painted"`
}

func (a *App) slicerFile(p *Project, name string) string {
	path := filepath.Join(a.projectDir(p.ID), p.Slicer.Dir, name)
	if abs, err := filepath.Abs(path); err == nil {
		return abs // Orca runs in the job folder: relative paths would point there
	}
	return path
}

// slicerPlate returns the saved plate n of a project, if the slicer saved it.
func slicerPlate(p *Project, n int) (SlicerPlate, bool) {
	if p == nil || p.Slicer == nil {
		return SlicerPlate{}, false
	}
	for _, sp := range p.Slicer.Plates {
		if sp.Index == n {
			return sp, true
		}
	}
	return SlicerPlate{}, false
}

// madeFor says whether a printer is the one a saved plate was set up for (or
// has the same Orca printer profile): it then prints exactly as saved, models
// where they were placed. Any other printer gets the plate re-sliced for it
// (see applySaved).
func madeFor(p *Project, v printers.Printer) bool {
	if p.Slicer == nil {
		return true
	}
	return v.ID == p.Slicer.Printer || (v.MachinePreset != "" && v.MachinePreset == p.Slicer.Machine)
}

// fitPlate is fit.Plate plus a note on how a plate saved in the slicer will be
// sliced for each printer. Every printer that can take the parts and colours
// qualifies: a plate is sliced for the printer it is sent to.
func (a *App) fitPlate(p *Project, pl threemf.Plate, views []printers.View, opts fit.Options) []fit.Result {
	res := fit.Plate(pl, p.Data.Filaments, views, a.Lib().Machine, opts)
	if p.Slicer == nil {
		return res
	}
	byID := map[string]printers.View{}
	for _, v := range views {
		byID[v.ID] = v
	}
	for i, r := range res {
		if r.Level == fit.Cannot {
			continue
		}
		note := "Set up in the slicer for this printer: prints exactly as saved"
		if !madeFor(p, byID[r.PrinterID].Printer) {
			note = "Sliced for this printer when sent: its own profile, the parts arranged on its bed; painting, colours and your setting changes are kept"
		}
		res[i].Reasons = append([]fit.Reason{{Kind: fit.Info, Text: note}}, r.Reasons...)
	}
	return res
}

// portableOverrides: the setting changes of a saved plate that make sense on
// any printer (walls, infill, supports …). Bed type, prime tower spot and what
// the page set only because of that printer stay behind.
func portableOverrides(sp SlicerPlate) map[string]string {
	skip := map[string]bool{"curr_bed_type": true, "wipe_tower_x": true, "wipe_tower_y": true}
	for _, k := range sp.Opts.Auto {
		skip[k] = true
	}
	out := map[string]string{}
	for k, v := range sp.Opts.Overrides {
		if !skip[k] {
			out[k] = v
		}
	}
	return out
}

// applySaved fills a queue job for a plate saved in the slicer.
//
// The printer it was set up for: exactly the saved printer and quality
// profiles, filaments, settings and prime tower spot, models where placed.
//
// Any other printer: that printer's own profile, the quality profile and
// filament profiles picked for it (by the dialog, or matched by PrintHarbor
// like for any project), the saved setting changes that make sense on any
// printer; Orca arranges the parts on its bed (prepare). Painting and colours
// travel in the plate file.
//
// j.SlotMap comes in project filament order (as fit and the Choose printer
// dialog make it); j.Filaments in project filament order or already in the
// plate's tool order. Both leave in tool order.
func (a *App) applySaved(p *Project, j *slicer.Job) error {
	sp, ok := slicerPlate(p, j.Plate)
	if !ok {
		return nil
	}
	pr, found := a.Printers.Get(j.PrinterID)
	if !found {
		return fmt.Errorf("printer not found")
	}
	toolOrder := func(projOrder []string) []string {
		out := make([]string, len(sp.Tools))
		for k, f := range sp.Tools {
			if f >= 1 && f <= len(projOrder) {
				out[k] = projOrder[f-1]
			}
		}
		return out
	}
	if madeFor(p, pr.Printer) {
		j.Machine, j.Process = sp.Opts.Machine, sp.Opts.Process
		j.Filaments = nil
		for _, f := range sp.Opts.Filaments {
			j.Filaments = append(j.Filaments, f.Preset)
		}
		j.Overrides = map[string]string{}
		for k, v := range sp.Opts.Overrides {
			j.Overrides[k] = v
		}
		if sp.Opts.Tower != nil {
			j.Overrides["wipe_tower_x"] = strconv.FormatFloat(sp.Opts.Tower.X, 'f', 3, 64)
			j.Overrides["wipe_tower_y"] = strconv.FormatFloat(sp.Opts.Tower.Y, 'f', 3, 64)
		}
	} else {
		if pr.MachinePreset == "" {
			return fmt.Errorf("%s has no Orca printer profile yet: set it under All Printers", pr.Name)
		}
		j.Machine = pr.MachinePreset
		var sug Suggestion
		if j.Process == "" || (len(j.Filaments) != len(sp.Tools) && len(j.Filaments) != len(p.Data.Filaments)) {
			s, err := a.Suggest(p, pr.ID)
			if err != nil {
				return err
			}
			sug = s
		}
		if j.Process == "" {
			j.Process = sug.Process
		}
		if j.Process == "" {
			return fmt.Errorf("no quality profile for %s", pr.Name)
		}
		switch len(j.Filaments) {
		case len(sp.Tools): // already one per tool (the dialog)
		case len(p.Data.Filaments):
			j.Filaments = toolOrder(j.Filaments)
		default:
			j.Filaments = toolOrder(sug.Filaments)
		}
		extra := j.Overrides
		j.Overrides = portableOverrides(sp)
		for k, v := range extra {
			j.Overrides[k] = v
		}
	}
	slots := make([]int, len(sp.Tools))
	for k, f := range sp.Tools {
		if f >= 1 && f <= len(j.SlotMap) {
			slots[k] = j.SlotMap[f-1]
		}
	}
	j.SlotMap = slots
	return nil
}

type slicerSaveMeta struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Printer     string          `json:"printer"`
	PrinterName string          `json:"printer_name"`
	Machine     string          `json:"machine"`
	Process     string          `json:"process"`
	Settings    json.RawMessage `json:"settings"`
	Filaments   []struct {
		Color  string `json:"color"`
		Preset string `json:"preset"`
		Type   string `json:"type"`
	} `json:"filaments"`
	Plates []SlicerPlate `json:"plates"`
}

func saveUpload(fh *multipart.FileHeader, dst string) error {
	src, err := fh.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// maxFilaments is the most colours/filaments a project may have. It is high
// enough for big multi-AMS tool changers (24+ colours) with headroom; the
// slicer page uses the same ceiling (MAX_FILAMENTS in web/slicer/profiles.js).
const maxFilaments = 64

// SaveFromSlicer stores what the slicer page sent: a new project (meta.ID
// empty) or a new version of an existing one.
func (a *App) SaveFromSlicer(meta slicerSaveMeta, files map[string][]*multipart.FileHeader) (*Project, error) {
	if len(meta.Plates) == 0 || len(meta.Plates) > 64 {
		return nil, fmt.Errorf("nothing to save: put a model on a plate first")
	}
	if len(meta.Filaments) == 0 || len(meta.Filaments) > maxFilaments {
		return nil, fmt.Errorf("pick 1 to %d filaments", maxFilaments)
	}
	lib := a.Lib()
	if _, err := lib.Resolve("machine", meta.Machine); err != nil {
		return nil, fmt.Errorf("unknown printer profile %q", meta.Machine)
	}
	for k, sp := range meta.Plates {
		if sp.Index != k+1 {
			return nil, fmt.Errorf("plates must be numbered 1 to %d", len(meta.Plates))
		}
		if len(files[fmt.Sprintf("plate-%d", sp.Index)]) == 0 {
			return nil, fmt.Errorf("plate %d was not received", sp.Index)
		}
		if len(sp.Tools) == 0 || len(sp.Tools) != len(sp.Opts.Filaments) {
			return nil, fmt.Errorf("plate %d: filaments and tools don't match", sp.Index)
		}
		for _, t := range sp.Tools {
			if t < 1 || t > len(meta.Filaments) {
				return nil, fmt.Errorf("plate %d uses filament %d, the project has %d", sp.Index, t, len(meta.Filaments))
			}
		}
		if _, err := lib.Resolve("process", sp.Opts.Process); err != nil {
			return nil, fmt.Errorf("plate %d: unknown quality profile %q", sp.Index, sp.Opts.Process)
		}
		for _, f := range sp.Opts.Filaments {
			if _, err := lib.Resolve("filament", f.Preset); err != nil {
				return nil, fmt.Errorf("plate %d: unknown filament profile %q", sp.Index, f.Preset)
			}
		}
	}
	if len(files["project"]) == 0 {
		return nil, fmt.Errorf("the project file was not received")
	}

	var p *Project
	if meta.ID != "" {
		got, ok := a.Project(meta.ID)
		if !ok {
			return nil, fmt.Errorf("that project is gone (deleted on the Projects page?). Save it as a new project")
		}
		p = got
	} else {
		name := strings.TrimSpace(meta.Name)
		if name == "" {
			name = "Slicer project"
		}
		p = &Project{ID: newID(), Name: name, Uploaded: time.Now(), Kind: "slicer", Data: &threemf.Project{}}
	}
	base := a.projectDir(p.ID)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, err
	}
	// next free slicer-<n>
	n := 1
	if p.Slicer != nil {
		if v, err := strconv.Atoi(strings.TrimPrefix(p.Slicer.Dir, "slicer-")); err == nil {
			n = v + 1
		}
	}
	for ; ; n++ {
		if _, err := os.Stat(filepath.Join(base, fmt.Sprintf("slicer-%d", n))); os.IsNotExist(err) {
			break
		}
	}
	dirName := fmt.Sprintf("slicer-%d", n)
	dir := filepath.Join(base, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dir)
		}
	}()
	if err := saveUpload(files["project"][0], filepath.Join(dir, "project.3mf")); err != nil {
		return nil, err
	}
	var total int64
	for _, sp := range meta.Plates {
		dst := filepath.Join(dir, fmt.Sprintf("plate-%d.3mf", sp.Index))
		if err := saveUpload(files[fmt.Sprintf("plate-%d", sp.Index)][0], dst); err != nil {
			return nil, err
		}
		if fi, err := os.Stat(dst); err == nil {
			total += fi.Size()
		}
		if th := files[fmt.Sprintf("thumb-%d", sp.Index)]; len(th) > 0 {
			_ = saveUpload(th[0], filepath.Join(dir, fmt.Sprintf("plate-%d.png", sp.Index)))
		}
	}

	// The plates as the rest of PrintHarbor sees them (fit, progress, dispatch).
	data := &threemf.Project{SourcePrinter: meta.PrinterName, SourceProcess: meta.Process}
	for i, f := range meta.Filaments {
		data.Filaments = append(data.Filaments, threemf.Filament{Index: i + 1, Color: strings.ToUpper(f.Color), Type: f.Type, Name: f.Preset})
	}
	plates := make([]SlicerPlate, 0, len(meta.Plates))
	for _, sp := range meta.Plates {
		sp.MaxX, sp.MaxY, sp.MaxZ = round1(sp.MaxX), round1(sp.MaxY), round1(sp.MaxZ)
		sp.Opts.Printer, sp.Opts.Machine = meta.Printer, meta.Machine
		for k := range sp.Parts {
			sp.Parts[k].ObjectID, sp.Parts[k].Instance = k+1, 1
			sp.Parts[k].SizeX, sp.Parts[k].SizeY, sp.Parts[k].SizeZ = round1(sp.Parts[k].SizeX), round1(sp.Parts[k].SizeY), round1(sp.Parts[k].SizeZ)
		}
		used := append([]int(nil), sp.Tools...)
		// fit checks every part against a bed (like any project): on other printers
		// Orca arranges the parts, so each part must fit, not the saved layout.
		dp := threemf.Plate{Index: sp.Index, Name: sp.Name, Parts: sp.Parts, Filaments: used, Painted: false, FootprintMM2: math.Round(sp.Footprint)}
		for _, pt := range sp.Parts {
			dp.MaxX, dp.MaxY, dp.MaxZ = math.Max(dp.MaxX, pt.SizeX), math.Max(dp.MaxY, pt.SizeY), math.Max(dp.MaxZ, pt.SizeZ)
		}
		data.Plates = append(data.Plates, dp)
		plates = append(plates, sp)
	}
	sv := &SlicerProject{Dir: dirName, Printer: meta.Printer, PrinterName: meta.PrinterName, Machine: meta.Machine, Process: meta.Process,
		Settings: meta.Settings, Plates: plates, SavedAt: time.Now()}

	a.projMu.Lock()
	// p is a copy (or brand new). The change goes onto the project as it is NOW,
	// so nothing changed meanwhile (copies, pinned spools, the name) is undone.
	if cur, exists := a.projects[p.ID]; exists {
		p = cur
	} else if meta.ID != "" {
		a.projMu.Unlock()
		return nil, fmt.Errorf("that project is gone (deleted on the Projects page?). Save it as a new project")
	}
	p.Slicer, p.Data = sv, data
	if p.Kind == "slicer" {
		p.Size = total
	}
	a.projects[p.ID] = p
	err := a.saveProjectsLocked()
	p = p.clone()
	a.projMu.Unlock()
	if err != nil {
		return nil, err
	}
	ok = true
	// Earlier versions: removed unless a plate is still slicing from them
	// (Windows keeps open files); the next save tries again.
	if entries, err := os.ReadDir(base); err == nil {
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "slicer-") && e.Name() != dirName {
				_ = os.RemoveAll(filepath.Join(base, e.Name()))
			}
		}
	}
	return p, nil
}

func (a *App) slicerProjectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/projects/slicer", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			slicerFail(w, 400, "Could not read what the slicer sent: "+err.Error())
			return
		}
		defer r.MultipartForm.RemoveAll()
		var meta slicerSaveMeta
		if err := json.Unmarshal([]byte(r.FormValue("meta")), &meta); err != nil {
			slicerFail(w, 400, "Bad project data: "+err.Error())
			return
		}
		p, err := a.SaveFromSlicer(meta, r.MultipartForm.File)
		if err != nil {
			slicerFail(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "project": p})
	})
	// What the slicer page needs to reopen a project: its saved state and
	// where to read the workspace, or (never saved) the project's own files.
	mux.HandleFunc("GET /api/projects/{id}/slicer", func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.Project(r.PathValue("id"))
		if !ok {
			slicerFail(w, 404, "That project is gone.")
			return
		}
		file := "/api/projects/" + p.ID + "/slicer/file"
		out := map[string]any{"ok": true, "id": p.ID, "name": p.Name}
		switch {
		case p.Slicer != nil:
			out["saved"], out["file"] = p.Slicer, file
		case p.Kind == "mesh":
			var plates []map[string]any
			for _, pl := range p.Data.Plates {
				e := map[string]any{"plate": pl.Index}
				if len(a.meshInputs(p, pl.Index)) > 0 {
					e["file"] = fmt.Sprintf("%s?plate=%d", file, pl.Index)
				}
				plates = append(plates, e)
			}
			out["plates"] = plates
		default:
			out["file"] = file
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/projects/{id}/slicer/file", func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.Project(r.PathValue("id"))
		if !ok {
			slicerFail(w, 404, "That project is gone.")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/octet-stream")
		if p.Slicer != nil || p.Kind != "mesh" {
			path := a.projectFile(p.ID)
			if p.Slicer != nil {
				path = a.slicerFile(p, "project.3mf")
			}
			w.Header().Set("X-File-Name", url.PathEscape(safeName(p.Name, "project")+".3mf"))
			http.ServeFile(w, r, path)
			return
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("plate"))
		var parts []MeshPart
		for _, mp := range p.MeshParts {
			if mp.Plate == n {
				parts = append(parts, mp)
			}
		}
		nameOf := func(mp MeshPart) string { return safeName(mp.Name, "part") + filepath.Ext(mp.File) }
		switch len(parts) {
		case 0:
			slicerFail(w, 404, "That plate is empty.")
		case 1:
			w.Header().Set("X-File-Name", url.PathEscape(nameOf(parts[0])))
			http.ServeFile(w, r, filepath.Join(a.projectDir(p.ID), parts[0].File))
		default:
			w.Header().Set("X-File-Name", url.PathEscape(safeName(p.Name, "models")+".zip"))
			zw := zip.NewWriter(w)
			used := map[string]bool{}
			for _, mp := range parts {
				name := nameOf(mp)
				for i := 2; used[strings.ToLower(name)]; i++ {
					name = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(nameOf(mp), filepath.Ext(mp.File)), i, filepath.Ext(mp.File))
				}
				used[strings.ToLower(name)] = true
				wr, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
				if err != nil {
					break
				}
				if in, err := os.Open(filepath.Join(a.projectDir(p.ID), mp.File)); err == nil {
					_, _ = io.Copy(wr, in)
					in.Close()
				}
			}
			zw.Close()
		}
	})
	mux.HandleFunc("GET /api/projects/{id}/plates/{n}/slicer.png", func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.Project(r.PathValue("id"))
		n, _ := strconv.Atoi(r.PathValue("n"))
		if !ok || p.Slicer == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, a.slicerFile(p, fmt.Sprintf("plate-%d.png", n)))
	})
}
