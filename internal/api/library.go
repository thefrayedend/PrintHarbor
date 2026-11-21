package api

import (
	"archive/zip"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/library"
	"github.com/thefrayedend/printharbor/internal/mesh"
	"github.com/thefrayedend/printharbor/internal/threemf"
)

const (
	thumbSize   = 420
	previewTris = 400000 // browser viewer cap; huge models are sampled
)

// --- previews (shared by the Library and STL plates) ---

func hashKey(parts ...string) string {
	h := sha1.New()
	for _, p := range parts {
		io.WriteString(h, p+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// previewPNG returns a cached render (or embedded 3MF thumbnail) for a model file.
func (a *App) previewPNG(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	cache := filepath.Join(a.Library.ThumbDir(), hashKey(path, fi.ModTime().String(), strconv.FormatInt(fi.Size(), 10))+".png")
	if b, err := os.ReadFile(cache); err == nil {
		return b, nil
	}
	var png []byte
	if strings.EqualFold(filepath.Ext(path), ".3dj") {
		// A 3D James project carries its own picture.
		if zr, err := zip.OpenReader(path); err == nil {
			for _, zf := range zr.File {
				if zf.Name == "preview.png" && zf.UncompressedSize64 < 16<<20 {
					if rc, err := zf.Open(); err == nil {
						png, _ = io.ReadAll(rc)
						rc.Close()
					}
				}
			}
			zr.Close()
		}
		if len(png) == 0 {
			return nil, fmt.Errorf("no preview in this project")
		}
	}
	if png == nil && strings.EqualFold(filepath.Ext(path), ".3mf") {
		if f, err := os.Open(path); err == nil {
			for _, n := range []string{"Metadata/plate_1.png", "Metadata/thumbnail.png", "Metadata/top_1.png"} {
				if b, err := threemf.Thumbnail(f, fi.Size(), n); err == nil && len(b) > 0 {
					png = b
					break
				}
			}
			f.Close()
		}
	}
	if png == nil {
		a.render <- struct{}{}
		m, err := mesh.Load(path, previewTris)
		if err == nil {
			png, err = m.RenderPNG(thumbSize)
		}
		<-a.render
		if err != nil {
			return nil, err
		}
	}
	_ = os.MkdirAll(filepath.Dir(cache), 0o755)
	_ = os.WriteFile(cache, png, 0o644)
	return png, nil
}

func (a *App) meshBinary(paths []string) ([]byte, [3]float64, error) {
	a.render <- struct{}{}
	defer func() { <-a.render }()
	var ms []*mesh.Mesh
	for _, p := range paths {
		m, err := mesh.Load(p, previewTris/len(paths))
		if err != nil {
			return nil, [3]float64{}, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		ms = append(ms, m)
	}
	m := ms[0]
	if len(ms) > 1 {
		m = mesh.Merge(ms, 10)
	}
	return m.Binary(), ms[0].Size(), nil
}

// platePreview renders all parts of an STL plate side by side (cached).
func (a *App) platePreview(p *Project, plate int) ([]byte, error) {
	paths := a.meshInputs(p, plate)
	if len(paths) == 0 {
		return nil, fmt.Errorf("empty plate")
	}
	if len(paths) == 1 {
		return a.previewPNG(paths[0])
	}
	cache := filepath.Join(a.Library.ThumbDir(), "plate-"+hashKey(paths...)+".png")
	if b, err := os.ReadFile(cache); err == nil {
		return b, nil
	}
	a.render <- struct{}{}
	defer func() { <-a.render }()
	var ms []*mesh.Mesh
	for _, pth := range paths {
		m, err := mesh.Load(pth, previewTris/len(paths))
		if err != nil {
			return nil, err
		}
		ms = append(ms, m)
	}
	b, err := mesh.Merge(ms, 10).RenderPNG(thumbSize)
	if err != nil {
		return nil, err
	}
	_ = os.MkdirAll(filepath.Dir(cache), 0o755)
	_ = os.WriteFile(cache, b, 0o644)
	return b, nil
}

func writePNG(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "max-age=3600")
	w.Write(b)
}

// coverRef picks what to show on an item's card: chosen cover, first added
// photo, first found photo, or a render of the first printable model.
func coverRef(it library.Item) (kind, name string) {
	if c := it.Cover; c != "" {
		if k, n, ok := strings.Cut(c, ":"); ok {
			return k, n
		}
	}
	if len(it.AddedPhotos) > 0 {
		return "added", it.AddedPhotos[0]
	}
	if len(it.Photos) > 0 {
		return "photo", it.Photos[0]
	}
	for _, f := range it.Files {
		if library.Printable(f.Name) || f.Kind == "3dj" {
			return "file", f.Name
		}
	}
	return "", ""
}

func (a *App) libraryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/library", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		offset, _ := strconv.Atoi(q.Get("offset"))
		items, total := a.Library.Search(library.Query{Text: q.Get("q"), Category: q.Get("category"), Tag: q.Get("tag"),
			Kind: q.Get("kind"), Favorites: q.Get("favorites") == "1", Folder: q.Get("folder"), Deep: q.Get("deep") == "1", Sort: q.Get("sort"), Limit: limit, Offset: offset})
		writeJSON(w, 200, map[string]any{"items": items, "total": total})
	})
	mux.HandleFunc("GET /api/library/facets", func(w http.ResponseWriter, r *http.Request) {
		_, added := a.Library.Search(library.Query{Folder: "-", Limit: 1})
		writeJSON(w, 200, map[string]any{"added": added, "categories": a.Library.Categories(), "tags": a.Library.Tags(), "tree": a.Library.FolderTree(), "folders": nonNil(a.cfg().LibraryFolders), "model_dir": a.cfg().ModelsDir()})
	})
	mux.HandleFunc("POST /api/library/rescan", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, a.Library.Rescan())
	})
	mux.HandleFunc("POST /api/library/folders", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Folders []string `json:"folders"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		clean := []string{}
		for _, f := range in.Folders {
			f = strings.TrimSpace(strings.Trim(strings.TrimSpace(f), `"`))
			if f == "" {
				continue
			}
			if fi, err := os.Stat(f); err != nil || !fi.IsDir() {
				fail(w, 400, fmt.Errorf("can't find the folder %q on this computer", f))
				return
			}
			clean = append(clean, f)
		}
		a.cfgEdit.Lock()
		c := a.cfg()
		c.LibraryFolders = clean
		a.setCfg(c)
		a.cfgEdit.Unlock()
		a.Library.SetFolders(a.cfg().ScanFolders())
		if a.CfgPath != "" {
			_ = config.Save(a.CfgPath, a.cfg())
		}
		writeJSON(w, 200, a.Library.Rescan())
	})
	mux.HandleFunc("POST /api/library", func(w http.ResponseWriter, r *http.Request) {
		files, photos, meta, err := a.readLibraryUpload(w, r)
		if err != nil {
			fail(w, 400, err)
			return
		}
		it, err := a.Library.Create(meta, files, photos)
		if err != nil {
			removeIncoming(files, photos)
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, it)
	})
	mux.HandleFunc("GET /api/library/{id}", func(w http.ResponseWriter, r *http.Request) {
		it, ok := a.Library.Get(r.PathValue("id"))
		if !ok {
			fail(w, 404, fmt.Errorf("item not found"))
			return
		}
		writeJSON(w, 200, it)
	})
	mux.HandleFunc("PUT /api/library/{id}", func(w http.ResponseWriter, r *http.Request) {
		var m library.Meta
		if err := readJSON(r, &m); err != nil {
			fail(w, 400, err)
			return
		}
		it, err := a.Library.Update(r.PathValue("id"), m)
		if err != nil {
			fail(w, 400, err)
			return
		}
		// Details go into printharbor.json in the model's folder, so they travel with the files.
		if m.Title != nil || m.Designer != nil || m.SourceURL != nil || m.Description != nil || m.Tags != nil || m.Category != nil || m.License != nil {
			if moved, ok := a.refileImported(it); ok {
				it = moved
			}
		}
		writeJSON(w, 200, it)
	})
	mux.HandleFunc("DELETE /api/library/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Library.Delete(r.PathValue("id")); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/library/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		files, photos, _, err := a.readLibraryUpload(w, r)
		if err != nil {
			fail(w, 400, err)
			return
		}
		id := r.PathValue("id")
		if len(files) > 0 {
			if _, err := a.Library.AddFiles(id, files); err != nil {
				removeIncoming(files, photos)
				fail(w, 400, err)
				return
			}
		}
		if len(photos) > 0 {
			if _, err := a.Library.AddPhotos(id, photos); err != nil {
				removeIncoming(nil, photos)
				fail(w, 400, err)
				return
			}
		}
		out, _ := a.Library.Get(id)
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("DELETE /api/library/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		it, err := a.Library.RemoveFile(r.PathValue("id"), q.Get("name"), q.Get("photo") == "1")
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, it)
	})
	// Raw file / photo download: kind = file | photo | added.
	// Rename files (PrintHarbor only renames files it manages: uploaded
	// models and models in your models folder, never your own folders).
	mux.HandleFunc("POST /api/library/{id}/rename", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Names map[string]string `json:"names"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		it, ok := a.Library.Get(r.PathValue("id"))
		if !ok {
			fail(w, 404, fmt.Errorf("item not found"))
			return
		}
		if it.Source == "folder" {
			if rel, err := filepath.Rel(a.cfg().ModelsDir(), it.Path); err != nil || strings.HasPrefix(rel, "..") {
				fail(w, 400, fmt.Errorf("this model is in one of your own folders, which PrintHarbor never changes; rename the files there"))
				return
			}
		}
		out, err := a.Library.RenameFiles(it.ID, in.Names)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/library/{id}/raw/{kind}", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		rc, size, err := a.Library.Open(r.PathValue("id"), r.PathValue("kind"), name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer rc.Close()
		if library.IsImage(name) {
			ext := strings.ToLower(filepath.Ext(name))
			ct := map[string]string{".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".webp": "image/webp", ".gif": "image/gif"}[ext]
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Cache-Control", "max-age=3600")
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(name)))
		}
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		io.Copy(w, rc)
	})
	mux.HandleFunc("GET /api/library/{id}/cover", func(w http.ResponseWriter, r *http.Request) {
		it, ok := a.Library.Get(r.PathValue("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		kind, name := coverRef(it)
		switch kind {
		case "added", "photo":
			http.Redirect(w, r, fmt.Sprintf("/api/library/%s/raw/%s?name=%s", it.ID, kind, urlEscape(name)), http.StatusFound)
		case "file":
			a.serveItemThumb(w, r, it.ID, name)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("GET /api/library/{id}/thumb", func(w http.ResponseWriter, r *http.Request) {
		a.serveItemThumb(w, r, r.PathValue("id"), r.URL.Query().Get("name"))
	})
	mux.HandleFunc("GET /api/library/{id}/mesh", func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("id"), r.URL.Query().Get("name")
		p, err := a.Library.LocalPath(id, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		b, size, err := a.meshBinary([]string{p})
		if err != nil {
			http.Error(w, err.Error(), 422)
			return
		}
		a.Library.SetFileSize(id, name, size)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(b)
	})
	mux.HandleFunc("POST /api/library/{id}/print", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Files     []string `json:"files"`
			ProjectID string   `json:"project_id"`
			Plate     int      `json:"plate"`
			As3MF     bool     `json:"as_3mf"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		p, err := a.PrintFromLibrary(r.PathValue("id"), in.Files, in.ProjectID, in.Plate, in.As3MF)
		if errors.Is(err, errNeedPrintable) {
			writeJSON(w, 409, map[string]any{"error": err.Error(), "need_printable": true})
			return
		}
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, p)
	})
	mux.HandleFunc("POST /api/projects/{id}/to-library", func(w http.ResponseWriter, r *http.Request) {
		var m library.Meta
		_ = readJSON(r, &m)
		it, err := a.ProjectToLibrary(r.PathValue("id"), m)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, it)
	})
}

