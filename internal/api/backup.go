package api

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Backups of what can't be made again: your projects (with their painting and
// settings), printers, print history, your own profiles, the library's list,
// the camera's learned beds, plugin settings and config.json.
//
// Each backup is a dated folder that holds everything, so going back is
// copying one folder. A file that hasn't changed since the backup before is
// not copied again: it is linked to the copy already there (a "hard link": two
// names for the same data), so fourteen days of backups take little more room
// than one. The link is always to an earlier BACKUP's copy, never to the live
// file, so nothing PrintHarbor writes later can change a backup.
//
// Not backed up, because it can be made again or isn't yours: sliced files
// (data\jobs), working folders, caches, the app window's browser profile, the
// AI model, logs, and the backups themselves.

var backupSkip = map[string]bool{"jobs": true, "slicer": true, "cache": true, "browser": true, "browser-downloads": true, "ai": true, "backups": true}

const backupStamp = "2006-01-02_150405"

// backupRoot is where backups go: the folder set in Settings, else
// data\backups.
func (a *App) backupRoot() string {
	if d := strings.TrimSpace(a.cfg().BackupDir); d != "" {
		return d
	}
	return filepath.Join(a.Store.Dir(), "backups")
}

// BackupInfo describes one backup for Settings.
type BackupInfo struct {
	Name  string    `json:"name"`
	At    time.Time `json:"at"`
	Files int       `json:"files"`
	Bytes int64     `json:"bytes"`
}

// backups lists the backups there are, newest first.
func (a *App) backups() []BackupInfo {
	out := []BackupInfo{}
	ents, _ := os.ReadDir(a.backupRoot())
	for _, e := range ents {
		at, err := time.ParseInLocation(backupStamp, e.Name(), time.Local)
		if err != nil || !e.IsDir() {
			continue
		}
		out = append(out, BackupInfo{Name: e.Name(), At: at})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// backupNow makes a backup and removes the ones older than the days kept. It
// returns what it made. One at a time.
func (a *App) backupNow(now time.Time) (BackupInfo, error) {
	a.backupMu.Lock()
	defer a.backupMu.Unlock()
	root := a.backupRoot()
	prev := ""
	if old := a.backups(); len(old) > 0 {
		prev = filepath.Join(root, old[0].Name)
	}
	info := BackupInfo{Name: now.Format(backupStamp), At: now}
	tmp := filepath.Join(root, info.Name+".part") // renamed when complete: a half-made backup is never taken for a whole one
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return info, fmt.Errorf("can't make the backup folder %s: %w", root, err)
	}
	fail := func(err error) (BackupInfo, error) {
		_ = os.RemoveAll(tmp)
		return info, err
	}
	data := a.Store.Dir()
	absRoot, _ := filepath.Abs(root)
	err := filepath.WalkDir(data, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // something vanished while we looked: carry on with the rest
		}
		rel, _ := filepath.Rel(data, path)
		if rel == "." {
			return nil
		}
		top := strings.Split(filepath.ToSlash(rel), "/")[0]
		if abs, _ := filepath.Abs(path); abs == absRoot || backupSkip[top] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		low := strings.ToLower(d.Name())
		if !d.Type().IsRegular() || strings.HasSuffix(low, ".log") || strings.HasSuffix(low, ".tmp") || low == "app-log.txt" {
			return nil
		}
		n, err := backupFile(path, filepath.Join(tmp, "data", rel), filepath.Join(prev, "data", rel), prev != "")
		if err != nil {
			return err
		}
		info.Files++
		info.Bytes += n
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if a.CfgPath != "" {
		if n, err := backupFile(a.CfgPath, filepath.Join(tmp, "config.json"), filepath.Join(prev, "config.json"), prev != ""); err == nil {
			info.Files++
			info.Bytes += n
		}
	}
	note := "A PrintHarbor backup, made " + now.Format("2 January 2006 at 15:04") + ".\r\n\r\n" +
		"To go back to it: quit PrintHarbor (tray icon, Quit), copy what is in the \"data\" folder here over PrintHarbor's own data folder, " +
		"copy config.json over the one beside the data folder, and start PrintHarbor again.\r\n" +
		"Sliced files are not in a backup; slice again.\r\n"
	_ = os.WriteFile(filepath.Join(tmp, "HOW TO RESTORE.txt"), []byte(note), 0o644)
	if err := os.Rename(tmp, filepath.Join(root, info.Name)); err != nil {
		return fail(err)
	}
	// Old ones go, but never the last few: a PC switched off for a month must not come back to none.
	keep := a.cfg().BackupKeepDays
	if keep > 0 {
		all := a.backups()
		for i, b := range all {
			if i >= 3 && now.Sub(b.At) > time.Duration(keep)*24*time.Hour {
				_ = os.RemoveAll(filepath.Join(root, b.Name))
			}
		}
	}
	return info, nil
}

// backupFile puts src into a backup as dst. When the backup before has the
// same file (same size, not older) dst becomes a link to that copy; otherwise,
// or when linking isn't possible there, it is copied. Returns the file's size.
func backupFile(src, dst, prev string, havePrev bool) (int64, error) {
	fi, err := os.Stat(src)
	if err != nil {
		return 0, nil // gone since it was listed
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	if havePrev {
		if pi, err := os.Stat(prev); err == nil && pi.Mode().IsRegular() && pi.Size() == fi.Size() && !pi.ModTime().Before(fi.ModTime()) {
			if os.Link(prev, dst) == nil {
				return fi.Size(), nil
			}
		}
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, nil // held open by something else right now: next time
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return 0, err
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	_ = os.Chtimes(dst, time.Now(), time.Now()) // "copied now": not older than the live file, so tomorrow's backup can link to it
	return fi.Size(), nil
}

// backupIfDue makes today's backup when the newest one is a day old or more.
func (a *App) backupIfDue(now time.Time) {
	if a.cfg().BackupKeepDays <= 0 {
		return
	}
	if old := a.backups(); len(old) > 0 && now.Sub(old[0].At) < 23*time.Hour {
		return
	}
	b, err := a.backupNow(now)
	if err != nil {
		log.Printf("Backup failed: %v", err)
		if now.Sub(a.backupWarned) < 23*time.Hour {
			return // said so already today
		}
		a.backupWarned = now
		a.Alert("", "error", "The daily backup of your projects and settings failed: "+err.Error(), nil)
		return
	}
	log.Printf("Backup: %d files, %.0f MB, in %s", b.Files, mb(b.Bytes), filepath.Join(a.backupRoot(), b.Name))
}
