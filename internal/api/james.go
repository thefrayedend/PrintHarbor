package api

import (
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 3D James (the CAD app in web/3djames.html) runs inside PrintHarbor. Its
// built-in "server" mode talks to these addresses: /api/me lists the folders
// it may open and save in, /api/list, /api/file and /api/mkdir work on them.
// Here those folders are the library's:
//   - "My designs": <models folder>\My designs. Saving Bracket.3dj there
//     stores it as My designs\Bracket\Bracket.3dj, so each design is its own
//     model in the library (its STL/3MF exports land next to it).
//   - "Models": the whole models folder (downloads are designer\model).
//   - your other library folders, read-only.
// Whatever is saved shows up in the library right away.

type jamesFolder struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Write bool   `json:"write"`
	root  string
}

const designsName = "My designs"

// maxJamesFile is the biggest file 3D James may save or hand over.
const maxJamesFile = 512 << 20

func (a *App) jamesFolders() []jamesFolder {
	md := a.cfg().ModelsDir()
	out := []jamesFolder{
		{ID: "designs", Label: designsName, Write: true, root: filepath.Join(md, designsName)},
		{ID: "models", Label: "Models", Write: true, root: md},
	}
	for i, f := range a.cfg().LibraryFolders {
		if abs, err := filepath.Abs(f); err == nil && sameFolder(abs, md) {
			continue
		}
		out = append(out, jamesFolder{ID: "lib" + strconv.Itoa(i), Label: filepath.Base(filepath.Clean(f)) + " (read-only)", root: f})
	}
	return out
}

func (a *App) jamesFolder(id string) (jamesFolder, bool) {
	for _, f := range a.jamesFolders() {
		if f.ID == id {
			return f, true
		}
	}
	return jamesFolder{}, false
}

// jamesPath maps a folder + path from 3D James to a file on disk.
func (a *App) jamesPath(folder, p string) (jamesFolder, string, error) {
	f, ok := a.jamesFolder(folder)
	if !ok {
		return f, "", fmt.Errorf("unknown folder")
	}
	p = strings.Trim(filepath.ToSlash(p), "/")
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return f, "", fmt.Errorf("bad path")
		}
	}
	// A file saved straight into "My designs" gets its own model folder.
	if f.ID == "designs" && p != "" && !strings.Contains(p, "/") && filepath.Ext(p) != "" {
		base := strings.TrimSuffix(p, filepath.Ext(p))
		p = base + "/" + p
	}
	full := filepath.Join(f.root, filepath.FromSlash(p))
	if rel, err := filepath.Rel(f.root, full); err != nil || strings.HasPrefix(rel, "..") {
		return f, "", fmt.Errorf("bad path")
	}
	return f, full, nil
}

// jamesLoc is the reverse: where a file on disk is for 3D James.
func (a *App) jamesLoc(full string) (folder, p string, ok bool) {
	for _, f := range a.jamesFolders() {
		rel, err := filepath.Rel(f.root, full)
		if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
			continue
		}
		rel = filepath.ToSlash(rel)
		if f.ID == "designs" {
			if parts := strings.Split(rel, "/"); len(parts) == 2 && strings.TrimSuffix(parts[1], filepath.Ext(parts[1])) == parts[0] {
				rel = parts[1]
			}
		}
		return f.ID, rel, true
	}
	return "", "", false
}

func jamesETag(fi os.FileInfo) string {
	h := sha1.Sum([]byte(fi.ModTime().UTC().Format(time.RFC3339Nano) + "/" + strconv.FormatInt(fi.Size(), 10)))
	return `"` + hex.EncodeToString(h[:8]) + `"`
}

var jamesRescan struct {
	sync.Mutex
	t *time.Timer
}

// rescanSoon picks up saved designs in the library (once things go quiet).
func (a *App) rescanSoon() {
	jamesRescan.Lock()
	defer jamesRescan.Unlock()
	if jamesRescan.t != nil {
		jamesRescan.t.Stop()
	}
	jamesRescan.t = time.AfterFunc(1500*time.Millisecond, func() {
		a.Library.SetFolders(a.cfg().ScanFolders())
		a.Library.Rescan()
	})
}