func urlEscape(s string) string {
	return strings.NewReplacer("%", "%25", "&", "%26", "#", "%23", "+", "%2B", " ", "%20", "?", "%3F").Replace(s)
}

func (a *App) serveItemThumb(w http.ResponseWriter, r *http.Request, id, name string) {
	p, err := a.Library.LocalPath(id, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	b, err := a.previewPNG(p)
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	writePNG(w, b)
}

// readLibraryUpload reads a multipart form: "file" parts (models), "photo"
// parts (images) and metadata fields.
func (a *App) readLibraryUpload(w http.ResponseWriter, r *http.Request) (files, photos []library.Incoming, meta library.Meta, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, nil, meta, err
	}
	fields := map[string]string{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			removeIncoming(files, photos)
			return nil, nil, meta, err
		}
		if part.FileName() == "" {
			b, _ := io.ReadAll(io.LimitReader(part, 64<<10))
			fields[part.FormName()] = string(b)
			continue
		}
		tmp, err := os.CreateTemp(a.Store.Dir(), "lib-*")
		if err != nil {
			removeIncoming(files, photos)
			return nil, nil, meta, err
		}
		_, err = io.Copy(tmp, part)
		tmp.Close()
		inc := library.Incoming{Name: part.FileName(), Tmp: tmp.Name()}
		// Images dropped in with models become photos.
		if part.FormName() == "photo" || library.IsImage(part.FileName()) {
			photos = append(photos, inc)
		} else {
			files = append(files, inc)
		}
		if err != nil {
			removeIncoming(files, photos)
			return nil, nil, meta, err
		}
	}
	str := func(k string) *string {
		if v, ok := fields[k]; ok {
			return &v
		}
		return nil
	}
	meta = library.Meta{Title: str("title"), Description: str("description"), Category: str("category"), Designer: str("designer"),
		SourceURL: str("source_url"), License: str("license"), Notes: str("notes")}
	if t, ok := fields["tags"]; ok {
		tags := strings.Split(t, ",")
		meta.Tags = &tags
	}
	return files, photos, meta, nil
}

