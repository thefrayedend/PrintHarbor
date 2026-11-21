package api

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Housekeeping: things PrintHarbor wrote for its own use and no longer needs.
// Nothing here touches your models, your library, your projects or the print
// history; only working files that would otherwise pile up for ever.
//
//   - Finished jobs in the Queue, with the G-code sliced for them, once they
//     are older than "job_keep_days" (30 by default; 0 keeps them for ever).
//     A project can always be sliced again.
//   - Files handed to the slicer page (data\slicer\inbox). They are only
//     tracked while PrintHarbor runs, so after a restart nothing can open them.
//   - Half-finished uploads and copies (upload-*, lib-*, copy-*, worker-*)
//     that a crash or power cut left in the data folder.

var (
	reJobDir   = regexp.MustCompile(`^[0-9a-f]{12}$`)                 // slicer.Queue job IDs
	reTempName = regexp.MustCompile(`^(upload|lib|copy|worker)-\d+$`) // os.CreateTemp / MkdirTemp names
)

// dirSize adds up the files under a folder.
func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// newestFile is when anything under a folder was last written (the folder's
// own date when it holds no files).
func newestFile(dir string) time.Time {
	var newest time.Time
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil && fi.ModTime().After(newest) {
				newest = fi.ModTime()
			}
		}
		return nil
	})
	if newest.IsZero() {
		if fi, err := os.Stat(dir); err == nil {
			newest = fi.ModTime()
		}
	}
	return newest
}

// cleanupJobs removes finished jobs older than the configured number of days,
// and job folders the queue no longer knows about.
func (a *App) cleanupJobs(now time.Time) (jobs int, freed int64) {
	days := a.cfg().JobKeepDays
	if days <= 0 || a.Queue == nil {
		return 0, 0
	}
	before := now.Add(-time.Duration(days) * 24 * time.Hour)
	root := filepath.Join(a.Store.Dir(), "jobs")
	remove := func(id string) {
		dir := filepath.Join(root, id)
		if !reJobDir.MatchString(id) {
			return // never anything but a job folder
		}
		if _, err := os.Stat(dir); err != nil {
			return
		}
		n := dirSize(dir)
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("Cleanup: couldn't remove %s: %v", dir, err)
			return
		}
		freed += n
	}
	for _, id := range a.Queue.Prune(before) {
		jobs++
		remove(id)
	}
	// Folders left behind by jobs that are no longer in the list.
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if !e.IsDir() || !reJobDir.MatchString(e.Name()) || a.Queue.Has(e.Name()) {
			continue
		}
		if newestFile(filepath.Join(root, e.Name())).Before(before) {
			remove(e.Name())
		}
	}
	return jobs, freed
}

// cleanupLeftovers runs once at startup, before anything is being uploaded:
// whatever temp files are there now were abandoned by an earlier run.
func (a *App) cleanupLeftovers(now time.Time) (files int, freed int64) {
	old := now.Add(-time.Hour)
	// Slicer inbox: only known to the run that created the files.
	inbox := filepath.Join(a.slicerDir(), "inbox")
	if ents, err := os.ReadDir(inbox); err == nil {
		for _, e := range ents {
			fi, err := e.Info()
			if err != nil || e.IsDir() || fi.ModTime().After(old) {
				continue
			}
			if os.Remove(filepath.Join(inbox, e.Name())) == nil {
				files++
				freed += fi.Size()
			}
		}
	}
	// Abandoned temp files and folders in the data folder itself.
	if ents, err := os.ReadDir(a.Store.Dir()); err == nil {
		for _, e := range ents {
			if !reTempName.MatchString(e.Name()) {
				continue
			}
			fi, err := e.Info()
			if err != nil || fi.ModTime().After(old) {
				continue
			}
			p := filepath.Join(a.Store.Dir(), e.Name())
			n := fi.Size()
			if e.IsDir() {
				n = dirSize(p)
			}
			if os.RemoveAll(p) == nil {
				files++
				freed += n
			}
		}
	}
	return files, freed
}

func mb(n int64) float64 { return float64(n) / (1 << 20) }

// housekeeping tidies up at startup and then once a day.
func (a *App) housekeeping(ctx context.Context) {
	if n, freed := a.cleanupLeftovers(time.Now()); n > 0 {
		log.Printf("Cleanup: removed %d leftover working file(s), %.0f MB", n, mb(freed))
	}
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	go func() { // the daily backup: looked at every hour, so a PC that sleeps or restarts still gets one
		h := time.NewTicker(time.Hour)
		defer h.Stop()
		for {
			a.backupIfDue(time.Now())
			select {
			case <-ctx.Done():
				return
			case <-h.C:
			}
		}
	}()
	for {
		if n, freed := a.cleanupJobs(time.Now()); n > 0 || freed > 0 {
			log.Printf("Cleanup: removed %d finished job(s) older than %d days, %.0f MB", n, a.cfg().JobKeepDays, mb(freed))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
