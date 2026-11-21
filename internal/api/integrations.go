package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/fit"
	"github.com/thefrayedend/printharbor/internal/slicer"
)

// Spool is a Spoolman spool, flattened for the UI.
type Spool struct {
	ID         int     `json:"id"`
	Name       string  `json:"name"`
	Vendor     string  `json:"vendor"`
	Material   string  `json:"material"`
	Color      string  `json:"color"`
	RemainingG float64 `json:"remaining_g"`
	CapacityG  float64 `json:"capacity_g"` // weight when full/registered, for the fill bar
	Location   string  `json:"location"`
}

func (s Spool) Label() string {
	parts := []string{}
	for _, p := range []string{s.Vendor, s.Name} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		parts = append(parts, s.Material)
	}
	return fmt.Sprintf("#%d %s", s.ID, strings.Join(parts, " "))
}

// Spools fetches active spools from Spoolman (cached for 30 s).
func (a *App) Spools(ctx context.Context) ([]Spool, error) {
	url := strings.TrimRight(a.cfg().SpoolmanURL, "/")
	if url == "" {
		return nil, fmt.Errorf("Spoolman isn't set up (Settings → Spoolman address)")
	}
	a.spoolMu.Lock()
	defer a.spoolMu.Unlock()
	if time.Since(a.spoolAt) < 30*time.Second && a.spoolCache != nil {
		return a.spoolCache, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, "GET", url+"/api/v1/spool", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("can't reach Spoolman: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Spoolman: %s", resp.Status)
	}
	var raw []struct {
		ID              int     `json:"id"`
		RemainingWeight float64 `json:"remaining_weight"`
		InitialWeight   float64 `json:"initial_weight"`
		Location        string  `json:"location"`
		Archived        bool    `json:"archived"`
		Filament        struct {
			Name     string  `json:"name"`
			Material string  `json:"material"`
			ColorHex string  `json:"color_hex"`
			Weight   float64 `json:"weight"`
			Vendor   struct {
				Name string `json:"name"`
			} `json:"vendor"`
		} `json:"filament"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("Spoolman: %w", err)
	}
	out := []Spool{}
	for _, s := range raw {
		if s.Archived {
			continue
		}
		c := s.Filament.ColorHex
		if c != "" && !strings.HasPrefix(c, "#") {
			c = "#" + c
		}
		cap := s.InitialWeight
		if cap <= 0 {
			cap = s.Filament.Weight
		}
		out = append(out, Spool{ID: s.ID, Name: s.Filament.Name, Vendor: s.Filament.Vendor.Name, Material: s.Filament.Material,
			Color: c, RemainingG: s.RemainingWeight, CapacityG: cap, Location: s.Location})
	}
	a.spoolCache, a.spoolAt = out, time.Now()
	return out, nil
}

// spoolDebits works out what a completed print used of each pinned Spoolman
// spool. It never guesses:
//
//   - When the G-code says how much of EACH filament was used, every filament
//     that is pinned to a spool is charged exactly its own grams. Filaments that
//     aren't pinned are left alone.
//   - When only a total is known, it is charged only if the project has a single
//     filament and that one is pinned. A total can't be split between colours.
//
// locks maps a project filament's number to its spool; numbers are the
// project's filament numbers in order (filamentNos), else 1, 2, 3…
func spoolDebits(job slicer.Job, locks map[int]int, filamentNos []int) map[int]float64 {
	out := map[int]float64{}
	if len(job.FilamentGs) > 0 {
		for i, g := range job.FilamentGs {
			no := i + 1
			if i < len(filamentNos) {
				no = filamentNos[i]
			}
			if spool := locks[no]; spool > 0 && g > 0 {
				out[spool] += g
			}
		}
		return out
	}
	if job.FilamentG > 0 && len(locks) == 1 && len(filamentNos) <= 1 && len(job.Filaments) <= 1 {
		for _, spool := range locks {
			if spool > 0 {
				out[spool] = job.FilamentG
			}
		}
	}
	return out
}

// spoolmanUse records the filament a COMPLETED print used against its Spoolman
// spools, when the user has turned write-back on. Only called for prints known
// to have run to the end (see onPrintEnd): a cancelled print is never charged.
// Best-effort and in the background: a Spoolman hiccup never affects a print.
func (a *App) spoolmanUse(job slicer.Job) {
	cfg := a.cfg()
	if !cfg.SpoolmanWriteback || strings.TrimSpace(cfg.SpoolmanURL) == "" {
		return
	}
	proj, ok := a.Project(job.ProjectID)
	if !ok {
		return
	}
	var nos []int
	if proj.Data != nil {
		for _, f := range proj.Data.Filaments {
			nos = append(nos, f.Index)
		}
	}
	debits := spoolDebits(job, proj.Locks, nos)
	if len(debits) == 0 {
		return
	}
	ok = false
	for spoolID, grams := range debits {
		url := strings.TrimRight(cfg.SpoolmanURL, "/") + fmt.Sprintf("/api/v1/spool/%d/use", spoolID)
		body, _ := json.Marshal(map[string]any{"use_weight": grams})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err != nil {
			log.Printf("Spoolman write-back: %v", err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			log.Printf("Spoolman write-back: spool #%d returned %s", spoolID, resp.Status)
			continue
		}
		ok = true
		log.Printf("Spoolman: deducted %.1f g from spool #%d (%s, plate %d)", grams, spoolID, job.ProjectName, job.Plate)
	}
	if ok {
		a.spoolMu.Lock()
		a.spoolCache = nil // refresh so the inventory shows the new remaining weight
		a.spoolMu.Unlock()
	}
}

func (a *App) locksFor(p *Project) map[int]fit.Lock {
	if len(p.Locks) == 0 {
		return nil
	}
	labels := map[int]string{}
	if spools, err := a.Spools(context.Background()); err == nil {
		for _, s := range spools {
			labels[s.ID] = s.Label()
		}
	}
	out := map[int]fit.Lock{}
	for f, id := range p.Locks {
		l := labels[id]
		if l == "" {
			l = fmt.Sprintf("spool #%d", id)
		}
		out[f] = fit.Lock{SpoolID: id, Label: l}
	}
	return out
}

// SetLock pins (spool > 0) or frees (0) a project filament.
func (a *App) SetLock(id string, filament, spool int) (*Project, error) {
	a.projMu.Lock()
	defer a.projMu.Unlock()
	p, ok := a.projects[id]
	if !ok {
		return nil, fmt.Errorf("project not found")
	}
	if p.Locks == nil {
		p.Locks = map[int]int{}
	}
	if spool > 0 {
		p.Locks[filament] = spool
	} else {
		delete(p.Locks, filament)
	}
	return p.clone(), a.saveProjectsLocked()
}

// Settings is what the Settings page can change.
type Settings struct {
	MaxConcurrentSlices int      `json:"max_concurrent_slices"`
	PollSeconds         int      `json:"poll_seconds"`
	JobKeepDays         int      `json:"job_keep_days"`
	BackupDir           string   `json:"backup_dir"`
	BackupKeepDays      int      `json:"backup_keep_days"`
	ShowPrinterLight    bool     `json:"show_printer_light"`
	SpoolmanURL         string   `json:"spoolman_url"`
	SpoolmanWriteback   bool     `json:"spoolman_writeback"`
	ObicoMLURL          string   `json:"obico_ml_url"`
	OllamaURL           string   `json:"ollama_url"`
	OllamaModel         string   `json:"ollama_model"`
	Workers             []string `json:"workers"`
	WorkerToken         string   `json:"worker_token"`
	MDNSName            string   `json:"mdns_name"`
	OrcaPath            string   `json:"orca_path"`
	OrcaProfilesDir     string   `json:"orca_profiles_dir"`
	DiscordWebhook      string   `json:"discord_webhook"`
	PublicURL           string   `json:"public_url"`
	ModelDir            string   `json:"model_dir"`
	ThingiverseToken    string   `json:"thingiverse_token"`
	MakerWorldToken     string   `json:"makerworld_token"`
	CultsUser           string   `json:"cults_user"`
	CultsKey            string   `json:"cults_key"`
}

func (a *App) settings() Settings {
	c := a.cfg()
	w := c.Workers
	if w == nil {
		w = []string{}
	}
	// By name, never by position: a field added to Settings in the wrong place
	// would otherwise shift every value after it without an error.
	return Settings{
		MaxConcurrentSlices: c.MaxConcurrentSlices, PollSeconds: c.PollSeconds, JobKeepDays: c.JobKeepDays,
		BackupDir: c.BackupDir, BackupKeepDays: c.BackupKeepDays,
		ShowPrinterLight: c.ShowPrinterLight, SpoolmanURL: c.SpoolmanURL, SpoolmanWriteback: c.SpoolmanWriteback,
		ObicoMLURL: c.ObicoMLURL, OllamaURL: c.OllamaURL, OllamaModel: c.OllamaModel,
		Workers: w, WorkerToken: c.WorkerToken, MDNSName: c.MDNSName, OrcaPath: c.OrcaPath, OrcaProfilesDir: c.OrcaProfilesDir,
		DiscordWebhook: c.DiscordWebhook, PublicURL: c.PublicURL, ModelDir: c.ModelsDir(),
		ThingiverseToken: c.ThingiverseToken, MakerWorldToken: c.MakerWorldToken, CultsUser: c.CultsUser, CultsKey: c.CultsKey,
	}
}

func (a *App) integrationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, a.settings())
	})
	mux.HandleFunc("POST /api/settings", func(w http.ResponseWriter, r *http.Request) {
		a.cfgEdit.Lock() // one settings change at a time, from read to save
		defer a.cfgEdit.Unlock()
		var s Settings
		if err := readJSON(r, &s); err != nil {
			fail(w, 400, err)
			return
		}
		if s.MaxConcurrentSlices < 1 || s.MaxConcurrentSlices > 16 {
			fail(w, 400, fmt.Errorf("slices at a time must be 1–16"))
			return
		}
		if s.PollSeconds < 1 || s.PollSeconds > 3600 {
			fail(w, 400, fmt.Errorf("the printer check interval must be 1–3600 seconds"))
			return
		}
		if s.JobKeepDays < 0 || s.JobKeepDays > 3650 {
			fail(w, 400, fmt.Errorf("days to keep finished jobs must be 0 (keep them) to 3650"))
			return
		}
		restart := s.MaxConcurrentSlices != a.cfg().MaxConcurrentSlices || s.MDNSName != a.cfg().MDNSName ||
			s.OrcaPath != a.cfg().OrcaPath || s.PollSeconds != a.cfg().PollSeconds
		profilesChanged := s.OrcaProfilesDir != a.cfg().OrcaProfilesDir
		c := a.cfg()
		c.MaxConcurrentSlices, c.PollSeconds = s.MaxConcurrentSlices, s.PollSeconds
		c.JobKeepDays = s.JobKeepDays
		if s.BackupKeepDays < 0 || s.BackupKeepDays > 3650 {
			fail(w, 400, fmt.Errorf("days to keep backups must be 0 (no backups) to 3650"))
			return
		}
		if bd := strings.TrimSpace(strings.Trim(strings.TrimSpace(s.BackupDir), `"`)); bd != "" {
			if !filepath.IsAbs(bd) {
				fail(w, 400, fmt.Errorf("the backup folder must be a full path, like D:\\PrintHarbor backups"))
				return
			}
			if err := os.MkdirAll(bd, 0o755); err != nil {
				fail(w, 400, fmt.Errorf("can't use %s for backups: %v", bd, err))
				return
			}
			c.BackupDir = bd
		} else {
			c.BackupDir = ""
		}
		c.BackupKeepDays = s.BackupKeepDays
		c.ShowPrinterLight = s.ShowPrinterLight
		c.SpoolmanURL, c.ObicoMLURL = strings.TrimSpace(s.SpoolmanURL), strings.TrimSpace(s.ObicoMLURL)
		c.SpoolmanWriteback = s.SpoolmanWriteback
		c.OllamaURL, c.OllamaModel = strings.TrimSpace(s.OllamaURL), strings.TrimSpace(s.OllamaModel)
		c.Workers, c.WorkerToken, c.MDNSName = cleanList(s.Workers), strings.TrimSpace(s.WorkerToken), strings.TrimSpace(s.MDNSName)
		c.OrcaPath, c.OrcaProfilesDir = strings.TrimSpace(s.OrcaPath), strings.TrimSpace(s.OrcaProfilesDir)
		c.DiscordWebhook, c.PublicURL = strings.TrimSpace(s.DiscordWebhook), strings.TrimSpace(s.PublicURL)
		c.ThingiverseToken, c.MakerWorldToken = strings.TrimSpace(s.ThingiverseToken), strings.TrimSpace(s.MakerWorldToken)
		c.CultsUser, c.CultsKey = strings.TrimSpace(s.CultsUser), strings.TrimSpace(s.CultsKey)
		if md := strings.TrimSpace(strings.Trim(strings.TrimSpace(s.ModelDir), `"`)); md != "" && md != a.cfg().ModelsDir() {
			if !filepath.IsAbs(md) {
				fail(w, 400, fmt.Errorf("the models folder must be a full path, like D:\\3D Models"))
				return
			}
			if err := os.MkdirAll(md, 0o755); err != nil {
				fail(w, 400, fmt.Errorf("can't use %s as the models folder: %v", md, err))
				return
			}
			// Models already imported stay where they are and stay in the library.
			old := a.cfg().ModelsDir()
			if ents, _ := os.ReadDir(old); len(ents) > 0 {
				c.LibraryFolders = append(append([]string{}, c.LibraryFolders...), old)
			}
			c.ModelDir = md
		}
		modelDirChanged := c.ModelsDir() != a.cfg().ModelsDir()
		a.setCfg(c)
		if a.Plugins != nil {
			a.Plugins.SetKeys(a.pluginKeys())
		}
		if modelDirChanged {
			a.Library.SetFolders(a.cfg().ScanFolders())
			go a.Library.Rescan()
		}
		a.spoolMu.Lock()
		a.spoolCache = nil
		a.spoolMu.Unlock()
		if profilesChanged {
			a.ReloadProfiles()
		}
		if a.CfgPath != "" {
			if err := config.Save(a.CfgPath, a.cfg()); err != nil {
				fail(w, 500, err)
				return
			}
		}
		writeJSON(w, 200, map[string]any{"settings": a.settings(), "restart_needed": restart})
	})
	mux.HandleFunc("GET /api/spoolman/spools", func(w http.ResponseWriter, r *http.Request) {
		s, err := a.Spools(r.Context())
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, s)
	})
	// A friendly connection check for Settings: is Spoolman set up and reachable,
	// and how many spools does it have?
	mux.HandleFunc("GET /api/spoolman/status", func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(a.cfg().SpoolmanURL) == "" {
			writeJSON(w, 200, map[string]any{"configured": false})
			return
		}
		s, err := a.Spools(r.Context())
		if err != nil {
			writeJSON(w, 200, map[string]any{"configured": true, "ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"configured": true, "ok": true, "count": len(s), "url": a.cfg().SpoolmanURL})
	})
	mux.HandleFunc("POST /api/projects/{id}/locks", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Filament int `json:"filament"`
			SpoolID  int `json:"spool_id"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		p, err := a.SetLock(r.PathValue("id"), in.Filament, in.SpoolID)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, p)
	})

	// --- tracking & dispatch ---
	mux.HandleFunc("GET /api/projects/{id}/progress", func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.Project(r.PathValue("id"))
		if !ok {
			fail(w, 404, fmt.Errorf("project not found"))
			return
		}
		writeJSON(w, 200, a.Progress(p))
	})
	mux.HandleFunc("POST /api/projects/{id}/copies", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Plate, Copies int }
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		p, err := a.SetCopies(r.PathValue("id"), in.Plate, in.Copies)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, p)
	})
	mux.HandleFunc("POST /api/projects/{id}/printed", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Plate int
			Done  bool
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		if err := a.MarkPrinted(r.PathValue("id"), in.Plate, in.Done); err != nil {
			fail(w, 400, err)
			return
		}
		p, ok := a.Project(r.PathValue("id"))
		if !ok {
			fail(w, 404, fmt.Errorf("project not found"))
			return
		}
		writeJSON(w, 200, a.Progress(p))
	})
	mux.HandleFunc("POST /api/projects/{id}/dispatch", func(w http.ResponseWriter, r *http.Request) {
		as, err := a.Dispatch(r.PathValue("id"))
		if err != nil {
			fail(w, 400, err)
			return
		}
		if as == nil {
			as = []Assignment{}
		}
		writeJSON(w, 200, as)
	})
	mux.HandleFunc("POST /api/projects/{id}/auto", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			On   bool   `json:"on"`
			Goal string `json:"goal"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		p, err := a.SetAuto(r.PathValue("id"), in.On, in.Goal)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, p)
	})
	mux.HandleFunc("POST /api/projects/{id}/repack", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			PrinterID string `json:"printer_id"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		p, err := a.Repack(r.PathValue("id"), in.PrinterID)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, p)
	})
	// Backups (Settings → General): what there is, and "Back up now".
	mux.HandleFunc("GET /api/backups", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"folder": a.backupRoot(), "keep_days": a.cfg().BackupKeepDays, "backups": a.backups()})
	})
	mux.HandleFunc("POST /api/backups", func(w http.ResponseWriter, r *http.Request) {
		b, err := a.backupNow(time.Now())
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, b)
	})
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		runs := a.Runs(q.Get("printer"), q.Get("project"))
		if n, _ := strconv.Atoi(q.Get("limit")); n > 0 && len(runs) > n {
			runs = runs[:n]
		}
		writeJSON(w, 200, runs)
	})
	mux.HandleFunc("POST /api/runs/{id}/review", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Good bool `json:"good"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		run, err := a.ReviewRun(r.PathValue("id"), in.Good)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, run)
	})
}

func cleanList(in []string) []string {
	out := []string{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
