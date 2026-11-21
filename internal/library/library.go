// Package library is PrintHarbor's local model library: a private
// Printables/MakerWorld for everything you buy, download and design.
//
// Items come from two places:
//   - Managed items: uploaded through the web UI and stored in the data dir.
//   - Folder items: found by scanning folders you already have on disk. A
//     folder that holds model files (directly, or in a files/models/stls
//     subfolder) is one item; the folders above it become its category.
//     A .zip download sitting in a category folder is an item too.
//
// Folder items are never modified or deleted on disk. Edits (title, tags,
// photos you add...) are stored by PrintHarbor alongside.
package library

import (
	"archive/zip"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type File struct {
	Name   string     `json:"name"` // relative path inside the item
	Kind   string     `json:"kind"` // stl, 3mf, obj, step, gcode, zip, other
	Size   int64      `json:"size"`
	SizeMM [3]float64 `json:"size_mm,omitempty"`
}

type Item struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	Tags        []string  `json:"tags"`
	Designer    string    `json:"designer"`
	SourceURL   string    `json:"source_url"`
	License     string    `json:"license"`
	Notes       string    `json:"notes"`
	Favorite    bool      `json:"favorite"`
	PrintCount  int       `json:"print_count"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	Files       []File    `json:"files"`
	Photos      []string  `json:"photos"`       // photos found in the folder/zip or uploaded
	AddedPhotos []string  `json:"added_photos"` // uploaded through PrintHarbor (stored in data dir)
	Cover       string    `json:"cover"`        // a photo name; empty = first photo or model render
	Source      string    `json:"source"`       // managed | folder | zip
	Path        string    `json:"path,omitempty"`
	Hidden      bool      `json:"hidden,omitempty"`
	// Folder: where it is on disk, as "<library folder name>/<subfolders>" (the
	// folder that holds the model's own folder / zip / file). Empty for models
	// added in PrintHarbor without a folder.
	Folder string `json:"folder,omitempty"`
}

var modelExt = map[string]string{".3dj": "3dj", ".stl": "stl", ".3mf": "3mf", ".obj": "obj", ".step": "step", ".stp": "step",
	".gcode": "gcode", ".bgcode": "gcode", ".scad": "scad", ".f3d": "cad", ".amf": "amf", ".ply": "ply"}
var imageExt = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true}

// Kind classifies a file name.
func Kind(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if k, ok := modelExt[ext]; ok {
		return k
	}
	if ext == ".zip" {
		return "zip"
	}
	if imageExt[ext] {
		return "image"
	}
	return "other"
}

func isModel(name string) bool { _, ok := modelExt[strings.ToLower(filepath.Ext(name))]; return ok }
func IsImage(name string) bool { return imageExt[strings.ToLower(filepath.Ext(name))] }

// Printable reports whether PrintHarbor can slice this file.
func Printable(name string) bool {
	switch Kind(name) {
	case "stl", "3mf", "obj":
		return true
	}
	return false
}

type Library struct {
	dir     string
	mu      sync.RWMutex
	folders []string
	items   map[string]*Item
}

func Open(dir string, folders []string) (*Library, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	l := &Library{dir: dir, folders: folders, items: map[string]*Item{}}
	b, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err == nil {
		var list []*Item
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, fmt.Errorf("library index: %w", err)
		}
		for _, it := range list {
			l.items[it.ID] = it
		}
	}
	return l, nil
}

func (l *Library) Folders() []string { return l.folders }

func (l *Library) saveLocked() error {
	list := make([]*Item, 0, len(l.items))
	for _, it := range l.items {
		list = append(list, it)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	b, _ := json.MarshalIndent(list, "", "  ")
	tmp := filepath.Join(l.dir, "index.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(l.dir, "index.json"))
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func pathID(p string) string {
	h := sha1.Sum([]byte(strings.ToLower(filepath.Clean(p))))
	return "f" + hex.EncodeToString(h[:6])
}

func (l *Library) itemDir(id string) string { return filepath.Join(l.dir, "items", id) }

// --- queries ---

type Query struct {
	Text      string
	Category  string // exact, or prefix with sub-categories
	Tag       string
	Kind      string
	Favorites bool
	Folder    string // only models in this folder ("" = everywhere)
	Deep      bool   // with Folder: also models in its subfolders
	Sort      string // recent | name | printed
	Limit     int
	Offset    int
}

func (it *Item) haystack() string {
	var b strings.Builder
	b.WriteString(it.Title + " " + it.Description + " " + it.Category + " " + it.Designer + " " + it.Notes + " " + it.License + " ")
	b.WriteString(strings.Join(it.Tags, " ") + " ")
	for _, f := range it.Files {
		b.WriteString(f.Name + " ")
	}
	return strings.ToLower(b.String())
}

func (l *Library) Search(q Query) ([]Item, int) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	toks := strings.Fields(strings.ToLower(q.Text))
	var out []Item
	for _, it := range l.items {
		if it.Hidden {
			continue
		}
		if q.Favorites && !it.Favorite {
			continue
		}
		if q.Category != "" && it.Category != q.Category && !strings.HasPrefix(it.Category, q.Category+"/") {
			continue
		}
		if q.Folder == "-" && it.Folder != "" { // models added in PrintHarbor, in no folder
			continue
		}
		if q.Folder != "" && q.Folder != "-" && it.Folder != q.Folder && !(q.Deep && strings.HasPrefix(it.Folder, q.Folder+"/")) {
			continue
		}
		if q.Tag != "" && !containsFold(it.Tags, q.Tag) {
			continue
		}
		if q.Kind != "" {
			found := false
			for _, f := range it.Files {
				found = found || f.Kind == q.Kind
			}
			if !found {
				continue
			}
		}
		if len(toks) > 0 {
			h := it.haystack()
			ok := true
			for _, t := range toks {
				if !strings.Contains(h, t) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
		}
		out = append(out, *it)
	}
	switch q.Sort {
	case "name":
		sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title) })
	case "printed":
		sort.Slice(out, func(i, j int) bool { return out[i].PrintCount > out[j].PrintCount })
	default:
		sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	}
	total := len(out)
	if q.Offset > 0 {
		if q.Offset >= len(out) {
			out = nil
		} else {
			out = out[q.Offset:]
		}
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	if out == nil {
		out = []Item{}
	}
	return out, total
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// FolderCount is one folder of the library as it is on disk.
type FolderCount struct {
	Path  string `json:"path"`  // "Models/Ann"
	Items int    `json:"items"` // models directly in it
	Total int    `json:"total"` // models in it and its subfolders
}

// FolderTree lists every folder that holds models (directly or below), with
// its parents, sorted by path.
func (l *Library) FolderTree() []FolderCount {
	l.mu.RLock()
	defer l.mu.RUnlock()
	m := map[string]*FolderCount{}
	get := func(p string) *FolderCount {
		if m[p] == nil {
			m[p] = &FolderCount{Path: p}
		}
		return m[p]
	}
	for _, it := range l.items {
		if it.Hidden || it.Folder == "" {
			continue
		}
		get(it.Folder).Items++
		parts := strings.Split(it.Folder, "/")
		for i := range parts {
			get(strings.Join(parts[:i+1], "/")).Total++
		}
	}
	out := make([]FolderCount, 0, len(m))
	for _, f := range m {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path) })
	return out
}

type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Categories lists every category (including parents of nested ones).
func (l *Library) Categories() []Count {
	l.mu.RLock()
	defer l.mu.RUnlock()
	m := map[string]int{}
	for _, it := range l.items {
		if it.Hidden || it.Category == "" {
			continue
		}
		parts := strings.Split(it.Category, "/")
		for i := range parts {
			m[strings.Join(parts[:i+1], "/")]++
		}
	}
	return sortedCounts(m, false)
}

func (l *Library) Tags() []Count {
	l.mu.RLock()
	defer l.mu.RUnlock()
	m := map[string]int{}
	for _, it := range l.items {
		if it.Hidden {
			continue
		}
		for _, t := range it.Tags {
			m[strings.ToLower(t)]++
		}
	}
	return sortedCounts(m, true)
}

func sortedCounts(m map[string]int, byCount bool) []Count {
	out := []Count{}
	for k, v := range m {
		out = append(out, Count{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if byCount && out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (l *Library) Get(id string) (Item, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	it, ok := l.items[id]
	if !ok {
		return Item{}, false
	}
	return *it, true
}

// --- editing ---

// Meta is the editable metadata of an item.
type Meta struct {
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	Category    *string   `json:"category"`
	Tags        *[]string `json:"tags"`
	Designer    *string   `json:"designer"`
	SourceURL   *string   `json:"source_url"`
	License     *string   `json:"license"`
	Notes       *string   `json:"notes"`
	Favorite    *bool     `json:"favorite"`
	Cover       *string   `json:"cover"`
}

func cleanTags(tags []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#")))
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func cleanCategory(c string) string {
	var parts []string
	for _, p := range strings.Split(strings.ReplaceAll(c, "\\", "/"), "/") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "/")
}

func (l *Library) Update(id string, m Meta) (Item, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	it, ok := l.items[id]
	if !ok {
		return Item{}, fmt.Errorf("item not found")
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&it.Title, m.Title)
	set(&it.Description, m.Description)
	set(&it.Designer, m.Designer)
	set(&it.SourceURL, m.SourceURL)
	set(&it.License, m.License)
	set(&it.Notes, m.Notes)
	set(&it.Cover, m.Cover)
	if m.Category != nil {
		it.Category = cleanCategory(*m.Category)
	}
	if m.Tags != nil {
		it.Tags = cleanTags(*m.Tags)
	}
	if m.Favorite != nil {
		it.Favorite = *m.Favorite
	}
	if it.Title == "" {
		it.Title = "Untitled"
	}
	it.Updated = time.Now()
	return *it, l.saveLocked()
}

// Incoming is a file arriving for an item (moved into place).
type Incoming struct {
	Name string // original file name
	Tmp  string // temp path; moved, so the caller must not reuse it
}

// Create makes a managed item from uploaded files and photos.
func (l *Library) Create(m Meta, files, photos []Incoming) (Item, error) {
	it := &Item{ID: newID(), Source: "managed", Created: time.Now(), Updated: time.Now(), Tags: []string{}, Files: []File{}, Photos: []string{}, AddedPhotos: []string{}}
	if err := os.MkdirAll(filepath.Join(l.itemDir(it.ID), "files"), 0o755); err != nil {
		return Item{}, err
	}
	l.mu.Lock()
	l.items[it.ID] = it
	l.mu.Unlock()
	if _, err := l.AddFiles(it.ID, files); err != nil {
		l.Delete(it.ID)
		return Item{}, err
	}
	if _, err := l.AddPhotos(it.ID, photos); err != nil {
		l.Delete(it.ID)
		return Item{}, err
	}
	if m.Title == nil || strings.TrimSpace(*m.Title) == "" {
		t := "Untitled"
		if len(files) > 0 {
			t = prettyName(files[0].Name)
		}
		m.Title = &t
	}
	return l.Update(it.ID, m)
}

func prettyName(name string) string {
	s := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	s = strings.NewReplacer("_", " ", "-", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func safeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	return name
}

func uniqueName(dir, name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	cand := name
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, cand)); os.IsNotExist(err) {
			return cand
		}
		cand = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		in.Close()
		return err
	}
	_, err = io.Copy(out, in)
	in.Close()
	out.Close()
	if err != nil {
		return err
	}
	return os.Remove(src)
}

// AddFiles adds model files to a managed item.
func (l *Library) AddFiles(id string, files []Incoming) (Item, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	it, ok := l.items[id]
	if !ok {
		return Item{}, fmt.Errorf("item not found")
	}
	if it.Source != "managed" {
		return Item{}, fmt.Errorf("this item lives in a folder on your drive; add files there and rescan")
	}
	dir := filepath.Join(l.itemDir(id), "files")
	_ = os.MkdirAll(dir, 0o755)
	for _, f := range files {
		name := uniqueName(dir, safeName(f.Name))
		if err := moveFile(f.Tmp, filepath.Join(dir, name)); err != nil {
			return Item{}, err
		}
		fi, _ := os.Stat(filepath.Join(dir, name))
		it.Files = append(it.Files, File{Name: name, Kind: Kind(name), Size: fi.Size()})
	}
	it.Updated = time.Now()
	return *it, l.saveLocked()
}

// AddPhotos stores photos for any item (folder items included; the photos
// are kept in PrintHarbor's data dir, never written into your folders).
func (l *Library) AddPhotos(id string, photos []Incoming) (Item, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	it, ok := l.items[id]
	if !ok {
		return Item{}, fmt.Errorf("item not found")
	}
	dir := filepath.Join(l.itemDir(id), "photos")
	_ = os.MkdirAll(dir, 0o755)
	for _, p := range photos {
		if !IsImage(p.Name) {
			return Item{}, fmt.Errorf("%s is not a photo (jpg, png, webp, gif)", p.Name)
		}
		name := uniqueName(dir, safeName(p.Name))
		if err := moveFile(p.Tmp, filepath.Join(dir, name)); err != nil {
			return Item{}, err
		}
		it.AddedPhotos = append(it.AddedPhotos, name)
	}
	it.Updated = time.Now()
	return *it, l.saveLocked()
}

// RemoveFile deletes a file from a managed item, or an added photo from any item.
func (l *Library) RemoveFile(id, name string, photo bool) (Item, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	it, ok := l.items[id]
	if !ok {
		return Item{}, fmt.Errorf("item not found")
	}
	if photo {
		for i, p := range it.AddedPhotos {
			if p == name {
				_ = os.Remove(filepath.Join(l.itemDir(id), "photos", p))
				it.AddedPhotos = append(it.AddedPhotos[:i], it.AddedPhotos[i+1:]...)
				if it.Cover == "added:"+p {
					it.Cover = ""
				}
				return *it, l.saveLocked()
			}
		}
		return Item{}, fmt.Errorf("only photos added in PrintHarbor can be removed here")
	}
	if it.Source != "managed" {
		return Item{}, fmt.Errorf("this item lives in a folder on your drive; PrintHarbor won't delete files there")
	}
	for i, f := range it.Files {
		if f.Name == name {
			_ = os.Remove(filepath.Join(l.itemDir(id), "files", f.Name))
			it.Files = append(it.Files[:i], it.Files[i+1:]...)
			it.Updated = time.Now()
			return *it, l.saveLocked()
		}
	}
	return Item{}, fmt.Errorf("file not found")
}

// Delete removes a managed item and its files. Folder items are hidden
// instead (your files on disk are never deleted); a rescan won't bring them back.
func (l *Library) Delete(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	it, ok := l.items[id]
	if !ok {
		return fmt.Errorf("item not found")
	}
	if it.Source == "managed" {
		_ = os.RemoveAll(l.itemDir(id))
		delete(l.items, id)
	} else {
		it.Hidden = true
	}
	return l.saveLocked()
}

func (l *Library) MarkPrinted(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if it, ok := l.items[id]; ok {
		it.PrintCount++
		_ = l.saveLocked()
	}
}

// --- file access ---

// Open opens a file or photo of an item for reading. kind is "file",
// "photo" (found with the item) or "added" (uploaded photo).
func (l *Library) Open(id, kind, name string) (io.ReadCloser, int64, error) {
	it, ok := l.Get(id)
	if !ok {
		return nil, 0, fmt.Errorf("item not found")
	}
	name = filepath.ToSlash(filepath.Clean("/" + name))[1:] // no escaping the item
	listed := false
	switch kind {
	case "file":
		for _, f := range it.Files {
			listed = listed || f.Name == name
		}
	case "photo":
		for _, p := range it.Photos {
			listed = listed || p == name
		}
	case "added":
		for _, p := range it.AddedPhotos {
			listed = listed || p == name
		}
	}
	if !listed {
		return nil, 0, fmt.Errorf("not found")
	}
	if kind == "added" {
		f, err := os.Open(filepath.Join(l.itemDir(id), "photos", name))
		if err != nil {
			return nil, 0, err
		}
		fi, _ := f.Stat()
		return f, fi.Size(), nil
	}
	switch it.Source {
	case "managed":
		f, err := os.Open(filepath.Join(l.itemDir(id), "files", name))
		if err != nil {
			return nil, 0, err
		}
		fi, _ := f.Stat()
		return f, fi.Size(), nil
	case "folder":
		f, err := os.Open(filepath.Join(it.Path, filepath.FromSlash(name)))
		if err != nil {
			return nil, 0, err
		}
		fi, _ := f.Stat()
		return f, fi.Size(), nil
	case "zip":
		zr, err := zip.OpenReader(it.Path)
		if err != nil {
			return nil, 0, err
		}
		for _, f := range zr.File {
			if f.Name == name {
				rc, err := f.Open()
				if err != nil {
					zr.Close()
					return nil, 0, err
				}
				return &zipFile{rc, zr}, int64(f.UncompressedSize64), nil
			}
		}
		zr.Close()
	}
	return nil, 0, fmt.Errorf("not found")
}

type zipFile struct {
	io.ReadCloser
	zr *zip.ReadCloser
}

func (z *zipFile) Close() error { z.ReadCloser.Close(); return z.zr.Close() }

// LocalPath returns a path on disk for an item file, extracting it from a
// zip into the cache when needed (for slicing and previews).
func (l *Library) LocalPath(id, name string) (string, error) {
	it, ok := l.Get(id)
	if !ok {
		return "", fmt.Errorf("item not found")
	}
	switch it.Source {
	case "managed":
		return filepath.Join(l.itemDir(id), "files", name), nil
	case "folder":
		return filepath.Join(it.Path, filepath.FromSlash(name)), nil
	}
	cache := filepath.Join(l.dir, "cache", id, filepath.FromSlash(filepath.ToSlash(filepath.Clean("/" + name))[1:]))
	if _, err := os.Stat(cache); err == nil {
		return cache, nil
	}
	rc, _, err := l.Open(id, "file", name)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	_ = os.MkdirAll(filepath.Dir(cache), 0o755)
	out, err := os.Create(cache + ".tmp")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, rc)
	out.Close()
	if err != nil {
		return "", err
	}
	return cache, os.Rename(cache+".tmp", cache)
}

// SetFileSize records measured model dimensions.
func (l *Library) SetFileSize(id, name string, size [3]float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if it, ok := l.items[id]; ok {
		for i := range it.Files {
			if it.Files[i].Name == name && it.Files[i].SizeMM != size {
				it.Files[i].SizeMM = size
				_ = l.saveLocked()
			}
		}
	}
}

// ThumbDir is where rendered previews are cached.
func (l *Library) ThumbDir() string { return filepath.Join(l.dir, "thumbs") }

// SetFolders changes which folders are scanned (call Rescan afterwards).
func (l *Library) SetFolders(folders []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.folders = folders
}

// RenameFiles renames an item's files (old name → new name, in the same
// subfolder). Callers decide which folder items may be renamed on disk.
func (l *Library) RenameFiles(id string, names map[string]string) (Item, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	it, ok := l.items[id]
	if !ok {
		return Item{}, fmt.Errorf("item not found")
	}
	var base string
	switch it.Source {
	case "managed":
		base = filepath.Join(l.itemDir(id), "files")
	case "folder":
		base = it.Path
	default:
		return Item{}, fmt.Errorf("files inside a zip can't be renamed")
	}
	have := map[string]int{}
	for i, f := range it.Files {
		have[strings.ToLower(f.Name)] = i
	}
	// Check everything first, then rename.
	type mv struct {
		i        int
		from, to string
	}
	var moves []mv
	taken := map[string]bool{}
	for from, to := range names {
		i, ok := have[strings.ToLower(from)]
		if !ok {
			return Item{}, fmt.Errorf("%s isn't in this model", from)
		}
		to = strings.TrimSpace(to)
		if to == "" || strings.ContainsAny(to, `/\:*?"<>|`) || to == "." || to == ".." {
			return Item{}, fmt.Errorf("%q isn't a usable file name", to)
		}
		if !strings.EqualFold(filepath.Ext(to), filepath.Ext(from)) {
			to += filepath.Ext(from)
		}
		if dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(from))); dir != "." {
			to = dir + "/" + to
		}
		if to == from {
			continue
		}
		k := strings.ToLower(to)
		if j, clash := have[k]; (clash && names[it.Files[j].Name] == "") || taken[k] {
			return Item{}, fmt.Errorf("there is already a file called %s", filepath.Base(to))
		}
		taken[k] = true
		moves = append(moves, mv{i, from, to})
	}
	// Two steps (via temporary names) so swapping names works.
	for n, m := range moves {
		tmp := filepath.Join(base, filepath.FromSlash(m.from)) + fmt.Sprintf(".renaming%d", n)
		if err := os.Rename(filepath.Join(base, filepath.FromSlash(m.from)), tmp); err != nil {
			return Item{}, err
		}
	}
	for n, m := range moves {
		tmp := filepath.Join(base, filepath.FromSlash(m.from)) + fmt.Sprintf(".renaming%d", n)
		if err := os.Rename(tmp, filepath.Join(base, filepath.FromSlash(m.to))); err != nil {
			return Item{}, err
		}
		it.Files[m.i].Name = m.to
	}
	sort.Slice(it.Files, func(a, b int) bool { return strings.ToLower(it.Files[a].Name) < strings.ToLower(it.Files[b].Name) })
	it.Updated = time.Now()
	return *it, l.saveLocked()
}
