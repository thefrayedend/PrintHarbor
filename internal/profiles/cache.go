package profiles

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Reading OrcaSlicer's ~10,000 profile files one by one is slow on Windows
// (each file open is checked by the virus scanner, and after an update the
// disk cache is cold): startup took minutes. LoadDirCached keeps everything it
// read in one file and reuses it while the profile folder is unchanged (same
// files, sizes and dates), which takes well under a second.

type profileCache struct {
	Sig   string        `json:"sig"`
	Dir   string        `json:"dir"`
	Files []cachedEntry `json:"files"`
}

type cachedEntry struct {
	Vendor string         `json:"v"`
	Path   string         `json:"p"`
	Raw    map[string]any `json:"r"`
}

// dirSignature fingerprints the .json files under dir from the directory
// listing alone (no file is opened).
func dirSignature(dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".json") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

// LoadDirCached is LoadDir with a cache file (see above). It reports whether
// the cache was used.
func (l *Library) LoadDirCached(dir, source, cacheFile string) (bool, error) {
	if dir == "" {
		return false, nil
	}
	if _, err := os.Stat(dir); err != nil {
		return false, err
	}
	sig, err := dirSignature(dir)
	if err == nil && cacheFile != "" {
		if b, err := os.ReadFile(cacheFile); err == nil {
			var c profileCache
			if json.Unmarshal(b, &c) == nil && c.Sig == sig && c.Dir == dir && len(c.Files) > 0 {
				for _, f := range c.Files {
					l.Add(f.Raw, f.Vendor, source, f.Path)
				}
				return true, nil
			}
		}
	}
	// Read the folder and remember what was read.
	c := profileCache{Sig: sig, Dir: dir}
	l.onAdd = func(raw map[string]any, vendor, path string) {
		c.Files = append(c.Files, cachedEntry{Vendor: vendor, Path: path, Raw: raw})
	}
	err = l.LoadDir(dir, source)
	l.onAdd = nil
	if err == nil && cacheFile != "" && sig != "" {
		if b, err := json.Marshal(c); err == nil {
			_ = os.MkdirAll(filepath.Dir(cacheFile), 0o755)
			tmp := cacheFile + ".tmp"
			if os.WriteFile(tmp, b, 0o644) == nil {
				_ = os.Rename(tmp, cacheFile)
			}
		}
	}
	return false, err
}
