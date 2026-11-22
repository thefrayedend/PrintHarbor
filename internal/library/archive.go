package library

import (
	"archive/zip"
	"compress/flate"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A library backup is ONE zip that holds the whole model library: every file
// in the library folders (models, photos, PDFs, the printharbor.json details
// written when a model was imported from a site…), the photos added in
// PrintHarbor, models uploaded into PrintHarbor, and what you changed in
// PrintHarbor (titles, tags, notes, favourites, print counts, covers, hidden
// models). Another PrintHarbor (a new PC, a mini PC, Docker) restores it with
// ImportArchive + RestoreArchive.
//
// Inside the zip:
//
//	printharbor-library.json          what is in it (ArchiveManifest)
//	<root name>/<…>                   each library folder as it is on disk
//	                                  ("Models" is the imported-models folder)
//	PrintHarbor uploads/<id>/<file>   models uploaded into PrintHarbor
//	Added photos/<id>/<photo>         photos added in PrintHarbor
//
// It opens in any zip program too: the folders are plain folders.

const (
	ArchiveManifestName = "printharbor-library.json"
	archiveFormat       = 1
	archUploads         = "PrintHarbor uploads"
	archPhotos          = "Added photos"
)

// ArchiveRoot is one library folder and its name inside the zip.
type ArchiveRoot struct {
	Name string `json:"name"`
	Dir  string `json:"-"`
}

// ArchiveModel is one model's details in the manifest.
type ArchiveModel struct {
	Root string `json:"root,omitempty"` // which root it is in ("" for uploaded models)
	Path string `json:"path,omitempty"` // where in that root, "/"-separated
	Item Item   `json:"item"`
}

type ArchiveManifest struct {
	Format int            `json:"format"`
	Made   time.Time      `json:"made"`
	By     string         `json:"by"`
	Roots  []ArchiveRoot  `json:"roots"`
	Models []ArchiveModel `json:"models"`
	Files  int            `json:"files"`
	Bytes  int64          `json:"bytes"`
}

// ArchiveStats says what went into a backup.
type ArchiveStats struct {
	Models int   `json:"models"`
	Files  int   `json:"files"`
	Bytes  int64 `json:"bytes"`
}

// Files that are already compressed are stored as they are: squeezing them
// again takes time and saves nothing.
var storedExt = map[string]bool{".3mf": true, ".zip": true, ".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".webp": true, ".mp4": true, ".mov": true, ".7z": true, ".rar": true, ".gz": true, ".bgcode": true, ".pdf": true, ".f3z": true}

func skipBackupFile(name string) bool {
	low := strings.ToLower(name)
	return strings.HasSuffix(low, ".part") || strings.HasSuffix(low, ".tmp") || low == "thumbs.db" || low == "desktop.ini" || low == ".ds_store"
}

func underDir(dir, p string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// Export writes the whole library into a zip on w. skip is a file never to
// put in (the backup itself, when it is kept inside a library folder).
func (l *Library) Export(w io.Writer, roots []ArchiveRoot, by string, skip string) (ArchiveStats, error) {
	var st ArchiveStats
	l.mu.RLock()
	items := make([]Item, 0, len(l.items))
	for _, it := range l.items {
		items = append(items, *it)
	}
	l.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })

	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) { return flate.NewWriter(out, flate.BestSpeed) })
	add := func(name, src string, fi os.FileInfo) error {
		h, err := zip.FileInfoHeader(fi)
		if err != nil {
			return err
		}
		h.Name = name
		h.Method = zip.Deflate
		if storedExt[strings.ToLower(path.Ext(name))] {
			h.Method = zip.Store
		}
		in, err := os.Open(src)
		if err != nil {
			return nil // held open or gone: leave it out rather than fail the whole backup
		}
		defer in.Close()
		out, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		n, err := io.Copy(out, in)
		if err != nil {
			return fmt.Errorf("%s: %w", src, err)
		}
		st.Files++
		st.Bytes += n
		return nil
	}
	skipAbs, _ := filepath.Abs(skip)

	m := ArchiveManifest{Format: archiveFormat, Made: time.Now(), By: by, Roots: roots, Models: []ArchiveModel{}}
	for _, r := range roots {
		if fi, err := os.Stat(r.Dir); err != nil || !fi.IsDir() {
			continue
		}
		err := filepath.WalkDir(r.Dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() || skipBackupFile(d.Name()) {
				return nil
			}
			if abs, _ := filepath.Abs(p); skip != "" && strings.EqualFold(abs, skipAbs) {
				return nil
			}
			rel, ok := underDir(r.Dir, p)
			if !ok {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			return add(r.Name+"/"+rel, p, fi)
		})
		if err != nil {
			return st, err
		}
	}
	for _, it := range items {
		am := ArchiveModel{Item: it}
		if it.Source == "managed" {
			dir := filepath.Join(l.itemDir(it.ID), "files")
			for _, f := range it.Files {
				if fi, err := os.Stat(filepath.Join(dir, f.Name)); err == nil {
					if err := add(archUploads+"/"+it.ID+"/"+f.Name, filepath.Join(dir, f.Name), fi); err != nil {
						return st, err
					}
				}
			}
		} else {
			for _, r := range roots {
				if rel, ok := underDir(r.Dir, it.Path); ok {
					am.Root, am.Path = r.Name, rel
					break
				}
			}
			if am.Root == "" {
				continue // not in a library folder being backed up
			}
		}
		for _, ph := range it.AddedPhotos {
			src := filepath.Join(l.itemDir(it.ID), "photos", ph)
			if fi, err := os.Stat(src); err == nil {
				if err := add(archPhotos+"/"+it.ID+"/"+ph, src, fi); err != nil {
					return st, err
				}
			}
		}
		m.Models = append(m.Models, am)
	}
	st.Models = len(m.Models)
	m.Files, m.Bytes = st.Files, st.Bytes
	b, _ := json.MarshalIndent(m, "", " ")
	out, err := zw.CreateHeader(&zip.FileHeader{Name: ArchiveManifestName, Method: zip.Deflate, Modified: m.Made})
	if err != nil {
		return st, err
	}
	if _, err := out.Write(b); err != nil {
		return st, err
	}
	return st, zw.Close()
}

