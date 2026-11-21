package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(t *testing.T, h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(zr)
	return string(out)
}

func TestStaticHandler(t *testing.T) {
	big := strings.Repeat("function f(){ return 1; }\n", 400)
	web := fstest.MapFS{
		"index.html":        {Data: []byte("<!doctype html><title>ui</title>" + strings.Repeat("<p>hello</p>", 200))},
		"app.js":            {Data: []byte(big)},
		"tiny.js":           {Data: []byte("1;")},
		"icon.png":          {Data: []byte("\x89PNG" + strings.Repeat("x", 4000))},
		"slicer/index.html": {Data: []byte("<!doctype html><title>slicer</title>" + strings.Repeat("<p>s</p>", 300))},
		"slicer/host.js":    {Data: []byte("import './slicer.js';\n" + big)},
	}
	h := staticHandler(web)
	gz := map[string]string{"Accept-Encoding": "gzip, deflate, br"}

	// Compressed, typed and fingerprinted.
	w := get(t, h, "/app.js", gz)
	etag := w.Header().Get("ETag")
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" || etag == "" ||
		w.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("app.js: %d %v", w.Code, w.Header())
	}
	if gunzip(t, w.Body.Bytes()) != big {
		t.Error("app.js doesn't unpack to the file")
	}
	if w.Body.Len() >= len(big)/4 {
		t.Errorf("app.js was barely compressed: %d of %d", w.Body.Len(), len(big))
	}
	// Second load: nothing is sent again.
	w = get(t, h, "/app.js", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": etag})
	if w.Code != 304 || w.Body.Len() != 0 {
		t.Errorf("revalidation: %d with %d bytes", w.Code, w.Body.Len())
	}
	// A different (older) fingerprint gets the new file.
	if w = get(t, h, "/app.js", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": `W/"0000"`}); w.Code != 200 {
		t.Errorf("stale fingerprint: %d", w.Code)
	}
	// A client that can't take gzip still gets the file.
	w = get(t, h, "/app.js", nil)
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "" || w.Body.String() != big {
		t.Errorf("no gzip: %d %q", w.Code, w.Header().Get("Content-Encoding"))
	}
	// The page itself, at / and in a folder.
	for path, want := range map[string]string{"/": "<title>ui</title>", "/slicer/": "<title>slicer</title>"} {
		w = get(t, h, path, gz)
		if w.Code != 200 || !strings.Contains(gunzip(t, w.Body.Bytes()), want) || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s: %d %v", path, w.Code, w.Header())
		}
	}
	// Module scripts must come as JavaScript whatever the PC's registry says.
	if w = get(t, h, "/slicer/host.js", gz); !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") {
		t.Errorf("host.js type: %q", w.Header().Get("Content-Type"))
	}
	// Pictures and tiny files go as they are, still fingerprinted.
	for _, path := range []string{"/icon.png", "/tiny.js"} {
		w = get(t, h, path, gz)
		if w.Code != 200 || w.Header().Get("Content-Encoding") != "" || w.Header().Get("ETag") == "" {
			t.Errorf("%s: %d %v", path, w.Code, w.Header())
		}
		if w2 := get(t, h, path, map[string]string{"If-None-Match": w.Header().Get("ETag")}); w2.Code != 304 {
			t.Errorf("%s revalidation: %d", path, w2.Code)
		}
	}
	// The usual file-server behaviour is kept.
	if w = get(t, h, "/slicer", gz); w.Code != 301 || w.Header().Get("Location") != "slicer/" {
		t.Errorf("/slicer: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w = get(t, h, "/index.html", gz); w.Code != 301 {
		t.Errorf("/index.html: %d", w.Code)
	}
	if w = get(t, h, "/nope.js", gz); w.Code != 404 {
		t.Errorf("missing file: %d", w.Code)
	}
	if w = get(t, h, "/../../etc/passwd", gz); w.Code == 200 {
		t.Errorf("path escape: %d", w.Code)
	}
	if w = get(t, staticHandler(nil), "/", gz); w.Code != 404 {
		t.Errorf("no web files: %d", w.Code)
	}
}

func Test3DJamesCompressed(t *testing.T) {
	a := guardApp(t)
	page := "<html><body>" + strings.Repeat("<p>3D James</p>", 500) + "</body></html>"
	h := a.serve3DJames(fstest.MapFS{"3djames.html": {Data: []byte(page)}})
	w := get(t, h, "/3djames.html", map[string]string{"Accept-Encoding": "gzip"})
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("ETag") == "" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	body := gunzip(t, w.Body.Bytes())
	if !strings.Contains(body, "PrintHarbor bridge for 3D James") || !strings.Contains(body, "<p>3D James</p>") {
		t.Error("the page or its bridge is missing")
	}
	if i, j := strings.Index(body, "PrintHarbor bridge for 3D James"), strings.LastIndex(body, "</body>"); i > j {
		t.Error("the bridge was added after </body>")
	}
	if w2 := get(t, h, "/3djames.html", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": w.Header().Get("ETag")}); w2.Code != 304 || w2.Body.Len() != 0 {
		t.Errorf("second load: %d, %d bytes", w2.Code, w2.Body.Len())
	}
	if w3 := get(t, h, "/3djames.html", nil); w3.Code != 200 || w3.Body.String() != body {
		t.Error("without gzip the page differs")
	}
}
