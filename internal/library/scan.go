package library

import (
	"archive/zip"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Subfolders that, when they hold the models, mean "the folder above is the
// item" (how Printables/MakerWorld/Thingiverse downloads are laid out).
var modelDirs = map[string]bool{"files": true, "models": true, "model": true, "stl": true, "stls": true,
	"3mf": true, "print files": true, "print-files": true, "printfiles": true, "parts": true, "stl files": true}

// ScanResult summarizes a rescan.
type ScanResult struct {
	Folders int      `json:"folders"`
	Added   int      `json:"added"`
	Updated int      `json:"updated"`
	Removed int      `json:"removed"`
	Errors  []string `json:"errors"`
}

// Rescan walks the configured folders and syncs folder/zip items. Your edits
// to found items (title, tags, photos...) are kept.
func (l *Library) Rescan() ScanResult {
	res := ScanResult{Errors: []string{}}
	found := map[string]*Item{}
	for _, root := range l.folders {
		root = filepath.Clean(root)
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			res.Errors = append(res.Errors, "can't read folder "+root)
			continue
		}
		res.Folders++
		scanRoot(root, found, &res)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	for id, f := range found {
		if old, ok := l.items[id]; ok {
			changed := len(old.Files) != len(f.Files) || len(old.Photos) != len(f.Photos)
			// Keep measured sizes.
			sizes := map[string][3]float64{}
			for _, of := range old.Files {
				sizes[of.Name] = of.SizeMM
			}
			for i := range f.Files {
				f.Files[i].SizeMM = sizes[f.Files[i].Name]
			}
			old.Files, old.Photos, old.Path, old.Source, old.Folder = f.Files, f.Photos, f.Path, f.Source, f.Folder
			if old.Designer == "" {
				old.Designer = f.Designer
			}
			// Left over from the old scan rule: the designer folder was taken as the category,
			// and "My designs" as a designer.
			if f.Designer != "" && strings.EqualFold(old.Category, cleanCategory(f.Designer)) {
				old.Category = ""
			}
			if f.Designer == "" && strings.EqualFold(old.Designer, "My designs") {
				old.Designer = ""
				if strings.EqualFold(old.Category, "My designs") {
					old.Category = ""
				}
			}
			if changed {
				old.Updated = time.Now()
				res.Updated++
			}
			continue
		}
		l.items[id] = f
		res.Added++
	}
	for id, it := range l.items {
		if it.Source != "managed" && found[id] == nil {
			delete(l.items, id)
			res.Removed++
		}
	}
	_ = l.saveLocked()
	return res
}

// scanRoot reads a library folder laid out as <designer>\<model>\…:
//
//   - a first-level folder with subfolders is a designer: each of its subfolders is one model,
//     with everything under it (any depth) as that model's files; files lying loose in the
//     designer folder are not models
//   - a first-level folder with model files and no subfolders is one model with no designer
//   - model files and ZIPs lying loose in the library folder itself are each a model
func scanRoot(root string, found map[string]*Item, res *ScanResult) {
	entries, err := os.ReadDir(root)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		return
	}
	add := func(it *Item) {
		if it != nil {
			found[it.ID] = it
		}
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(root, name)
		if !e.IsDir() {
			if strings.EqualFold(filepath.Ext(name), ".zip") {
				add(zipItem(root, full))
			} else if isModel(name) {
				add(looseItem(root, full))
			}
			continue
		}
		sub, err := os.ReadDir(full)
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		var dirs []os.DirEntry
		hasModel := false
		for _, s := range sub {
			if strings.HasPrefix(s.Name(), ".") {
				continue
			}
			if s.IsDir() {
				dirs = append(dirs, s)
			} else if isModel(s.Name()) || strings.EqualFold(filepath.Ext(s.Name()), ".zip") {
				hasModel = true
			}
		}
		if len(dirs) == 0 {
			if hasModel {
				add(folderItem(root, full, "")) // a model with no designer folder
			}
			continue
		}
		designer := name
		if strings.EqualFold(name, "My designs") { // 3D James saves there: your own designs
			designer = ""
		}
		for _, d := range dirs { // a designer: each subfolder is a model
			if it := folderItem(root, filepath.Join(full, d.Name()), designer); it != nil && len(it.Files) > 0 {
				add(it) // (a folder with no model files in it yet is left out)
			}
		}
	}
}

func (l *Library) scanDir(root, dir string, found map[string]*Item, res *ScanResult, depth int) {
	if depth > 12 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		return
	}
	if dir != root && isItemDir(dir, entries) {
		if it := folderItem(root, dir, ""); it != nil {
			found[it.ID] = it
		}
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if e.IsDir() {
			l.scanDir(root, full, found, res, depth+1)
		} else if strings.EqualFold(filepath.Ext(name), ".zip") {
			if it := zipItem(root, full); it != nil {
				found[it.ID] = it
			}
		} else if isModel(name) && dir == root {
			// Loose model files at the top level: each is its own item.
			if it := looseItem(root, full); it != nil {
				found[it.ID] = it
			}
		}
	}
}