func removeIncoming(lists ...[]library.Incoming) {
	for _, l := range lists {
		for _, f := range l {
			os.Remove(f.Tmp)
		}
	}
}

// copyToTemp copies a file so the project can take ownership of the copy.
func (a *App) copyToTemp(src string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.CreateTemp(a.Store.Dir(), "copy-*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, in)
	if e := out.Close(); err == nil {
		err = e
	}
	if err != nil {
		os.Remove(out.Name()) // don't leave a half copy in the data folder
		return "", err
	}
	return out.Name(), nil
}

// PrintFromLibrary turns library files into a project (or adds them to a
// plate of an existing STL project), ready to send to a printer.
func (a *App) PrintFromLibrary(id string, names []string, projectID string, plate int, as3MF bool) (*Project, error) {
	it, ok := a.Library.Get(id)
	if !ok {
		return nil, fmt.Errorf("item not found")
	}
	if len(names) == 0 {
		for _, f := range it.Files {
			if library.Printable(f.Name) && f.Kind != "3mf" {
				names = append(names, f.Name)
			}
		}
		if len(names) == 0 {
			for _, f := range it.Files {
				if f.Kind == "3mf" {
					names = []string{f.Name}
					break
				}
			}
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("this item has no printable files (STL, OBJ or 3MF)")
	}
	var ups []Upload
	for _, n := range names {
		src, upName := "", filepath.Base(n)
		if strings.EqualFold(filepath.Ext(n), ".3dj") {
			// A 3D James project prints from its printable copy (STL for
			// plates, 3MF when it opens as its own project).
			format := "stl"
			if projectID == "" && len(names) == 1 && as3MF {
				format = "3mf"
			}
			p, err := a.printableCopy(id, n, format)
			if err != nil {
				removeUploads(ups)
				return nil, err
			}
			src, upName = p, strings.TrimSuffix(upName, filepath.Ext(upName))+"."+format
			defer os.Remove(p) // used once: the project keeps its own copy
		} else {
			if !library.Printable(n) {
				removeUploads(ups)
				return nil, fmt.Errorf("%s can't be sliced (use STL, OBJ or 3MF)", n)
			}
			var err error
			if src, err = a.Library.LocalPath(id, n); err != nil {
				removeUploads(ups)
				return nil, err
			}
		}
		tmp, err := a.copyToTemp(src)
		if err != nil {
			removeUploads(ups)
			return nil, err
		}
		ups = append(ups, Upload{Name: upName, Tmp: tmp})
	}
	var p *Project
	var err error
	if projectID != "" {
		p, err = a.AddMeshFiles(projectID, plate, ups)
	} else {
		p, err = a.AddProject(ups)
		if err == nil {
			p, err = a.RenameProject(p.ID, it.Title)
		}
	}
	if err != nil {
		removeUploads(ups)
		return nil, err
	}
	a.Library.MarkPrinted(id)
	return p, nil
}

// ProjectToLibrary saves a project's model files as a new library item.
func (a *App) ProjectToLibrary(id string, m library.Meta) (library.Item, error) {
	p, ok := a.Project(id)
	if !ok {
		return library.Item{}, fmt.Errorf("project not found")
	}
	var files []library.Incoming
	if p.Slicer != nil {
		tmp, err := a.copyToTemp(a.slicerFile(p, "project.3mf"))
		if err != nil {
			return library.Item{}, err
		}
		files = append(files, library.Incoming{Name: p.Name + ".3mf", Tmp: tmp})
	} else if p.Kind == "mesh" {
		for _, mp := range p.MeshParts {
			tmp, err := a.copyToTemp(filepath.Join(a.projectDir(p.ID), mp.File))
			if err != nil {
				removeIncoming(files)
				return library.Item{}, err
			}
			files = append(files, library.Incoming{Name: mp.Name + filepath.Ext(mp.File), Tmp: tmp})
		}
	} else {
		tmp, err := a.copyToTemp(a.projectFile(p.ID))
		if err != nil {
			return library.Item{}, err
		}
		files = append(files, library.Incoming{Name: p.Name + ".3mf", Tmp: tmp})
	}
	if len(files) == 0 {
		return library.Item{}, fmt.Errorf("the project has no files yet")
	}
	if m.Title == nil {
		t := p.Name
		m.Title = &t
	}
	return a.Library.Create(m, files, nil)
}