func jamesFail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"message": msg})
}

func (a *App) jamesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		_ = os.MkdirAll(filepath.Join(a.cfg().ModelsDir(), designsName), 0o755)
		// The hidden copy that makes printable files keeps its own storage, so
		// it never touches the session you have open in the 3D James tab.
		user := "PrintHarbor"
		if strings.Contains(r.Referer(), "convert=1") {
			user = "PrintHarbor-convert"
		}
		writeJSON(w, 200, map[string]any{"user": user, "folders": a.jamesFolders()})
	})
	mux.HandleFunc("GET /api/list", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f, dir, err := a.jamesPath(q.Get("folder"), q.Get("path"))
		if err != nil {
			jamesFail(w, 400, err.Error())
			return
		}
		ents, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			jamesFail(w, 400, err.Error())
			return
		}
		type entry struct {
			Name  string `json:"name"`
			Dir   bool   `json:"dir"`
			Size  int64  `json:"size"`
			MTime int64  `json:"mtime,omitempty"`
		}
		out := []entry{}
		add := func(name string, fi os.FileInfo, isDir bool) {
			e := entry{Name: name, Dir: isDir}
			if fi != nil {
				e.MTime = fi.ModTime().UnixMilli()
				if !isDir {
					e.Size = fi.Size()
				}
			}
			out = append(out, e)
		}
		root := f.ID == "designs" && strings.Trim(q.Get("path"), "/") == ""
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") || strings.HasSuffix(e.Name(), ".part") {
				continue
			}
			fi, _ := e.Info()
			if e.IsDir() && root {
				// Each design's own files show at the top level.
				sub, _ := os.ReadDir(filepath.Join(dir, e.Name()))
				for _, s := range sub {
					if !s.IsDir() && strings.TrimSuffix(s.Name(), filepath.Ext(s.Name())) == e.Name() {
						si, _ := s.Info()
						add(s.Name(), si, false)
					}
				}
			}
			add(e.Name(), fi, e.IsDir())
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		writeJSON(w, 200, map[string]any{"entries": out})
	})
	mux.HandleFunc("GET /api/file", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		_, full, err := a.jamesPath(q.Get("folder"), q.Get("path"))
		if err != nil {
			jamesFail(w, 400, err.Error())
			return
		}
		fh, err := os.Open(full)
		if err != nil {
			jamesFail(w, 404, "That file isn't there any more.")
			return
		}
		defer fh.Close()
		fi, _ := fh.Stat()
		w.Header().Set("ETag", jamesETag(fi))
		w.Header().Set("X-Modified", strconv.FormatInt(fi.ModTime().UnixMilli(), 10))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.Copy(w, fh)
	})
	mux.HandleFunc("PUT /api/file", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f, full, err := a.jamesPath(q.Get("folder"), q.Get("path"))
		if err != nil {
			jamesFail(w, 400, err.Error())
			return
		}
		if !f.Write {
			jamesFail(w, 403, fmt.Sprintf("%q is read-only. Save in My designs or Models.", f.Label))
			return
		}
		match := r.Header.Get("If-Match")
		if fi, err := os.Stat(full); err == nil {
			if match == "new" || (match != "any" && match != "" && match != jamesETag(fi)) {
				writeJSON(w, 409, map[string]any{"message": "changed", "by": "another program", "mtime": fi.ModTime().UnixMilli()})
				return
			}
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			jamesFail(w, 500, err.Error())
			return
		}
		tmp := full + ".part"
		out, err := os.Create(tmp)
		if err != nil {
			jamesFail(w, 500, err.Error())
			return
		}
		n, err := io.Copy(out, io.LimitReader(r.Body, maxJamesFile+1))
		if e := out.Close(); err == nil {
			err = e
		}
		if err == nil && n > maxJamesFile {
			// Never replace a good design with the first part of a too-big one.
			err = fmt.Errorf("that file is bigger than %d MB", maxJamesFile>>20)
		}
		if err == nil && r.ContentLength > 0 && n != r.ContentLength {
			err = fmt.Errorf("the save was cut off; nothing was changed")
		}
		if err != nil {
			os.Remove(tmp)
			jamesFail(w, 500, err.Error())
			return
		}
		if err := replaceFile(tmp, full); err != nil {
			os.Remove(tmp)
			jamesFail(w, 500, err.Error())
			return
		}
		fi, _ := os.Stat(full)
		a.rescanSoon()
		writeJSON(w, 200, map[string]string{"etag": jamesETag(fi)})
	})
	mux.HandleFunc("POST /api/mkdir", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Folder, Path string }
		if err := readJSON(r, &in); err != nil {
			jamesFail(w, 400, err.Error())
			return
		}
		f, full, err := a.jamesPath(in.Folder, in.Path)
		if err != nil || !f.Write {
			jamesFail(w, 403, "Can't make a folder there.")
			return
		}
		if err := os.MkdirAll(full, 0o755); err != nil {
			jamesFail(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/delete", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Folder, Path string }
		if err := readJSON(r, &in); err != nil {
			jamesFail(w, 400, err.Error())
			return
		}
		f, full, err := a.jamesPath(in.Folder, in.Path)
		if err != nil || !f.Write {
			jamesFail(w, 403, "Can't delete there.")
			return
		}
		if fi, err := os.Stat(full); err != nil || fi.IsDir() {
			jamesFail(w, 400, "Only files can be deleted.")
			return
		}
		if err := os.Remove(full); err != nil {
			jamesFail(w, 500, err.Error())
			return
		}
		a.rescanSoon()
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/presence", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"others": []string{}})
	})
	mux.HandleFunc("GET /logout", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	})
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	})
	// Where a library file is, for "Open in 3D James".
	mux.HandleFunc("GET /api/library/{id}/james", func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("id"), r.URL.Query().Get("name")
		out := map[string]string{"url": "/api/library/" + id + "/raw/file?name=" + url.QueryEscape(name), "name": filepath.Base(name)}
		if full, err := a.Library.LocalPath(id, name); err == nil {
			if _, err := os.Stat(full); err == nil {
				if folder, p, ok := a.jamesLoc(full); ok {
					out["folder"], out["path"] = folder, p
				}
			}
		}
		writeJSON(w, 200, out)
	})
}