// ReadArchive opens a library backup and reads what is in it.
func ReadArchive(zipPath string) (*zip.ReadCloser, *ArchiveManifest, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, nil, fmt.Errorf("that isn't a zip file PrintHarbor can read: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != ArchiveManifestName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			zr.Close()
			return nil, nil, err
		}
		var m ArchiveManifest
		err = json.NewDecoder(io.LimitReader(rc, 64<<20)).Decode(&m)
		rc.Close()
		if err != nil {
			zr.Close()
			return nil, nil, fmt.Errorf("the backup's list of models is damaged: %w", err)
		}
		if m.Format < 1 || m.Format > archiveFormat {
			zr.Close()
			return nil, nil, fmt.Errorf("this backup was made by a newer PrintHarbor; update this one first")
		}
		return zr, &m, nil
	}
	zr.Close()
	return nil, nil, fmt.Errorf("that zip isn't a PrintHarbor library backup (it has no %s)", ArchiveManifestName)
}

// ImportResult says what a restore did.
type ImportResult struct {
	Models   int      `json:"models"`   // models whose details were restored
	Written  int      `json:"written"`  // files put on disk
	Same     int      `json:"same"`     // files that were already there
	Renamed  int      `json:"renamed"`  // files kept beside a different file of the same name
	Uploaded int      `json:"uploaded"` // models uploaded into PrintHarbor, restored
	Errors   []string `json:"errors"`
}

func cleanZipName(name string) (string, bool) {
	if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return "", false
	}
	c := path.Clean(name)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

func extractTo(f *zip.File, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, rc)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Chtimes(dst, f.Modified, f.Modified)
	return nil
}

