package api

import (
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/thefrayedend/printharbor/internal/extension"
	"github.com/thefrayedend/printharbor/internal/sites"
)

// Downloads from your own browser: PrintHarbor's extension (Chrome/Edge)
// reports each file you download from Printables, Thingiverse, MakerWorld or
// Cults3D, with the model page it came from. PrintHarbor moves the file into
// <models folder>\<designer>\<model>\ and adds it to the library.

// ExtensionDir is where the unpacked extension is kept (next to the program).
func ExtensionDir() string {
	if p, err := filepath.Abs("extension"); err == nil {
		return p
	}
	return "extension"
}

type capturePage struct {
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Designer    string   `json:"designer"`
	Description string   `json:"description"`
	License     string   `json:"license"`
	Category    string   `json:"category"`
	Images      []string `json:"images"`
	Tags        []string `json:"tags"`
}

type captureReq struct {
	Path string       `json:"path"`
	Name string       `json:"name"`
	URL  string       `json:"url"`
	Size int64        `json:"size"`
	Page *capturePage `json:"page"`
}

var captureExts = map[string]bool{".stl": true, ".3mf": true, ".obj": true, ".step": true, ".stp": true, ".zip": true, ".gcode": true,
	".bgcode": true, ".scad": true, ".f3d": true, ".amf": true, ".ply": true, ".7z": true, ".rar": true, ".pdf": true, ".png": true, ".jpg": true}

var reTitleJunk = regexp.MustCompile(`(?i)^(free\s+)?(stl|3d|obj|3mf|step)\s+(file|model)\s+`)

// cleanPageTitle removes site decorations ("Free STL file Benchy・3D model・Cults" → "Benchy").
func cleanPageTitle(s string) string {
	s = strings.TrimSpace(s)
	for _, sep := range []string{"・", " | ", " - Cults", " - Printables", " - Thingiverse", " - MakerWorld"} {
		if i := strings.Index(s, sep); i > 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(reTitleJunk.ReplaceAllString(s, ""))
}

// captureModel turns what the extension read from the page into model
// details, using the site's API too when it is set up (Printables always is).
func (a *App) captureModel(ctx context.Context, in captureReq, name string) sites.Model {
	var m sites.Model
	if p := in.Page; p != nil {
		m = sites.Model{Card: sites.Card{Title: cleanPageTitle(p.Title), Author: strings.TrimPrefix(strings.TrimSpace(p.Designer), "@"), URL: p.URL},
			Description: p.Description, Images: p.Images, Tags: p.Tags, License: p.License, Category: p.Category}
		if s, id, ok := sites.ParseLink(p.URL); ok {
			m.Site, m.ID = s, id
			if site, err := a.site(s); err == nil && site.Info().Ready {
				cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				am, err := site.Model(cctx, id)
				cancel()
				if err == nil {
					pick := func(api, page string) string {
						if strings.TrimSpace(api) != "" {
							return api
						}
						return page
					}
					m.Title, m.Author, m.Description = pick(am.Title, m.Title), pick(am.Author, m.Author), pick(am.Description, m.Description)
					m.License, m.Category = pick(am.License, m.License), pick(am.Category, m.Category)
					if am.URL != "" {
						m.URL = am.URL
					}
					if len(am.Images) > 0 {
						m.Images = am.Images
					}
					if len(am.Tags) > 0 {
						m.Tags = am.Tags
					}
				}
			}
		}
	}
	if strings.TrimSpace(m.Title) == "" {
		m.Title = strings.TrimSuffix(name, filepath.Ext(name))
	}
	return m
}

func (a *App) fileCapture(in captureReq, j *ImportJob) error {
	ctx := context.Background()
	name := in.Name
	if name == "" {
		name = filepath.Base(in.Path)
	}
	m := a.captureModel(ctx, in, name)
	dir := a.modelFolder(m)
	imports.update(j, func(j *ImportJob) {
		j.Title, j.Folder, j.Site, j.ModelID, j.Step = m.Title, dir, m.Site, m.ID, "Filing "+name
	})
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("can't create %s: %w", dir, err)
	}
	dst := filepath.Join(dir, safeName(name, "download"))
	// A file that was downloaded straight into its model folder is already in
	// place: moving it "over itself" would delete it.
	if si, e1 := os.Stat(in.Path); e1 != nil {
		return fmt.Errorf("the downloaded file is gone: %w", e1)
	} else if di, e2 := os.Stat(dst); e2 != nil || !os.SameFile(si, di) {
		if err := moveFile(in.Path, dst); err != nil {
			return err
		}
	}
	if strings.EqualFold(filepath.Ext(dst), ".zip") {
		// Only drop the zip when everything in it came out; otherwise keep it.
		if err := unzipInto(dst, dir); err == nil {
			os.Remove(dst)
		} else {
			log.Printf("capture: kept %s (%v)", dst, err)
		}
	}
	siteName := siteNameOf(m.Site)
	if siteName == "" {
		siteName = "Downloads"
	}
	return a.saveDetails(ctx, j, m.Site, siteName, m, dir, false)
}