//go:embed 3djames-bridge.js
var jamesBridge string

var jamesPage struct {
	sync.Mutex
	path string
	mod  time.Time
	etag string
	gz   []byte // the page, gzip-compressed (it is 26 MB as it is)
}

// serve3DJames sends 3D James with PrintHarbor's bridge added. A newer
// 3DJames.html dropped next to the program (3DJames.html in the program folder) is
// used instead of the built-in copy, so updating 3D James is just replacing
// that file. The page is sent compressed and with a fingerprint, so the
// browser only downloads it again when it has actually changed.
func (a *App) serve3DJames(web fs.FS) http.HandlerFunc {
	go keep3DJames(web, filepath.Join(a.Store.Dir(), "3djames.html"))
	return func(w http.ResponseWriter, r *http.Request) {
		var src []byte
		var key string
		var mod time.Time
		if ents, err := os.ReadDir("."); err == nil {
			for _, e := range ents {
				if !e.IsDir() && strings.EqualFold(e.Name(), "3djames.html") {
					if fi, err := e.Info(); err == nil {
						key, mod = e.Name(), fi.ModTime()
					}
				}
			}
		}
		jamesPage.Lock()
		if jamesPage.gz == nil || jamesPage.path != key || !jamesPage.mod.Equal(mod) {
			if key != "" {
				src, _ = os.ReadFile(key)
			}
			if len(src) == 0 && web != nil {
				key, mod = "", time.Time{}
				src, _ = fs.ReadFile(web, "3djames.html")
			}
			// A build without 3D James inside (the published ones): use the
			// copy an earlier build left in the data folder (see keep3DJames).
			if len(src) == 0 {
				key, mod = "", time.Time{}
				src, _ = os.ReadFile(filepath.Join(a.Store.Dir(), "3djames.html"))
			}
			if len(src) == 0 {
				jamesPage.Unlock()
				http.Error(w, "3D James isn't installed (put 3DJames.html next to PrintHarbor)", 404)
				return
			}
			html := string(src)
			if !strings.Contains(html, "PrintHarbor bridge for 3D James") {
				tag := "<script>\n" + jamesBridge + "</script>\n"
				if i := strings.LastIndex(strings.ToLower(html), "</body>"); i >= 0 {
					html = html[:i] + tag + html[i:]
				} else {
					html += tag
				}
			}
			page := []byte(html)
			jamesPage.path, jamesPage.mod, jamesPage.etag, jamesPage.gz = key, mod, etagOf(page), gzipBytes(page)
		}
		etag, gz := jamesPage.etag, jamesPage.gz
		jamesPage.Unlock()
		writeCompressed(w, r, "text/html; charset=utf-8", etag, gz)
	}
}

