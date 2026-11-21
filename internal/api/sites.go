package api

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thefrayedend/printharbor/internal/library"
	"github.com/thefrayedend/printharbor/internal/sites"
)

// Online model sites: browse/search them on their own (never mixed with your
// library) and import a model's files into
// ModelDir/<designer>/<model>/<files>, with its details in printharbor.json.

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// testSites replaces the real sites in tests.
var testSites []sites.Site

func (a *App) pluginKeys() map[string]string {
	c := a.cfg()
	return map[string]string{
		"thingiverse_token": c.ThingiverseToken,
		"makerworld_token":  c.MakerWorldToken,
		"cults_user":        c.CultsUser,
		"cults_key":         c.CultsKey,
	}
}

func (a *App) siteList() []sites.Site {
	if testSites != nil {
		return testSites
	}
	c := a.cfg()
	built := sites.All(sites.Keys{ThingiverseToken: c.ThingiverseToken, MakerWorldToken: c.MakerWorldToken, CultsUser: c.CultsUser, CultsKey: c.CultsKey})
	// A plugin with the same id as a built-in site replaces it, so model sites
	// can be moved to plugins one at a time without losing the others.
	if a.Plugins == nil {
		return built
	}
	byID := map[string]sites.Site{}
	order := []string{}
	for _, s := range built {
		id := s.Info().ID
		byID[id] = s
		order = append(order, id)
	}
	for _, s := range a.Plugins.Sites() {
		id := s.Info().ID
		if _, ok := byID[id]; !ok {
			order = append(order, id)
		}
		byID[id] = s
	}
	// A site the user turned off is hidden entirely (no built-in fallback).
	off := a.Plugins.OffIDs()
	out := make([]sites.Site, 0, len(order))
	for _, id := range order {
		if off[id] {
			continue
		}
		out = append(out, byID[id])
	}
	return out
}

func (a *App) site(id string) (sites.Site, error) {
	for _, s := range a.siteList() {
		if s.Info().ID == id {
			return s, nil
		}
	}
	return nil, fmt.Errorf("unknown site %q", id)
}

// ImportJob is a running or finished import.
type ImportJob struct {
	ID       string    `json:"id"`
	Site     string    `json:"site"`
	ModelID  string    `json:"model_id"`
	Title    string    `json:"title"`
	State    string    `json:"state"` // running | done | error
	Step     string    `json:"step"`
	Done     int64     `json:"done"`
	Total    int64     `json:"total"`
	Error    string    `json:"error,omitempty"`
	ItemID   string    `json:"item_id,omitempty"`
	Folder   string    `json:"folder,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitempty"`
}

type importState struct {
	mu   sync.Mutex
	jobs map[string]*ImportJob
	seq  int
}

var imports = &importState{jobs: map[string]*ImportJob{}}

func (s *importState) snapshot() []ImportJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ImportJob{}
	for _, j := range s.jobs {
		if !j.Finished.IsZero() && time.Since(j.Finished) > 10*time.Minute {
			delete(s.jobs, j.ID)
			continue
		}
		out = append(out, *j)
	}
	return out
}

// add registers a new job and returns it with a copy to send to the browser:
// the job itself keeps changing on another goroutine, so it must not be
// encoded as JSON outside the lock.
func (s *importState) add(j *ImportJob) (*ImportJob, ImportJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	j.ID = "imp" + strconv.Itoa(s.seq)
	s.jobs[j.ID] = j
	return j, *j
}

func (s *importState) update(j *ImportJob, fn func(j *ImportJob)) {
	s.mu.Lock()
	fn(j)
	s.mu.Unlock()
}

