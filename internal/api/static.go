package api

import (
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Serving the web UI (the files built into the program, and 3D James).
//
// Every file is sent with a fingerprint of its contents (an ETag). The browser
// still asks on every load ("no-cache"), so an updated PrintHarbor never runs
// with yesterday's JavaScript, but when the fingerprint is unchanged the
// answer is a few bytes ("not modified") instead of the whole file. Text files
// are also sent gzip-compressed. Before this, every load of every page
// re-sent everything: 26 MB for 3D James alone, which is slow on a phone.

// webTypes are the content types of the UI's files. They are set here rather
// than left to the operating system: on Windows the type for ".js" comes from
// the registry, and a PC that has it wrong would break the slicer page.
var webTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".json": "application/json", ".map": "application/json", ".svg": "image/svg+xml",
	".txt": "text/plain; charset=utf-8", ".wasm": "application/wasm", ".png": "image/png", ".jpg": "image/jpeg",
	".ico": "image/x-icon", ".webp": "image/webp", ".woff2": "font/woff2",
}

// compressible are the types worth gzipping (pictures and fonts already are).
var compressible = map[string]bool{".html": true, ".js": true, ".mjs": true, ".css": true, ".json": true, ".map": true, ".svg": true, ".txt": true, ".wasm": true}

func etagOf(b []byte) string {
	h := sha1.Sum(b)
	return `W/"` + hex.EncodeToString(h[:10]) + `"`
}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

// etagMatches reports whether the browser already has this version
// (the If-None-Match header lists the fingerprints it holds).
func etagMatches(r *http.Request, etag string) bool {
	strip := func(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "W/") }
	for _, have := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		if have = strings.TrimSpace(have); have == "*" || (have != "" && strip(have) == strip(etag)) {
			return true
		}
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for _, enc := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, q, _ := strings.Cut(strings.TrimSpace(enc), ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") && strings.TrimSpace(q) != "q=0" {
			return true
		}
	}
	return false
}

// writeCompressed sends gz (gzip of the body) with its fingerprint, or just
// "not modified". Browsers that can't take gzip get it unpacked.
func writeCompressed(w http.ResponseWriter, r *http.Request, ctype, etag string, gz []byte) {
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Vary", "Accept-Encoding")
	h.Set("Cache-Control", "no-cache")
	if etagMatches(r, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", ctype)
	if !acceptsGzip(r) {
		if r.Method == http.MethodHead {
			return
		}
		if zr, err := gzip.NewReader(bytes.NewReader(gz)); err == nil {
			_, _ = io.Copy(w, zr)
		}
		return
	}
	h.Set("Content-Encoding", "gzip")
	h.Set("Content-Length", strconv.Itoa(len(gz)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(gz)
	}
}

type webAsset struct {
	etag, ctype string
	gz          []byte // nil: sent as it is (a picture, or too small to matter)
}

// staticHandler serves the built-in web UI.
func staticHandler(web fs.FS) http.Handler {
	if web == nil {
		return http.NotFoundHandler()
	}
	files := http.FileServerFS(web)
	var cache sync.Map // file name → *webAsset; the built-in files never change while running
	asset := func(name string) *webAsset {
		if v, ok := cache.Load(name); ok {
			return v.(*webAsset)
		}
		fi, err := fs.Stat(web, name)
		if err != nil || fi.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(web, name)
		if err != nil {
			return nil
		}
		ext := strings.ToLower(path.Ext(name))
		a := &webAsset{etag: etagOf(b), ctype: webTypes[ext]}
		if compressible[ext] && len(b) > 1024 {
			a.gz = gzipBytes(b)
		}
		cache.Store(name, a)
		return a
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		p := r.URL.Path
		// Leave everything unusual to the standard file server: other methods,
		// folders without the trailing slash and .../index.html (it redirects
		// both), and byte ranges.
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || strings.HasSuffix(p, "/index.html") || r.Header.Get("Range") != "" {
			files.ServeHTTP(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+p), "/")
		if strings.HasSuffix(p, "/") {
			name = strings.TrimPrefix(name+"/index.html", "/")
		}
		a := asset(name)
		if a == nil {
			files.ServeHTTP(w, r)
			return
		}
		if a.gz != nil && a.ctype != "" {
			writeCompressed(w, r, a.ctype, a.etag, a.gz)
			return
		}
		// Sent as it is; the file server answers "not modified" from the ETag.
		w.Header().Set("ETag", a.etag)
		if a.ctype != "" {
			w.Header().Set("Content-Type", a.ctype)
		}
		files.ServeHTTP(w, r)
	})
}

// buildID identifies this build of the server: a fingerprint of the program
// file itself. A page that was left open while PrintHarbor was updated sees it
// change and knows it is running the old version's code.
var buildID = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return Version
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return Version
	}
	h := sha1.Sum([]byte(Version + "/" + strconv.FormatInt(fi.Size(), 10) + "/" + fi.ModTime().UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(h[:6])
})
