package api

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thefrayedend/printharbor/internal/appver"
	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/fit"
	"github.com/thefrayedend/printharbor/internal/printers"
	"github.com/thefrayedend/printharbor/internal/profiles"
	"github.com/thefrayedend/printharbor/internal/slicer"
	"github.com/thefrayedend/printharbor/internal/threemf"
	"github.com/thefrayedend/printharbor/internal/tray"
)

const maxUpload = 2 << 30 // 2 GB

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(v)
}

// Handler returns the full HTTP handler: API plus the static web UI.
func (a *App) Handler(web fs.FS) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		lib := a.Lib()
		_, orcaErr := os.Stat(a.cfg().OrcaPath)
		writeJSON(w, 200, map[string]any{
			"version":               Version,
			"build_number":          Build, // which versions\ folder this exe is (see Build)
			"orca_path":             a.cfg().OrcaPath,
			"orca_found":            orcaErr == nil,
			"profiles_dir":          a.cfg().OrcaProfilesDir,
			"printer_profiles":      len(lib.List("machine")),
			"process_profiles":      len(lib.List("process")),
			"filament_profiles":     len(lib.List("filament")),
			"profile_errors":        lib.Errors,
			"max_concurrent_slices": a.cfg().MaxConcurrentSlices,
			"show_printer_light":    a.cfg().ShowPrinterLight,
			"assistant":             a.cfg().OllamaURL != "",
			"spoolman":              a.cfg().SpoolmanURL != "",
			"spoolman_writeback":    a.cfg().SpoolmanWriteback,
			"can_restart":           a.Restart != nil,
			"ai":                    a.aiAvailable(),
			"started_at":            a.StartedAt,
			"app_build":             appver.Build, // the app window build made alongside this server
			"build":                 buildID(),    // changes with every build of the server (pages left open notice an update)
		})
	})

	// --- profiles ---
	mux.HandleFunc("GET /api/profiles/vendors", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, a.Lib().Vendors())
	})
	mux.HandleFunc("GET /api/profiles/machine", func(w http.ResponseWriter, r *http.Request) {
		m, err := a.Lib().Machine(r.URL.Query().Get("name"))
		if err != nil {
			fail(w, 404, err)
			return
		}
		writeJSON(w, 200, m)
	})
	mux.HandleFunc("GET /api/profiles/compatible", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		typ := q.Get("type")
		if typ != "process" && typ != "filament" {
			fail(w, 400, fmt.Errorf("type must be process or filament"))
			return
		}
		out := []map[string]string{}
		for _, p := range a.Lib().CompatibleList(q.Get("machine"), typ) {
			out = append(out, map[string]string{"name": p.Name, "source": p.Source, "vendor": p.Vendor})
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/profiles/resolve", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res, err := a.Lib().Resolve(q.Get("type"), q.Get("name"))
		if err == nil && q.Get("type") == "process" {
			// what a slice will really use: with your print defaults
			res, err = a.resolveProcess(a.Lib(), q.Get("name"), q.Get("printer"))
		}
		if err != nil {
			fail(w, 404, err)
			return
		}
		writeJSON(w, 200, res)
	})
	// Your print defaults (Settings → Printing): your own values in place of
	// the stock profiles' for the common quality settings.
	// ?printer=<id>: that printer's own (they go on top of the general ones),
	// with the quality profiles that fit it and the one first offered.
	mux.HandleFunc("GET /api/print-defaults", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"settings": printDefaultSettings()}
		general := a.cfg().PrintDefaults
		if general == nil {
			general = map[string]string{}
		}
		out["defaults"] = general
		if id := r.URL.Query().Get("printer"); id != "" {
			pr, ok := a.Printers.Raw(id)
			if !ok {
				fail(w, 404, fmt.Errorf("printer not found"))
				return
			}
			own := pr.PrintDefaults
			if own == nil {
				own = map[string]string{}
			}
			procs := []string{}
			for _, x := range a.Lib().CompatibleList(pr.MachinePreset, "process") {
				procs = append(procs, x.Name)
			}
			sort.Strings(procs)
			out["defaults"], out["general"] = own, general
			out["processes"], out["default_process"] = procs, pr.DefaultProcess
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/print-defaults", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Printer        string            `json:"printer"`
			DefaultProcess string            `json:"default_process"`
			Defaults       map[string]string `json:"defaults"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		defs, err := cleanPrintDefaults(in.Defaults)
		if err != nil {
			fail(w, 400, err)
			return
		}
		if in.Printer != "" {
			pr, ok := a.Printers.Raw(in.Printer)
			if !ok {
				fail(w, 404, fmt.Errorf("printer not found"))
				return
			}
			in.DefaultProcess = strings.TrimSpace(in.DefaultProcess)
			if in.DefaultProcess != "" {
				fits := false
				for _, x := range a.Lib().CompatibleList(pr.MachinePreset, "process") {
					fits = fits || x.Name == in.DefaultProcess
				}
				if !fits {
					fail(w, 400, fmt.Errorf("%q is not a quality profile for %s", in.DefaultProcess, pr.Name))
					return
				}
			}
			if err := a.Printers.Tweak(in.Printer, func(p *printers.Printer) {
				p.PrintDefaults, p.DefaultProcess = defs, in.DefaultProcess // a new map: the old one is never changed
			}); err != nil {
				fail(w, 500, err)
				return
			}
			writeJSON(w, 200, map[string]any{"defaults": defs, "default_process": in.DefaultProcess})
			return
		}
		a.cfgEdit.Lock()
		c := a.cfg()
		c.PrintDefaults = defs // a new map: the old one is never changed
		a.setCfg(c)
		var err2 error
		if a.CfgPath != "" {
			err2 = config.Save(a.CfgPath, a.cfg())
		}
		a.cfgEdit.Unlock()
		if err2 != nil {
			fail(w, 500, err2)
			return
		}
		writeJSON(w, 200, map[string]any{"defaults": defs})
	})
	// Your own profiles (made here, kept in data\presets): list, and remove.
	// Saving one is POST /api/profiles/save below; saving under the same name
	// replaces it.
	mux.HandleFunc("GET /api/profiles/mine", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, a.myPresets(r.URL.Query().Get("type")))
	})
	mux.HandleFunc("DELETE /api/profiles/mine", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if err := a.deletePreset(q.Get("type"), q.Get("name")); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/profiles/catalog", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, profiles.Catalog)
	})
	mux.HandleFunc("POST /api/profiles/reload", func(w http.ResponseWriter, r *http.Request) {
		l := a.ReloadProfiles()
		writeJSON(w, 200, map[string]any{"printers": len(l.List("machine")), "errors": l.Errors})
	})
	mux.HandleFunc("POST /api/profiles/save", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Type      string            `json:"type"`
			Name      string            `json:"name"`
			Inherits  string            `json:"inherits"`
			Overrides map[string]string `json:"overrides"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		if err := a.SavePreset(in.Type, in.Name, in.Inherits, in.Overrides); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]string{"saved": in.Name})
	})

	// --- printers ---
	mux.HandleFunc("GET /api/drivers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, printers.Drivers())
	})
	mux.HandleFunc("GET /api/printers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, a.PrintersForUI())
	})
	mux.HandleFunc("POST /api/printers", func(w http.ResponseWriter, r *http.Request) {
		var p printers.Printer
		if err := readJSON(r, &p); err != nil {
			fail(w, 400, err)
			return
		}
		// Editing without retyping the secret keeps the stored one.
		if old, ok := a.Printers.Raw(p.ID); ok && (p.APIKey == "••••" || p.APIKey == "") {
			p.APIKey = old.APIKey
		}
		saved, err := a.Printers.Put(p)
		if err != nil {
			fail(w, 400, err)
			return
		}
		a.Printers.PollOnce(r.Context())
		v, _ := a.Printers.Get(saved.ID)
		writeJSON(w, 200, PrinterView{View: v, HasEmptyBed: a.hasEmptyBed(saved.ID)})
	})
	mux.HandleFunc("DELETE /api/printers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Printers.Delete(r.PathValue("id")); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/printers/{id}/action", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Action string `json:"action"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		// Manual "clear platform": tell the printer its finished print was taken
		// off (clears the "print finished" state) without touching PrintHarbor's
		// own bed-clear review flow.
		if in.Action == "clear_platform" {
			if err := a.Printers.ClearPlatform(r.Context(), r.PathValue("id")); err != nil {
				fail(w, 400, err)
				return
			}
			a.Printers.PollOnce(r.Context())
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		if err := a.Printers.Action(r.Context(), r.PathValue("id"), in.Action); err != nil {
			fail(w, 400, err)
			return
		}
		a.Printers.PollOnce(r.Context())
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/printers/{id}/snapshot", func(w http.ResponseWriter, r *http.Request) {
		b, ct, err := a.Printers.Snapshot(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
	})
	mux.HandleFunc("POST /api/printers/{id}/bed-clear", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Clear bool `json:"clear"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		if in.Clear {
			_ = a.Printers.ClearPlatform(r.Context(), r.PathValue("id")) // FlashForge: tell the printer too
		}
		if err := a.Printers.SetBedClear(r.PathValue("id"), in.Clear); err != nil {
			fail(w, 400, err)
			return
		}
		// Marking the bed clear does NOT teach the empty-bed reference: that is only
		// the dedicated "This is my empty bed" button, so pressing clear with parts
		// still on the bed can't poison the camera's reference.
		writeJSON(w, 200, map[string]bool{"ok": true})
	})

	// --- projects ---
	mux.HandleFunc("GET /api/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, a.Projects())
	})
	mux.HandleFunc("POST /api/projects", func(w http.ResponseWriter, r *http.Request) {
		ups, err := a.readUploads(w, r)
		if err != nil {
			fail(w, 400, err)
			return
		}
		p, err := a.AddProject(ups)
		if err != nil {
			removeUploads(ups)
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, p)
	})
	mux.HandleFunc("POST /api/projects/new", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string `json:"name"`
		}
		_ = readJSON(r, &in)
		p, err := a.NewMeshProject(in.Name)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, p)
	})
	mux.HandleFunc("POST /api/projects/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		plate, _ := strconv.Atoi(r.URL.Query().Get("plate"))
		ups, err := a.readUploads(w, r)
		if err != nil {
			fail(w, 400, err)
			return
		}
		p, err := a.AddMeshFiles(r.PathValue("id"), plate, ups)
		if err != nil {
			removeUploads(ups)
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, p)
	})
	meshEdit := func(f func(r *http.Request, id string) (*Project, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			p, err := f(r, r.PathValue("id"))
			if err != nil {
				fail(w, 400, err)
				return
			}
			writeJSON(w, 200, p)
		}
	}
	mux.HandleFunc("POST /api/projects/{id}/parts/{part}/move", meshEdit(func(r *http.Request, id string) (*Project, error) {
		var in struct {
			Plate int `json:"plate"`
		}
		if err := readJSON(r, &in); err != nil {
			return nil, err
		}
		part, _ := strconv.Atoi(r.PathValue("part"))
		return a.MovePart(id, part, in.Plate)
	}))
	mux.HandleFunc("DELETE /api/projects/{id}/parts/{part}", meshEdit(func(r *http.Request, id string) (*Project, error) {
		part, _ := strconv.Atoi(r.PathValue("part"))
		return a.RemovePart(id, part)
	}))
	mux.HandleFunc("POST /api/projects/{id}/plates", meshEdit(func(r *http.Request, id string) (*Project, error) {
		return a.AddPlate(id)
	}))
	mux.HandleFunc("DELETE /api/projects/{id}/plates/{n}", meshEdit(func(r *http.Request, id string) (*Project, error) {
		n, _ := strconv.Atoi(r.PathValue("n"))
		return a.RemovePlate(id, n)
	}))
	mux.HandleFunc("POST /api/projects/{id}/rename", meshEdit(func(r *http.Request, id string) (*Project, error) {
		var in struct {
			Name string `json:"name"`
		}
		if err := readJSON(r, &in); err != nil {
			return nil, err
		}
		return a.RenameProject(id, in.Name)
	}))
	mux.HandleFunc("GET /api/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.Project(r.PathValue("id"))
		if !ok {
			fail(w, 404, fmt.Errorf("project not found"))
			return
		}
		writeJSON(w, 200, p)
	})
	mux.HandleFunc("DELETE /api/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := a.DeleteProject(r.PathValue("id")); err != nil {
			fail(w, 404, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/projects/{id}/plates/{n}/mesh", func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.Project(r.PathValue("id"))
		n, _ := strconv.Atoi(r.PathValue("n"))
		if !ok || p.Kind != "mesh" {
			http.NotFound(w, r)
			return
		}
		paths := a.meshInputs(p, n)
		if len(paths) == 0 {
			http.NotFound(w, r)
			return
		}
		b, _, err := a.meshBinary(paths)
		if err != nil {
			http.Error(w, err.Error(), 422)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(b)
	})
	mux.HandleFunc("GET /api/projects/{id}/plates/{n}/thumb", func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.Project(r.PathValue("id"))
		n, _ := strconv.Atoi(r.PathValue("n"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		if p.Slicer != nil {
			b, err := os.ReadFile(a.slicerFile(p, fmt.Sprintf("plate-%d.png", n)))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(b)
			return
		}
		if p.Kind == "mesh" {
			b, err := a.platePreview(p, n)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			writePNG(w, b)
			return
		}
		f, err := os.Open(a.projectFile(p.ID))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		fi, _ := f.Stat()
		b, err := threemf.Thumbnail(f, fi.Size(), fmt.Sprintf("Metadata/plate_%d.png", n))
		if err != nil && (p.Data == nil || len(p.Data.Plates) <= 1) {
			// 3MFs from 3D James and other apps often carry no plate picture:
			// draw the model instead.
			b, err = a.previewPNG(a.projectFile(p.ID))
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(b)
	})
	mux.HandleFunc("GET /api/projects/{id}/plates/{n}/fit", func(w http.ResponseWriter, r *http.Request) {
		p, pl, err := a.plate(r)
		if err != nil {
			fail(w, 404, err)
			return
		}
		pending := map[string]string{}
		for _, j := range a.Queue.List() {
			if j.Start && (j.State == slicer.Queued || j.State == slicer.Slicing || j.State == slicer.Sending) {
				pending[j.PrinterID] = fmt.Sprintf("About to start %s plate %d", j.ProjectName, j.Plate)
			}
		}
		writeJSON(w, 200, a.fitPlate(p, pl, a.Printers.List(), fit.Options{Pending: pending, Locks: a.locksFor(p)}))
	})
	mux.HandleFunc("GET /api/projects/{id}/suggest", func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.Project(r.PathValue("id"))
		if !ok {
			fail(w, 404, fmt.Errorf("project not found"))
			return
		}
		s, err := a.Suggest(p, r.URL.Query().Get("printer"))
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, s)
	})

	// --- jobs ---
	mux.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, a.jobViews(time.Now()))
	})
	mux.HandleFunc("POST /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		var in slicer.Job
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		p, ok := a.Project(in.ProjectID)
		if !ok {
			fail(w, 400, fmt.Errorf("project not found"))
			return
		}
		pr, ok := a.Printers.Get(in.PrinterID)
		if !ok {
			fail(w, 400, fmt.Errorf("printer not found"))
			return
		}
		if in.Machine == "" {
			in.Machine = pr.MachinePreset
		}
		in.ProjectName, in.PrinterName = p.Name, pr.Name
		if err := a.applySaved(p, &in); err != nil {
			fail(w, 400, err)
			return
		}
		j, err := a.Queue.Add(in)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, j)
	})
	mux.HandleFunc("POST /api/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Queue.Cancel(r.PathValue("id")); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// Send a job that was only sliced (you looked at its preview first) to its
	// printer, as it is: no slicing again.
	mux.HandleFunc("POST /api/jobs/{id}/send", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Start bool `json:"start"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		j, err := a.Queue.SendNow(r.PathValue("id"), in.Start)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, j)
	})
	mux.HandleFunc("POST /api/jobs/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		j, err := a.Queue.Retry(r.PathValue("id"))
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, j)
	})
	mux.HandleFunc("DELETE /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := a.Queue.Remove(id); err != nil {
			fail(w, 400, err)
			return
		}
		if reJobDir.MatchString(id) {
			_ = os.RemoveAll(filepath.Join(a.Store.Dir(), "jobs", id))
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/jobs/{id}/download", func(w http.ResponseWriter, r *http.Request) {
		j, ok := a.Queue.Get(r.PathValue("id"))
		if !ok || (j.GCode == "" && j.Sliced3MF == "") {
			http.NotFound(w, r)
			return
		}
		path, name := j.GCode, fmt.Sprintf("%s_plate%d.gcode", j.ProjectName, j.Plate)
		if r.URL.Query().Get("format") == "3mf" || path == "" {
			path, name = j.Sliced3MF, fmt.Sprintf("%s_plate%d.gcode.3mf", j.ProjectName, j.Plate)
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", unsafeName.ReplaceAllString(name, "_")))
		http.ServeFile(w, r, path)
	})

	a.libraryRoutes(mux)
	a.sitesRoutes(mux)
	a.captureRoutes(mux)
	a.jamesRoutes(mux)
	a.printableRoutes(mux)
	a.slicerUIRoutes(mux)
	a.slicerProjectRoutes(mux)
	a.captureRoutesProto(mux)
	a.pluginRoutes(mux)
	mux.HandleFunc("POST /api/shortcut", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Engine string `json:"engine"`
		}
		_ = readJSON(r, &in)
		msg, err := a.makeShortcut(in.Engine)
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "message": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
	})
	mux.HandleFunc("GET /api/icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(tray.IconPNG(128))
	})
	a.linkRoutes(mux)
	a.integrationRoutes(mux)
	a.cameraRoutes(mux)
	a.reachRoutes(mux)
	a.workerRoutes(mux)
	a.controlRoutes(mux)
	a.streamRoutes(mux)
	a.aiRoutes(mux)
	a.assistantRoutes(mux)

	// The web UI itself (see static.go: fingerprinted, compressed, re-checked on
	// every load so an updated PrintHarbor never runs with yesterday's JavaScript).
	mux.HandleFunc("GET /3djames.html", a.serve3DJames(web))
	mux.Handle("/", staticHandler(web))
	return a.guard(mux)
}

// readUploads saves every "file" part of a multipart request to temp files.
func (a *App) readUploads(w http.ResponseWriter, r *http.Request) ([]Upload, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, err
	}
	var ups []Upload
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			removeUploads(ups)
			return nil, err
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		tmp, err := os.CreateTemp(a.Store.Dir(), "upload-*")
		if err != nil {
			removeUploads(ups)
			return nil, err
		}
		ups = append(ups, Upload{Name: part.FileName(), Tmp: tmp.Name()})
		_, err = io.Copy(tmp, part)
		tmp.Close()
		if err != nil {
			removeUploads(ups)
			return nil, err
		}
	}
	if len(ups) == 0 {
		return nil, fmt.Errorf("no file uploaded")
	}
	return ups, nil
}

func removeUploads(ups []Upload) {
	for _, u := range ups {
		os.Remove(u.Tmp)
	}
}

func (a *App) plate(r *http.Request) (*Project, threemf.Plate, error) {
	p, ok := a.Project(r.PathValue("id"))
	if !ok {
		return nil, threemf.Plate{}, fmt.Errorf("project not found")
	}
	n, _ := strconv.Atoi(r.PathValue("n"))
	for _, pl := range p.Data.Plates {
		if pl.Index == n {
			return p, pl, nil
		}
	}
	return nil, threemf.Plate{}, fmt.Errorf("plate %d not found", n)
}