var reBadName = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]+`)

// safeName makes a Windows-safe folder or file name.
func safeName(s, fallback string) string {
	s = reBadName.ReplaceAllString(strings.TrimSpace(s), " ")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRight(s, ". ")
	if len([]rune(s)) > 80 {
		s = strings.TrimRight(string([]rune(s)[:80]), ". ")
	}
	switch strings.ToUpper(strings.SplitN(s, ".", 2)[0]) {
	case "", "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "LPT1", "LPT2", "LPT3":
		s = ""
	}
	if s == "" {
		return fallback
	}
	return s
}

// modelFolder is ModelDir/<designer>/<model>.
func (a *App) modelFolder(m sites.Model) string {
	return filepath.Join(a.cfg().ModelsDir(), safeName(m.Author, "Unknown designer"), safeName(m.Title, m.Site+" "+m.ID))
}

// libraryBySource maps source URLs (and site:id) to library item ids.
func (a *App) libraryBySource() map[string]string {
	items, _ := a.Library.Search(library.Query{Limit: 1 << 30})
	out := map[string]string{}
	for _, it := range items {
		if it.SourceURL == "" {
			continue
		}
		out[normURL(it.SourceURL)] = it.ID
		if s, id, ok := sites.ParseLink(it.SourceURL); ok {
			out[s+":"+id] = it.ID
		}
	}
	return out
}

func normURL(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	return strings.TrimSuffix(strings.TrimPrefix(u, "www."), "/")
}

func (a *App) inLibrary(idx map[string]string, c sites.Card) string {
	if id := idx[c.Site+":"+c.ID]; id != "" {
		return id
	}
	return idx[normURL(c.URL)]
}

type cardOut struct {
	sites.Card
	InLibrary string `json:"in_library,omitempty"`
}

func (a *App) sitesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sites", func(w http.ResponseWriter, r *http.Request) {
		infos := []sites.Info{}
		for _, s := range a.siteList() {
			infos = append(infos, s.Info())
		}
		writeJSON(w, 200, map[string]any{"sites": infos, "model_dir": a.cfg().ModelsDir(), "imports": imports.snapshot()})
	})
	mux.HandleFunc("GET /api/sites/link", func(w http.ResponseWriter, r *http.Request) {
		url := r.URL.Query().Get("url")
		s, id, ok := sites.ParseLink(url)
		if !ok && a.Plugins != nil { // plugins can recognise their own sites' links too
			s, id, ok = a.Plugins.Match(url)
		}
		if !ok {
			fail(w, 400, fmt.Errorf("that isn't a model link PrintHarbor recognises"))
			return
		}
		writeJSON(w, 200, map[string]string{"site": s, "id": id})
	})
	mux.HandleFunc("GET /api/sites/{site}/search", func(w http.ResponseWriter, r *http.Request) {
		s, err := a.site(r.PathValue("site"))
		if err != nil {
			fail(w, 404, err)
			return
		}
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		pg, err := s.Search(ctx, sites.Query{Text: q.Get("q"), Sort: q.Get("sort"), Page: page, Cursor: q.Get("cursor")})
		if err != nil {
			fail(w, 502, err)
			return
		}
		idx := a.libraryBySource()
		items := []cardOut{}
		for _, c := range pg.Items {
			items = append(items, cardOut{c, a.inLibrary(idx, c)})
		}
		writeJSON(w, 200, map[string]any{"items": items, "more": pg.More, "cursor": pg.Cursor})
	})
	mux.HandleFunc("GET /api/sites/{site}/models/{id}", func(w http.ResponseWriter, r *http.Request) {
		s, err := a.site(r.PathValue("site"))
		if err != nil {
			fail(w, 404, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		m, err := s.Model(ctx, r.PathValue("id"))
		if err != nil {
			fail(w, 502, err)
			return
		}
		writeJSON(w, 200, map[string]any{"model": m, "in_library": a.inLibrary(a.libraryBySource(), m.Card),
			"folder": a.modelFolder(m), "can_download": s.Info().CanDownload})
	})
	mux.HandleFunc("GET /api/sites/printables/schema", func(w http.ResponseWriter, r *http.Request) {
		out, err := (&sites.Printables{}).Schema(r.Context())
		if err != nil {
			fail(w, 502, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/sites/printables/probe", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		out, err := (&sites.Printables{}).Probe(r.Context(), in.Query, in.Variables)
		if err != nil {
			fail(w, 502, err)
			return
		}
		writeJSON(w, 200, out)
	})
	// Image proxy: site pictures load through PrintHarbor (no hotlink blocks,
	// and your browser only talks to PrintHarbor).
	mux.HandleFunc("GET /api/sites/image", func(w http.ResponseWriter, r *http.Request) {
		u := r.URL.Query().Get("u")
		if !allowedImageHost(u) {
			fail(w, 400, fmt.Errorf("not a model site picture"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		// Smaller copy first; the original if the site can't make one.
		var rc io.ReadCloser
		var ctype string
		for _, try := range []string{sizedImage(u, r.URL.Query().Get("s")), u} {
			req, err := http.NewRequestWithContext(ctx, "GET", try, nil)
			if err != nil {
				continue
			}
			req.Header.Set("User-Agent", "PrintHarbor")
			resp, err := imageClient.Do(req)
			if err != nil {
				continue
			}
			// Pictures only. Anything else (a web page, an SVG with script in it)
			// would otherwise be shown as if it were part of PrintHarbor itself.
			ct := proxiedImageType(resp.Header.Get("Content-Type"))
			if resp.StatusCode != 200 || ct == "" {
				resp.Body.Close()
				continue
			}
			rc, ctype = resp.Body, ct
			break
		}
		if rc == nil {
			fail(w, 502, fmt.Errorf("picture not available"))
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = io.Copy(w, io.LimitReader(rc, 20<<20))
	})
	mux.HandleFunc("GET /api/sites/imports", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, imports.snapshot())
	})
	// Import: JSON {files:[ids]} downloads from the site; multipart "file"
	// fields file what you downloaded yourself (Cults3D) with the details.
	mux.HandleFunc("POST /api/sites/{site}/models/{id}/import", func(w http.ResponseWriter, r *http.Request) {
		s, err := a.site(r.PathValue("site"))
		if err != nil {
			fail(w, 404, err)
			return
		}
		var want []string
		var uploads []string
		var tmp string
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			if err := r.ParseMultipartForm(64 << 20); err != nil {
				fail(w, 400, err)
				return
			}
			tmp, _ = os.MkdirTemp("", "ph-import-")
			for _, fh := range r.MultipartForm.File["file"] {
				src, err := fh.Open()
				if err != nil {
					continue
				}
				dst := filepath.Join(tmp, safeName(filepath.Base(fh.Filename), "file"))
				f, err := os.Create(dst)
				if err == nil {
					_, _ = io.Copy(f, src)
					f.Close()
					uploads = append(uploads, dst)
				}
				src.Close()
			}
			if len(uploads) == 0 {
				os.RemoveAll(tmp)
				fail(w, 400, fmt.Errorf("no files"))
				return
			}
		} else {
			var in struct {
				Files []string `json:"files"`
			}
			if err := readJSON(r, &in); err != nil {
				fail(w, 400, err)
				return
			}
			want = in.Files
		}
		j, started := imports.add(&ImportJob{Site: s.Info().ID, ModelID: r.PathValue("id"), State: "running", Step: "Reading the model…", Started: time.Now()})
		go func() {
			if tmp != "" {
				defer os.RemoveAll(tmp)
			}
			err := a.runImport(s, j, want, uploads)
			imports.update(j, func(j *ImportJob) {
				j.Finished = time.Now()
				if err != nil {
					j.State, j.Error = "error", err.Error()
				} else {
					j.State, j.Step = "done", "Imported"
				}
			})
		}()
		writeJSON(w, 202, started)
	})
}

// sizedImage asks the site for a smaller picture where it can make one
// (full photos are often 5000 px / several MB). Imports keep the originals.
func sizedImage(u, size string) string {
	if size == "" || !strings.HasPrefix(u, "https://media.printables.com/") || strings.Contains(u, "/thumbs/") {
		return u
	}
	i := strings.LastIndex(u, "/")
	name := u[i+1:]
	if j := strings.LastIndex(name, "."); j > 0 {
		name = name[:j]
	}
	if size == "card" {
		return u[:i+1] + "thumbs/cover/640x480/jpg/" + name + ".webp"
	}
	return u[:i+1] + "thumbs/inside/1280x960/jpg/" + name + ".webp"
}

// imageClient fetches model-site pictures: public https hosts only, also
// after redirects.
var imageClient = publicClient(30 * time.Second)

// proxiedImageType returns the picture type to pass on to the browser, or ""
// when the content isn't a (safe) picture.
func proxiedImageType(header string) string {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(header, ";", 2)[0]))
	switch ct {
	case "image/jpeg", "image/jpg", "image/png", "image/webp", "image/gif", "image/avif", "image/bmp":
		return ct
	case "application/octet-stream", "binary/octet-stream", "":
		// Some CDNs don't label pictures; the browser is told not to guess
		// (nosniff), so label it as a picture: non-pictures just won't show.
		return "image/jpeg"
	}
	return ""
}

func allowedImageHost(u string) bool {
	if !strings.HasPrefix(u, "https://") {
		return false
	}
	host := strings.SplitN(strings.TrimPrefix(u, "https://"), "/", 2)[0]
	for _, h := range []string{"printables.com", "thingiverse.com", "makerworld.com", "bambulab.com", "bblmw.com", "bambulab.cn", "cults3d.com", "cdn.thingiverse.com", "amazonaws.com", "cloudfront.net"} {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// countingReader reports progress.
type countingReader struct {
	r  io.Reader
	fn func(n int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.fn(int64(n))
	}
	return n, err
}

func (a *App) runImport(s sites.Site, j *ImportJob, want, uploads []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	{ // stop with PrintHarbor
		run := a.runCtxOr()
		var stop context.CancelFunc
		ctx, stop = context.WithCancel(ctx)
		defer stop()
		go func() {
			select {
			case <-run.Done():
				stop()
			case <-ctx.Done():
			}
		}()
	}
	m, err := s.Model(ctx, j.ModelID)
	if err != nil {
		return err
	}
	dir := a.modelFolder(m)
	imports.update(j, func(j *ImportJob) { j.Title, j.Folder = m.Title, dir })
	pick := map[string]bool{}
	for _, id := range want {
		pick[id] = true
	}
	var files []sites.File
	for _, f := range m.Files {
		if len(pick) == 0 && f.Kind != "gcode" || pick[f.ID] {
			files = append(files, f)
		}
	}
	if len(uploads) == 0 && len(files) == 0 {
		if !s.Info().CanDownload {
			return fmt.Errorf("%s", firstNonEmptyStr(m.DownloadNote, "this site doesn't allow downloads"))
		}
		return fmt.Errorf("no files chosen")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("can't create %s: %w", dir, err)
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	imports.update(j, func(j *ImportJob) { j.Total = total })
	used := map[string]bool{}
	uniq := func(name string) string {
		base, ext := strings.TrimSuffix(name, filepath.Ext(name)), filepath.Ext(name)
		n := name
		for i := 2; used[strings.ToLower(n)]; i++ {
			n = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		used[strings.ToLower(n)] = true
		return n
	}
	place := func(name string, r io.Reader) error {
		name = uniq(safeName(name, "file"))
		dst := filepath.Join(dir, name)
		tmp := dst + ".part"
		f, err := os.Create(tmp)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, r)
		if e := f.Close(); err == nil {
			err = e // a full disk often only shows up here
		}
		if err != nil {
			os.Remove(tmp)
			return err
		}
		if err := replaceFile(tmp, dst); err != nil {
			os.Remove(tmp)
			return err
		}
		if strings.EqualFold(filepath.Ext(dst), ".zip") {
			// Only drop the zip when everything in it came out; otherwise keep it.
			if err := unzipInto(dst, dir); err == nil {
				os.Remove(dst)
			} else {
				log.Printf("import: kept %s (%v)", dst, err)
			}
		}
		return nil
	}
	for i, f := range files {
		imports.update(j, func(j *ImportJob) { j.Step = fmt.Sprintf("Downloading %s (%d of %d)", f.Name, i+1, len(files)) })
		rc, err := s.Download(ctx, m, f)
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
		err = place(f.Name, &countingReader{rc, func(n int64) { imports.update(j, func(j *ImportJob) { j.Done += n }) }})
		rc.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	for _, p := range uploads {
		imports.update(j, func(j *ImportJob) { j.Step = "Filing " + filepath.Base(p) })
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		err = place(filepath.Base(p), f)
		f.Close()
		if err != nil {
			return err
		}
	}
	info := s.Info()
	return a.saveDetails(ctx, j, info.ID, info.Name, m, dir, true)
}

// saveDetails stores a model's pictures and printharbor.json in its folder
// and adds it to the library. With overwrite false, existing details stay.
func (a *App) saveDetails(ctx context.Context, j *ImportJob, siteID, siteName string, m sites.Model, dir string, overwrite bool) error {
	imgDir := filepath.Join(dir, "images")
	if _, err := os.Stat(imgDir); overwrite || err != nil {
		// Pictures go in images/ (the library shows them as the model's photos).
		imports.update(j, func(j *ImportJob) { j.Step = "Saving pictures" })
		for i, u := range m.Images {
			if i >= 12 {
				break
			}
			rc, err := sitesFetchImage(ctx, u)
			if err != nil {
				continue
			}
			ext := strings.ToLower(path.Ext(strings.SplitN(u, "?", 2)[0]))
			if ext != ".png" && ext != ".webp" && ext != ".gif" {
				ext = ".jpg"
			}
			_ = os.MkdirAll(imgDir, 0o755)
			if f, err := os.Create(filepath.Join(imgDir, fmt.Sprintf("%02d%s", i+1, ext))); err == nil {
				_, _ = io.Copy(f, io.LimitReader(rc, 30<<20))
				f.Close()
			}
			rc.Close()
		}
	}
	side := filepath.Join(dir, library.SidecarName)
	if _, err := os.Stat(side); overwrite || err != nil {
		cat := m.Category
		if cat == "" {
			cat = siteName
		}
		sc := library.Sidecar{Title: m.Title, Description: m.Description, Designer: m.Author, Tags: m.Tags, License: m.License,
			SourceURL: m.URL, Category: cat, Site: siteID, SiteID: m.ID, Imported: time.Now().Format(time.RFC3339)}
		b, _ := json.MarshalIndent(sc, "", "  ")
		if err := os.WriteFile(side, b, 0o644); err != nil {
			return err
		}
	}
	// Make sure the models folder is scanned, then pick up the new item.
	imports.update(j, func(j *ImportJob) { j.Step = "Adding to your library" })
	a.Library.SetFolders(a.cfg().ScanFolders())
	a.Library.Rescan()
	items, _ := a.Library.Search(library.Query{Limit: 1 << 30})
	for _, it := range items {
		if it.Path != "" && sameFolder(it.Path, dir) {
			imports.update(j, func(j *ImportJob) { j.ItemID = it.ID })
			break
		}
	}
	return nil
}

func sameFolder(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func firstNonEmptyStr(a ...string) string {
	for _, s := range a {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func sitesFetchImage(ctx context.Context, u string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PrintHarbor")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return resp.Body, nil
}

// unzipInto extracts a downloaded zip into dir (no paths outside dir).
func unzipInto(zipPath, dir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	// Skip unsafe entries, then strip a single shared top-level folder
	// ("Model Name/files...").
	var entries []*zip.File
	for _, f := range zr.File {
		n := strings.TrimLeft(filepath.ToSlash(f.Name), "/")
		if f.FileInfo().IsDir() || n == "" || strings.Contains(n, "__MACOSX") || strings.Contains(n, ":") ||
			strings.HasPrefix(n, "../") || strings.Contains(n, "/../") || n == ".." {
			continue
		}
		entries = append(entries, f)
	}
	prefix := ""
	for i, f := range entries {
		top := strings.SplitN(strings.TrimLeft(filepath.ToSlash(f.Name), "/"), "/", 2)
		if len(top) < 2 {
			prefix = ""
			break
		}
		if i == 0 {
			prefix = top[0] + "/"
		} else if top[0]+"/" != prefix {
			prefix = ""
			break
		}
	}
	root := filepath.Clean(dir) + string(os.PathSeparator)
	var failed []string
	var total int64
	for _, f := range entries {
		name := strings.TrimPrefix(strings.TrimLeft(filepath.ToSlash(f.Name), "/"), prefix)
		if name == "" {
			continue
		}
		parts := strings.Split(name, "/")
		for i := range parts {
			parts[i] = safeName(parts[i], "_")
		}
		dst := filepath.Join(append([]string{dir}, parts...)...)
		if !strings.HasPrefix(dst, root) {
			continue
		}
		// A zip can claim to hold far more than fits on the disk ("zip bomb").
		if total += int64(f.UncompressedSize64); total > maxUnzip || f.UncompressedSize64 > maxUnzip {
			return fmt.Errorf("the zip unpacks to more than %d GB", maxUnzip>>30)
		}
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		src, err := f.Open()
		if err != nil {
			failed = append(failed, name)
			continue
		}
		out, err := os.Create(dst)
		if err == nil {
			var n int64
			n, err = io.Copy(out, io.LimitReader(src, int64(f.UncompressedSize64)+1))
			if e := out.Close(); err == nil {
				err = e
			}
			if err == nil && uint64(n) != f.UncompressedSize64 {
				err = fmt.Errorf("size mismatch")
			}
			if err != nil {
				os.Remove(dst) // never leave half a model file behind
			}
		}
		src.Close()
		if err != nil {
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d file(s) in the zip couldn't be unpacked (first: %s)", len(failed), failed[0])
	}
	return nil
}

// maxUnzip is the most a downloaded zip may unpack to.
const maxUnzip = 8 << 30

// refileImported keeps a model that was imported from a site filed as
// <models folder>\<designer>\<model> after you correct its title or
// designer in the library: the folder is renamed and printharbor.json updated.
func (a *App) refileImported(it library.Item) (library.Item, bool) {
	if it.Source != "folder" || it.Path == "" {
		return it, false
	}
	if fi, err := os.Stat(it.Path); err != nil || !fi.IsDir() {
		return it, false // a loose file or a zip: details stay in PrintHarbor's index
	}
	side := filepath.Join(it.Path, library.SidecarName)
	var sc library.Sidecar
	b, err := os.ReadFile(side)
	imported := false
	if err == nil {
		if json.Unmarshal(b, &sc) != nil {
			return it, false
		}
		imported = sc.Imported != "" // imported by PrintHarbor from a model site (a source link you add later does not count)
	}
	sc.Title, sc.Designer, sc.SourceURL = it.Title, it.Designer, it.SourceURL
	sc.Description, sc.License, sc.Category, sc.Tags = it.Description, it.License, it.Category, it.Tags
	if s, id, ok := sites.ParseLink(it.SourceURL); ok {
		sc.Site, sc.SiteID = s, id
	}
	nb, _ := json.MarshalIndent(sc, "", "  ")
	_ = os.WriteFile(side, nb, 0o644)
	if !imported {
		return it, false // your own folders keep their names; only imported models are refiled
	}
	dir := a.modelFolder(sites.Model{Card: sites.Card{Title: it.Title, Author: it.Designer}})
	md := a.cfg().ModelsDir()
	if rel, err := filepath.Rel(md, it.Path); err != nil || strings.HasPrefix(rel, "..") || sameFolder(dir, it.Path) {
		return it, false // not in the models folder, or already right
	}
	if _, err := os.Stat(dir); err == nil {
		return it, false // a model with that name is already there
	}
	_ = os.MkdirAll(filepath.Dir(dir), 0o755)
	if err := os.Rename(it.Path, dir); err != nil {
		return it, false
	}
	// Drop an empty designer folder left behind.
	if ents, err := os.ReadDir(filepath.Dir(it.Path)); err == nil && len(ents) == 0 {
		_ = os.Remove(filepath.Dir(it.Path))
	}
	a.Library.Rescan()
	items, _ := a.Library.Search(library.Query{Limit: 1 << 30})
	for _, x := range items {
		if sameFolder(x.Path, dir) {
			return x, true
		}
	}
	return it, false
}
