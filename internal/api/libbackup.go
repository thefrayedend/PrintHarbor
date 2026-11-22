package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/library"
)

// The model library backup: ONE zip, printharbor-library.zip, holding every
// model and everything known about it (see library.Export). Making a new one
// replaces the old one, so there is always exactly one, ready to copy to
// another computer. Another PrintHarbor restores it from Settings.
//
// It is made when you press the button. With library_backup_hours set it is
// also made again that often, but only when the library has changed.

const libBackupName = "printharbor-library.zip"

type libBackupState struct {
	Path    string    `json:"path"`
	Made    time.Time `json:"made"`
	Models  int       `json:"models"`
	Files   int       `json:"files"`
	Bytes   int64     `json:"bytes"`
	Took    float64   `json:"took_sec"`
	Sig     string    `json:"sig"`
	Error   string    `json:"error,omitempty"`
	Running bool      `json:"running"`
}

var libBackupMu sync.Mutex
var libBackupRunning struct {
	sync.Mutex
	on bool
}

func (a *App) libBackupDir() string {
	if d := strings.TrimSpace(a.cfg().LibraryBackupDir); d != "" {
		return d
	}
	return a.backupRoot()
}

func (a *App) libBackupPath() string { return filepath.Join(a.libBackupDir(), libBackupName) }

func (a *App) libBackupStateFile() string { return filepath.Join(a.Store.Dir(), "library-backup.json") }

func (a *App) libBackupState() libBackupState {
	var st libBackupState
	if b, err := os.ReadFile(a.libBackupStateFile()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	libBackupRunning.Lock()
	st.Running = libBackupRunning.on
	libBackupRunning.Unlock()
	return st
}

func (a *App) saveLibBackupState(st libBackupState) {
	if b, err := json.MarshalIndent(st, "", " "); err == nil {
		_ = os.WriteFile(a.libBackupStateFile(), b, 0o644)
	}
}

// libRoots are the library folders, with the names they get inside the zip:
// "Models" for the imported-models folder, the folder's own name for others.
func (a *App) libRoots() []library.ArchiveRoot {
	md := a.cfg().ModelsDir()
	out := []library.ArchiveRoot{}
	used := map[string]bool{}
	for _, d := range a.cfg().ScanFolders() {
		d = filepath.Clean(d)
		name := filepath.Base(d)
		if strings.EqualFold(d, md) {
			name = "Models"
		}
		if name == "" || name == "." || name == string(filepath.Separator) || strings.ContainsAny(name, `:\/`) {
			name = "Folder"
		}
		base := name
		for i := 2; used[strings.ToLower(name)] || strings.EqualFold(name, "PrintHarbor uploads") || strings.EqualFold(name, "Added photos") || strings.EqualFold(name, library.ArchiveManifestName); i++ {
			name = fmt.Sprintf("%s %d", base, i)
		}
		used[strings.ToLower(name)] = true
		out = append(out, library.ArchiveRoot{Name: name, Dir: d})
	}
	return out
}

// libSignature fingerprints the library (file names, sizes and times, and
// PrintHarbor's own record of it) without reading any file, so a timed backup
// is only made when something changed.
func (a *App) libSignature() string {
	h := sha256.New()
	walk := func(dir string) {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if fi, err := d.Info(); err == nil {
				fmt.Fprintf(h, "%s\x00%d\x00%d\n", p, fi.Size(), fi.ModTime().UnixNano())
			}
			return nil
		})
	}
	for _, r := range a.libRoots() {
		walk(r.Dir)
	}
	walk(filepath.Join(a.Store.Dir(), "library", "items"))
	if fi, err := os.Stat(filepath.Join(a.Store.Dir(), "library", "index.json")); err == nil {
		fmt.Fprintf(h, "index\x00%d\x00%d\n", fi.Size(), fi.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

// LibraryBackup makes printharbor-library.zip, replacing the one before only
// once the new one is complete.
func (a *App) LibraryBackup() (libBackupState, error) {
	libBackupMu.Lock()
	defer libBackupMu.Unlock()
	libBackupRunning.Lock()
	libBackupRunning.on = true
	libBackupRunning.Unlock()
	defer func() { libBackupRunning.Lock(); libBackupRunning.on = false; libBackupRunning.Unlock() }()

	t0 := time.Now()
	st := a.libBackupState()
	sig := a.libSignature()
	dst := a.libBackupPath()
	fail := func(err error) (libBackupState, error) {
		st.Error = err.Error()
		a.saveLibBackupState(st)
		return st, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fail(fmt.Errorf("can't use the folder %s: %w", filepath.Dir(dst), err))
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return fail(fmt.Errorf("can't write in %s: %w", filepath.Dir(dst), err))
	}
	stats, err := a.Library.Export(f, a.libRoots(), fmt.Sprintf("PrintHarbor %s build %d", Version, Build), dst)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return fail(fmt.Errorf("the library backup failed: %w", err))
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fail(fmt.Errorf("the new backup is made but couldn't replace %s (is it open somewhere?): %w", dst, err))
	}
	fi, _ := os.Stat(dst)
	st = libBackupState{Path: dst, Made: time.Now(), Models: stats.Models, Files: stats.Files, Took: time.Since(t0).Seconds(), Sig: sig}
	if fi != nil {
		st.Bytes = fi.Size()
	}
	a.saveLibBackupState(st)
	log.Printf("Library backup: %d models, %d files, %.0f MB in %s (%.0fs)", st.Models, st.Files, mb(st.Bytes), dst, st.Took)
	return st, nil
}

// libBackupIfDue is the timer: when library_backup_hours is set, the backup
// is made again that often, if the library changed since the last one.
func (a *App) libBackupIfDue(now time.Time) {
	hours := a.cfg().LibraryBackupHours
	if hours <= 0 {
		return
	}
	st := a.libBackupState()
	if now.Sub(st.Made) < time.Duration(hours)*time.Hour-5*time.Minute {
		return
	}
	if _, err := os.Stat(a.libBackupPath()); err == nil && st.Path == a.libBackupPath() && st.Sig == a.libSignature() {
		return // nothing changed
	}
	if _, err := a.LibraryBackup(); err != nil {
		log.Printf("Library backup: %v", err)
		a.Alert("", "error", "The model library backup failed: "+err.Error(), nil)
	}
}

func (a *App) libBackupLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.libBackupIfDue(time.Now())
		}
	}
}

