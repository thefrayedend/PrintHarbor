package main

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const defaultPort = "7621"

// appConfig is the little the app window needs to know about the server.
type appConfig struct {
	Port    string // the port PrintHarbor listens on
	DataDir string // PrintHarbor's data folder ("" when it can't be worked out)
}

// loadAppConfig reads PrintHarbor's config.json so the window follows the
// server when its port ("listen") or data folder is changed there. It looks
// where the server looks: an explicit -config path, then next to the program,
// then one folder up (the launcher keeps the programs in dist\).
func loadAppConfig(exeDir string, args []string) appConfig {
	c := appConfig{Port: defaultPort}
	var paths []string
	for i, a := range args {
		if (a == "-config" || a == "--config") && i+1 < len(args) {
			paths = append(paths, args[i+1])
		}
	}
	if exeDir != "" {
		paths = append(paths, filepath.Join(exeDir, "config.json"), filepath.Join(filepath.Dir(exeDir), "config.json"))
		c.DataDir = filepath.Join(filepath.Dir(exeDir), "data") // where it is when there's no config to say
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var raw struct {
			Listen  string `json:"listen"`
			DataDir string `json:"data_dir"`
		}
		if json.Unmarshal(b, &raw) != nil {
			return c // a config we can't read: keep the defaults
		}
		if _, port, err := net.SplitHostPort(raw.Listen); err == nil {
			if n, err := strconv.Atoi(port); err == nil && n > 0 && n < 65536 {
				c.Port = strconv.Itoa(n)
			}
		}
		// The server works from the config's folder, so a relative data_dir is relative to that.
		d := raw.DataDir
		if d == "" {
			d = "data"
		}
		if !filepath.IsAbs(d) {
			d = filepath.Join(filepath.Dir(p), d)
		}
		c.DataDir = d
		return c
	}
	return c
}

// appendCapped adds a line to a log file, and when the file has grown past
// max bytes keeps only its last keep bytes first (cut at a line start), so
// the log can't grow forever.
func appendCapped(path, line string, max, keep int64) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > max {
		if f, err := os.Open(path); err == nil {
			if _, err := f.Seek(-keep, io.SeekEnd); err == nil {
				tail, _ := io.ReadAll(f)
				f.Close()
				for i, c := range tail { // drop the partial first line
					if c == '\n' {
						tail = tail[i+1:]
						break
					}
				}
				_ = os.WriteFile(path, append([]byte("(older lines removed)\n"), tail...), 0o644)
			} else {
				f.Close()
			}
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line + "\n")
	f.Close()
}

// fileStamp identifies one version of a file on disk.
type fileStamp struct {
	size int64
	mod  time.Time
}

func stampOf(path string) (fileStamp, bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return fileStamp{}, false
	}
	return fileStamp{fi.Size(), fi.ModTime()}, true
}

// replaced reports whether the file at path is no longer the one that was
// there when was was taken (a missing file is "not yet", not "replaced": an
// update is swapped in with two renames and we may look in between).
func replaced(path string, was fileStamp) bool {
	now, ok := stampOf(path)
	return ok && (now.size != was.size || !now.mod.Equal(was.mod))
}

// reopenWindow is how long before an update was noticed a page load still
// counts as "just now" (the window only looks for an update every 2 seconds).
const reopenWindow = 6 * time.Second

// safeToReopen decides when a window whose program was updated may close and
// reopen itself: only when PrintHarbor's own page has (re)loaded in it at
// about the time the update was noticed, or later. At that moment nothing
// unsaved can be open in the window (a design in 3D James, plates in the
// slicer), so nothing is lost. noticed is when the update was noticed,
// ownPage when PrintHarbor's own page last loaded (zero: never).
func safeToReopen(noticed, ownPage time.Time) bool {
	return !ownPage.IsZero() && ownPage.After(noticed.Add(-reopenWindow))
}