func siteNameOf(id string) string {
	return map[string]string{"printables": "Printables", "thingiverse": "Thingiverse", "makerworld": "MakerWorld", "cults": "Cults3D"}[id]
}

// fromExtension: only PrintHarbor's extension may report downloads (the
// custom header makes the browser refuse it from ordinary web pages).
func fromExtension(r *http.Request) bool {
	o := r.Header.Get("Origin")
	return r.Header.Get("X-PrintHarbor") == "1" && (strings.HasPrefix(o, "chrome-extension://") || strings.HasPrefix(o, "moz-extension://"))
}

func (a *App) captureRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/capture", func(w http.ResponseWriter, r *http.Request) {
		a.extMu.Lock()
		seen := a.extSeen
		a.extMu.Unlock()
		writeJSON(w, 200, map[string]any{"folder": ExtensionDir(), "version": extension.Version, "last_download": seen, "model_dir": a.cfg().ModelsDir()})
	})
	mux.HandleFunc("POST /api/capture", func(w http.ResponseWriter, r *http.Request) {
		if !fromExtension(r) {
			fail(w, 403, fmt.Errorf("only PrintHarbor's browser extension can do this"))
			return
		}
		var in captureReq
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		fi, err := os.Stat(in.Path)
		if err != nil || !fi.Mode().IsRegular() || !filepath.IsAbs(in.Path) {
			fail(w, 400, fmt.Errorf("can't find the downloaded file"))
			return
		}
		in.Name = filepath.Base(strings.TrimSpace(in.Name))
		if in.Name == "" || in.Name == "." || !captureExts[strings.ToLower(filepath.Ext(in.Name))] {
			in.Name = filepath.Base(in.Path)
		}
		if !captureExts[strings.ToLower(filepath.Ext(in.Name))] {
			fail(w, 400, fmt.Errorf("not a model file"))
			return
		}
		a.extMu.Lock()
		a.extSeen = time.Now()
		a.extMu.Unlock()
		j, started := imports.add(&ImportJob{State: "running", Started: time.Now(), Title: in.Name, Step: "Filing"})
		go func() {
			err := a.fileCapture(in, j)
			imports.update(j, func(j *ImportJob) {
				j.Finished = time.Now()
				if err != nil {
					j.State, j.Error = "error", err.Error()
				} else {
					j.State, j.Step = "done", "Saved "+in.Name
				}
			})
		}()
		writeJSON(w, 202, started)
	})

	// The desktop app window captures downloads by URL: when you click a model
	// site's download (or its "open in <slicer>" hand-off), the window's own
	// program (not the web page) sends the file's URL here and PrintHarbor
	// downloads it and files it in your library. No extension, no token.
	// Web pages can't call this: it is not opened up cross-origin (see guard.go),
	// and the download itself is limited to public https hosts.
	mux.HandleFunc("POST /api/sites/capture-url", func(w http.ResponseWriter, r *http.Request) {
		var in captureURLReq
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		in.URL = strings.TrimSpace(in.URL)
		if err := captureURLOK(in.URL); err != nil {
			fail(w, 400, err)
			return
		}
		name := captureName(in.Name, in.URL)
		a.extMu.Lock()
		a.extSeen = time.Now()
		a.extMu.Unlock()
		title := name
		if title == "" {
			title = "Download"
		}
		j, started := imports.add(&ImportJob{State: "running", Started: time.Now(), Title: title, Step: "Downloading"})
		go func() {
			err := a.urlCapture(in, name, j)
			imports.update(j, func(j *ImportJob) {
				j.Finished = time.Now()
				if err != nil {
					j.State, j.Error = "error", err.Error()
				} else {
					j.State, j.Step = "done", "Saved "+j.Title
				}
			})
		}()
		writeJSON(w, 202, started)
	})
}