// Printing 3D James projects. A .3dj is a recipe (shapes, cuts, fillets...),
// not a shape: building it takes 3D James's geometry engine (OpenCascade).
// So when you print one, 3D James builds the STL/3MF on the fly and hands it
// over; PrintHarbor puts it straight into the project (which keeps its own
// copy, as with any file) and nothing extra is stored in your library.

var errNeedPrintable = errors.New("this 3D James project needs to be built by 3D James first")

// printableTemp is where a freshly built file waits until the print picks it up.
func (a *App) printableTemp(id, name, format string) string {
	h := sha1.Sum([]byte(id + "\x00" + name))
	return filepath.Join(os.TempDir(), "printharbor-3dj", hex.EncodeToString(h[:8])+"."+format)
}

// printableCopy returns the file 3D James just built for a library .3dj.
func (a *App) printableCopy(id, name, format string) (string, error) {
	src, err := a.Library.LocalPath(id, name)
	if err != nil {
		return "", err
	}
	si, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	p := a.printableTemp(id, name, format)
	if pi, err := os.Stat(p); err == nil && pi.Size() > 0 && !pi.ModTime().Before(si.ModTime()) && time.Since(pi.ModTime()) < 10*time.Minute {
		return p, nil
	}
	return "", errNeedPrintable
}

func (a *App) printableRoutes(mux *http.ServeMux) {
	// 3D James (running hidden in the library page) hands the built file over here.
	mux.HandleFunc("POST /api/library/{id}/printable", func(w http.ResponseWriter, r *http.Request) {
		id, name, format := r.PathValue("id"), r.URL.Query().Get("name"), r.URL.Query().Get("format")
		if format != "stl" && format != "3mf" {
			fail(w, 400, fmt.Errorf("format must be stl or 3mf"))
			return
		}
		if !strings.EqualFold(filepath.Ext(name), ".3dj") {
			fail(w, 400, fmt.Errorf("not a 3D James project"))
			return
		}
		if _, err := a.Library.LocalPath(id, name); err != nil {
			fail(w, 404, err)
			return
		}
		p := a.printableTemp(id, name, format)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		out, err := os.Create(p + ".part")
		if err != nil {
			fail(w, 500, err)
			return
		}
		n, err := io.Copy(out, io.LimitReader(r.Body, maxJamesFile+1))
		if e := out.Close(); err == nil {
			err = e
		}
		if err == nil && n > maxJamesFile {
			err = fmt.Errorf("that model is bigger than %d MB", maxJamesFile>>20)
		}
		if err == nil {
			err = replaceFile(p+".part", p)
		}
		if err != nil {
			os.Remove(p + ".part")
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
}

// keep3DJames copies the 3D James page built into this program to the data
// folder, so a later build that comes without it (the published builds do)
// still has it. It does nothing when this build has no page inside or the
// copy is already the same.
func keep3DJames(web fs.FS, dst string) {
	if web == nil {
		return
	}
	src, err := fs.ReadFile(web, "3djames.html")
	if err != nil || len(src) == 0 {
		return
	}
	if fi, err := os.Stat(dst); err == nil && fi.Size() == int64(len(src)) {
		return
	}
	tmp := dst + ".tmp"
	if os.WriteFile(tmp, src, 0o644) == nil {
		_ = os.Rename(tmp, dst)
	}
}