func isItemDir(dir string, entries []os.DirEntry) bool {
	for _, e := range entries {
		if !e.IsDir() && isModel(e.Name()) {
			return true
		}
		if e.IsDir() && modelDirs[strings.ToLower(e.Name())] {
			sub, _ := os.ReadDir(filepath.Join(dir, e.Name()))
			for _, s := range sub {
				if !s.IsDir() && isModel(s.Name()) {
					return true
				}
			}
		}
	}
	return false
}

// diskFolder is where a model sits: the library folder's name, then the
// subfolders down to the folder holding the model.
func diskFolder(root, parent string) string {
	name := filepath.Base(filepath.Clean(root))
	rel, err := filepath.Rel(root, parent)
	if err != nil || rel == "." {
		return name
	}
	return name + "/" + filepath.ToSlash(rel)
}

func category(root, parent string) string {
	rel, err := filepath.Rel(root, parent)
	if err != nil || rel == "." {
		return ""
	}
	return cleanCategory(filepath.ToSlash(rel))
}

func baseItem(root, path, source string) *Item {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	name := filepath.Base(path)
	if source != "folder" {
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	return &Item{ID: pathID(path), Title: prettyName(name + ".x"), Category: category(root, filepath.Dir(path)), Folder: diskFolder(root, filepath.Dir(path)),
		Source: source, Path: path, Created: fi.ModTime(), Updated: fi.ModTime(),
		Tags: []string{}, Files: []File{}, Photos: []string{}, AddedPhotos: []string{}}
}

// folderItem makes one model of a folder and everything in it. designer: the
// designer folder it is in ("" for none); printharbor.json in it wins.
func folderItem(root, dir, designer string) *Item {
	it := baseItem(root, dir, "folder")
	if it == nil {
		return nil
	}
	it.Designer = designer
	if designer != "" {
		it.Category = "" // the designer folder is not a category
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			if strings.Count(filepath.ToSlash(rel), "/") >= 30 || (rel != "." && strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		rel = filepath.ToSlash(rel)
		switch {
		case isModel(d.Name()) || strings.EqualFold(filepath.Ext(d.Name()), ".zip"):
			fi, _ := d.Info()
			var size int64
			if fi != nil {
				size = fi.Size()
			}
			it.Files = append(it.Files, File{Name: rel, Kind: Kind(d.Name()), Size: size})
		case IsImage(d.Name()):
			it.Photos = append(it.Photos, rel)
		case strings.EqualFold(d.Name(), "readme.txt") || strings.EqualFold(d.Name(), "readme.md") || strings.EqualFold(d.Name(), "description.txt"):
			if b, err := os.ReadFile(p); err == nil && len(b) < 64<<10 && it.Description == "" {
				it.Description = strings.TrimSpace(string(b))
			}
		}
		return nil
	})
	sortFiles(it)
	applySidecar(dir, it)
	return it
}

// Sidecar is printharbor.json, written next to models imported from a
// model site, so their details stay with the files on disk.
type Sidecar struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Designer    string   `json:"designer,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	License     string   `json:"license,omitempty"`
	SourceURL   string   `json:"source_url,omitempty"`
	Category    string   `json:"category,omitempty"`
	Site        string   `json:"site,omitempty"`
	SiteID      string   `json:"site_id,omitempty"`
	Imported    string   `json:"imported,omitempty"`
}

const SidecarName = "printharbor.json"

func applySidecar(dir string, it *Item) {
	b, err := os.ReadFile(filepath.Join(dir, SidecarName))
	if err != nil {
		return
	}
	var sc Sidecar
	if json.Unmarshal(b, &sc) != nil {
		return
	}
	if sc.Title != "" {
		it.Title = sc.Title
	}
	if sc.Description != "" {
		it.Description = sc.Description
	}
	if sc.Designer != "" {
		it.Designer = sc.Designer
	}
	it.License, it.SourceURL = sc.License, sc.SourceURL
	if sc.Category != "" {
		it.Category = cleanCategory(sc.Category)
	}
	if len(sc.Tags) > 0 {
		it.Tags = cleanTags(sc.Tags)
	}
}

func zipItem(root, path string) *Item {
	it := baseItem(root, path, "zip")
	if it == nil {
		return nil
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || strings.HasPrefix(filepath.Base(f.Name), ".") || strings.Contains(f.Name, "__MACOSX") {
			continue
		}
		switch {
		case isModel(f.Name):
			it.Files = append(it.Files, File{Name: f.Name, Kind: Kind(f.Name), Size: int64(f.UncompressedSize64)})
		case IsImage(f.Name):
			it.Photos = append(it.Photos, f.Name)
		}
	}
	if len(it.Files) == 0 {
		return nil // a zip with no models isn't a print item
	}
	sortFiles(it)
	return it
}

func looseItem(root, path string) *Item {
	it := baseItem(root, path, "folder")
	if it == nil {
		return nil
	}
	// The "folder" of a loose file is its directory; the file is the only entry.
	fi, _ := os.Stat(path)
	it.Path = filepath.Dir(path)
	it.ID = pathID(path)
	it.Files = []File{{Name: filepath.Base(path), Kind: Kind(path), Size: fi.Size()}}
	return it
}

func sortFiles(it *Item) {
	sort.Slice(it.Files, func(i, j int) bool { return strings.ToLower(it.Files[i].Name) < strings.ToLower(it.Files[j].Name) })
	sort.Strings(it.Photos)
}