// maxCapture is the biggest file the app window will download by URL.
const maxCapture = 800 << 20

// captureClient downloads model files for the app window: public https only.
var captureClient = publicClient(20 * time.Minute)

type captureURLReq struct {
	URL  string       `json:"url"`
	Name string       `json:"name"`
	Page *capturePage `json:"page"`
}

// captureURLOK rejects anything but a public https URL: no other schemes, and no
// localhost / LAN / IP-literal hosts, so this can't be used to reach the
// user's own network (SSRF). The name is checked here; where it actually
// points is checked again when connecting (captureClient).
func captureURLOK(raw string) error {
	if raw == "" {
		return fmt.Errorf("no download link")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return fmt.Errorf("only https downloads are allowed")
	}
	host := u.Hostname()
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return fmt.Errorf("that host isn't allowed")
	}
	if ip := net.ParseIP(host); ip != nil {
		return fmt.Errorf("downloads must come from a site, not an IP address")
	}
	return nil
}

// captureName picks a model-file name from the given name or the URL path, or ""
// to decide later from the response's Content-Disposition.
func captureName(name, u string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name != "" && name != "." && captureExts[strings.ToLower(filepath.Ext(name))] {
		return name
	}
	if pu, err := url.Parse(u); err == nil {
		b := filepath.Base(path.Clean("/" + pu.Path))
		if b != "" && b != "." && captureExts[strings.ToLower(filepath.Ext(b))] {
			return b
		}
	}
	return ""
}

// urlCapture downloads the file and files it into the library like a real
// browser download would, using the page info the window scraped for metadata.
func (a *App) urlCapture(in captureURLReq, name string, j *ImportJob) error {
	req, err := http.NewRequest("GET", in.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (PrintHarbor)")
	if in.Page != nil && in.Page.URL != "" {
		req.Header.Set("Referer", in.Page.URL)
	}
	resp, err := captureClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("the site returned %s for the download", resp.Status)
	}
	if name == "" {
		if cd := resp.Header.Get("Content-Disposition"); cd != "" {
			if _, params, e := mime.ParseMediaType(cd); e == nil {
				if fn := filepath.Base(params["filename"]); fn != "" && captureExts[strings.ToLower(filepath.Ext(fn))] {
					name = fn
				}
			}
		}
	}
	if name == "" || !captureExts[strings.ToLower(filepath.Ext(name))] {
		return fmt.Errorf("that download isn't a model file")
	}
	imports.update(j, func(j *ImportJob) { j.Title, j.Step = name, "Downloading "+name })
	tmp, err := os.CreateTemp("", "ph-cap-*"+filepath.Ext(name))
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	n, cerr := io.Copy(tmp, io.LimitReader(resp.Body, maxCapture+1))
	if e := tmp.Close(); cerr == nil {
		cerr = e
	}
	if cerr == nil && n > maxCapture {
		cerr = fmt.Errorf("that download is bigger than %d MB; download it in your browser and add it to the library instead", maxCapture>>20)
	}
	if cerr == nil && resp.ContentLength > 0 && n != resp.ContentLength {
		cerr = fmt.Errorf("the download stopped early (%d of %d bytes)", n, resp.ContentLength)
	}
	if cerr != nil {
		os.Remove(tmpName)
		return cerr
	}
	pageURL := in.URL
	if in.Page != nil && in.Page.URL != "" {
		pageURL = in.Page.URL
	}
	err = a.fileCapture(captureReq{Path: tmpName, Name: name, URL: pageURL, Size: n, Page: in.Page}, j)
	os.Remove(tmpName)
	return err
}