// ImportArchive puts the files of a library backup into folders: dest maps
// each root name in the backup to the folder it goes into. Nothing already
// there is overwritten: a file that is there with the same size is taken as
// the same file; a different file of the same name stays, and the one from
// the backup is put beside it as "<name> (restored)". The caller then rescans
// the library and calls RestoreArchive.
func ImportArchive(zr *zip.ReadCloser, dest map[string]string) ImportResult {
	res := ImportResult{Errors: []string{}}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || f.Name == ArchiveManifestName {
			continue
		}
		name, ok := cleanZipName(f.Name)
		if !ok {
			res.Errors = append(res.Errors, "left out a file with an unsafe name: "+f.Name)
			continue
		}
		root, rel, _ := strings.Cut(name, "/")
		dir, ok := dest[root]
		if !ok || rel == "" {
			continue // uploads and added photos: RestoreArchive
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if fi, err := os.Stat(dst); err == nil {
			if fi.Size() == int64(f.UncompressedSize64) {
				res.Same++
				continue
			}
			ext := filepath.Ext(dst)
			dst = strings.TrimSuffix(dst, ext) + " (restored)" + ext
			if _, err := os.Stat(dst); err == nil {
				res.Same++
				continue
			}
			res.Renamed++
		}
		if err := extractTo(f, dst); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		res.Written++
	}
	return res
}

// RestoreArchive brings back what was changed in PrintHarbor (details,
// photos added here, models uploaded here) for the models of a backup whose
// files ImportArchive put into dest. Call it after rescanning the library.
func (l *Library) RestoreArchive(zr *zip.ReadCloser, m *ArchiveManifest, dest map[string]string, res *ImportResult) {
	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		byName[f.Name] = f
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, am := range m.Models {
		old := am.Item
		var it *Item
		if old.Source == "managed" {
			it = &Item{ID: newID(), Source: "managed", Files: []File{}}
			if l.items[old.ID] == nil {
				it.ID = old.ID // keep its id when it is free: links to it keep working
			}
			dir := filepath.Join(l.itemDir(it.ID), "files")
			for _, f := range old.Files {
				zf := byName[archUploads+"/"+old.ID+"/"+f.Name]
				if zf == nil {
					continue
				}
				name := safeName(f.Name)
				if err := extractTo(zf, filepath.Join(dir, name)); err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", old.Title, err))
					continue
				}
				it.Files = append(it.Files, File{Name: name, Kind: Kind(name), Size: f.Size, SizeMM: f.SizeMM})
			}
			if len(it.Files) == 0 {
				res.Errors = append(res.Errors, old.Title+": its files are not in the backup")
				continue
			}
			l.items[it.ID] = it
			res.Uploaded++
		} else {
			dir, ok := dest[am.Root]
			if !ok {
				continue
			}
			it = l.items[pathID(filepath.Join(dir, filepath.FromSlash(am.Path)))]
			if it == nil {
				res.Errors = append(res.Errors, old.Title+": not found after restoring its files")
				continue
			}
			res.Models++
		}
		// What you set in PrintHarbor.
		it.Title, it.Description, it.Category, it.Designer = old.Title, old.Description, old.Category, old.Designer
		it.SourceURL, it.License, it.Notes, it.Favorite, it.Hidden = old.SourceURL, old.License, old.Notes, old.Favorite, old.Hidden
		it.Tags = append([]string{}, old.Tags...)
		if old.PrintCount > it.PrintCount {
			it.PrintCount = old.PrintCount
		}
		if !old.Created.IsZero() {
			it.Created = old.Created
		}
		if it.Updated.Before(old.Updated) {
			it.Updated = old.Updated
		}
		sizes := map[string][3]float64{}
		for _, f := range old.Files {
			sizes[f.Name] = f.SizeMM
		}
		for i := range it.Files {
			if s, ok := sizes[it.Files[i].Name]; ok && it.Files[i].SizeMM == ([3]float64{}) {
				it.Files[i].SizeMM = s
			}
		}
		// Photos added in PrintHarbor.
		have := map[string]bool{}
		for _, p := range it.AddedPhotos {
			have[p] = true
		}
		pdir := filepath.Join(l.itemDir(it.ID), "photos")
		for _, p := range old.AddedPhotos {
			zf := byName[archPhotos+"/"+old.ID+"/"+p]
			if zf == nil || have[p] {
				continue
			}
			if err := extractTo(zf, filepath.Join(pdir, safeName(p))); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s photo %s: %v", old.Title, p, err))
				continue
			}
			it.AddedPhotos = append(it.AddedPhotos, safeName(p))
			have[p] = true
		}
		if it.AddedPhotos == nil {
			it.AddedPhotos = []string{}
		}
		if it.Photos == nil {
			it.Photos = []string{}
		}
		it.Cover = old.Cover
	}
	if err := l.saveLocked(); err != nil {
		res.Errors = append(res.Errors, "saving the library: "+err.Error())
	}
}