// RestoreLibrary puts a library backup into this PrintHarbor: the models go
// into its models folder (other library folders from the backup into folders
// beside it, which are added to the library), then everything changed in
// PrintHarbor is put back on them.
func (a *App) RestoreLibrary(zipPath string) (library.ImportResult, error) {
	libBackupMu.Lock() // not while a backup is being made, and one restore at a time
	defer libBackupMu.Unlock()
	zr, m, err := library.ReadArchive(zipPath)
	if err != nil {
		return library.ImportResult{}, err
	}
	defer zr.Close()
	md := a.cfg().ModelsDir()
	dest := map[string]string{}
	var extra []string
	for _, r := range m.Roots {
		if r.Name == "Models" {
			dest[r.Name] = md
			continue
		}
		// Another library folder: into a folder of the same name beside the
		// models folder (a library folder already, or a new one).
		d := filepath.Join(filepath.Dir(md), r.Name)
		dest[r.Name] = d
		extra = append(extra, d)
	}
	if err := os.MkdirAll(md, 0o755); err != nil {
		return library.ImportResult{}, err
	}
	res := library.ImportArchive(zr, dest)
	if len(extra) > 0 {
		a.cfgEdit.Lock()
		c := a.cfg()
		have := map[string]bool{}
		for _, f := range c.ScanFolders() {
			have[strings.ToLower(filepath.Clean(f))] = true
		}
		for _, d := range extra {
			if !have[strings.ToLower(filepath.Clean(d))] {
				if _, err := os.Stat(d); err == nil {
					c.LibraryFolders = append(append([]string{}, c.LibraryFolders...), d)
				}
			}
		}
		a.setCfg(c)
		if a.CfgPath != "" {
			_ = config.Save(a.CfgPath, c)
		}
		a.cfgEdit.Unlock()
	}
	a.Library.SetFolders(a.cfg().ScanFolders())
	a.Library.Rescan()
	a.Library.RestoreArchive(zr, m, dest, &res)
	log.Printf("Library restore from %s: %d models, %d files written, %d already there, %d errors", zipPath, res.Models+res.Uploaded, res.Written, res.Same, len(res.Errors))
	return res, nil
}

func (a *App) libBackupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/library/backup", func(w http.ResponseWriter, r *http.Request) {
		st := a.libBackupState()
		_, err := os.Stat(a.libBackupPath())
		writeJSON(w, 200, map[string]any{"state": st, "path": a.libBackupPath(), "exists": err == nil,
			"dir": a.cfg().LibraryBackupDir, "default_dir": a.backupRoot(), "hours": a.cfg().LibraryBackupHours})
	})
	mux.HandleFunc("POST /api/library/backup", func(w http.ResponseWriter, r *http.Request) {
		st, err := a.LibraryBackup()
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, st)
	})
	mux.HandleFunc("POST /api/library/backup/settings", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Dir   string `json:"dir"`
			Hours int    `json:"hours"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		dir := strings.TrimSpace(strings.Trim(strings.TrimSpace(in.Dir), `"`))
		if dir != "" {
			if !filepath.IsAbs(dir) {
				fail(w, 400, fmt.Errorf(`the folder must be a full path, like D:\Backups or \\nas\backups`))
				return
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				fail(w, 400, fmt.Errorf("can't use %s: %v", dir, err))
				return
			}
		}
		if in.Hours < 0 || in.Hours > 24*90 {
			fail(w, 400, fmt.Errorf("hours must be 0 (only when you press the button) up to 2160"))
			return
		}
		a.cfgEdit.Lock()
		c := a.cfg()
		c.LibraryBackupDir, c.LibraryBackupHours = dir, in.Hours
		a.setCfg(c)
		var err error
		if a.CfgPath != "" {
			err = config.Save(a.CfgPath, c)
		}
		a.cfgEdit.Unlock()
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "path": a.libBackupPath()})
	})
	mux.HandleFunc("GET /api/library/backup/download", func(w http.ResponseWriter, r *http.Request) {
		p := a.libBackupPath()
		if _, err := os.Stat(p); err != nil {
			fail(w, 404, fmt.Errorf("there is no library backup yet; make one first"))
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+libBackupName+`"`)
		w.Header().Set("Content-Type", "application/zip")
		http.ServeFile(w, r, p)
	})
	// Restore: the zip is sent as the request body (any size; it streams to disk).
	mux.HandleFunc("POST /api/library/restore", func(w http.ResponseWriter, r *http.Request) {
		tmp := filepath.Join(a.Store.Dir(), "library-restore.zip")
		f, err := os.Create(tmp)
		if err != nil {
			fail(w, 500, err)
			return
		}
		_, err = io.Copy(f, r.Body)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		defer os.Remove(tmp)
		if err != nil {
			fail(w, 400, fmt.Errorf("the upload stopped: %w", err))
			return
		}
		res, err := a.RestoreLibrary(tmp)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, res)
	})
}
