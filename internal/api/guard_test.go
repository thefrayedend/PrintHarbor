package api

import (
	"archive/zip"
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thefrayedend/printharbor/internal/config"
)

func guardApp(t *testing.T) *App {
	t.Helper()
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.OrcaProfilesDir = t.TempDir()
	cfg.OrcaUserPresetsDir = ""
	cfg.ModelDir = t.TempDir()
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A web page on another site must not be able to change anything; PrintHarbor's
// own pages, its extension (one endpoint) and non-browser programs must.
func TestGuardCrossSite(t *testing.T) {
	a := guardApp(t)
	hit := 0
	h := a.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit++; w.WriteHeader(204) }))
	cases := []struct {
		name, method, path, host, remote string
		hdr                              map[string]string
		want                             int
	}{
		{"own page", "POST", "/api/settings", "localhost:7621", "127.0.0.1:5000", map[string]string{"Origin": "http://localhost:7621"}, 204},
		{"own page by LAN name", "POST", "/api/settings", "harbor.local:7621", "192.168.1.20:5000", map[string]string{"Origin": "http://harbor.local:7621"}, 204},
		{"another site", "POST", "/api/printers/x/gcode", "localhost:7621", "127.0.0.1:5000", map[string]string{"Origin": "https://evil.example"}, 403},
		{"another site, form post", "POST", "/api/plugins/install", "localhost:7621", "127.0.0.1:5000", map[string]string{"Origin": "https://evil.example", "Content-Type": "multipart/form-data; boundary=x"}, 403},
		{"another port on this PC", "POST", "/api/settings", "localhost:7621", "127.0.0.1:5000", map[string]string{"Origin": "http://localhost:3000"}, 403},
		{"sandboxed page", "PUT", "/api/file", "localhost:7621", "127.0.0.1:5000", map[string]string{"Origin": "null"}, 403},
		{"old browser, no Origin", "POST", "/api/settings", "localhost:7621", "127.0.0.1:5000", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"not a browser", "POST", "/api/sites/capture-url", "localhost:7621", "127.0.0.1:5000", nil, 204},
		{"worker hub", "POST", "/api/worker/slice", "192.168.1.5:7621", "192.168.1.9:5000", map[string]string{"X-Worker-Token": "t"}, 204},
		{"extension", "POST", "/api/capture", "localhost:7621", "127.0.0.1:5000", map[string]string{"Origin": "chrome-extension://abc"}, 204},
		{"extension elsewhere", "POST", "/api/settings", "localhost:7621", "127.0.0.1:5000", map[string]string{"Origin": "chrome-extension://abc"}, 403},
		{"reading is open", "GET", "/api/printers", "localhost:7621", "127.0.0.1:5000", map[string]string{"Origin": "https://evil.example"}, 204},
		{"DNS rebinding", "GET", "/api/settings", "evil.example:7621", "127.0.0.1:5000", nil, 403},
		{"DNS rebinding v6", "GET", "/api/settings", "evil.example:7621", "[::1]:5000", nil, 403},
		{"remote name from another device", "GET", "/api/settings", "pc.tailnet.ts.net:7621", "100.70.1.2:5000", nil, 204},
		{"127.0.0.1", "GET", "/", "127.0.0.1:7621", "127.0.0.1:5000", nil, 204},
		{"pc name", "GET", "/", "desktop-jl01psa:7621", "127.0.0.1:5000", nil, 204},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader("{}"))
		r.Host, r.RemoteAddr = c.host, c.remote
		for k, v := range c.hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: got %d, want %d (%s)", c.name, w.Code, c.want, strings.TrimSpace(w.Body.String()))
		}
	}
	gc := a.cfg()
	gc.AllowedHosts = []string{"print.example.com"}
	a.setCfg(gc)
	r := httptest.NewRequest("GET", "/", nil)
	r.Host, r.RemoteAddr = "print.example.com", "127.0.0.1:5000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Errorf("allowed_hosts: got %d", w.Code)
	}
	if hit == 0 {
		t.Fatal("nothing got through")
	}
}

func TestPublicIP(t *testing.T) {
	for ip, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true, "151.101.1.1": true,
		"127.0.0.1": false, "::1": false, "10.0.0.5": false, "192.168.1.10": false, "172.16.4.4": false,
		"169.254.169.254": false, "0.0.0.0": false, "100.64.0.1": false, "fe80::1": false, "fd00::1": false,
		"::ffff:127.0.0.1": false, "::ffff:192.168.0.1": false, "224.0.0.1": false, "255.255.255.255": false,
	} {
		if got := publicIP(net.ParseIP(ip)); got != want {
			t.Errorf("publicIP(%s) = %v, want %v", ip, got, want)
		}
	}
	if publicIP(nil) {
		t.Error("nil is not public")
	}
	if err := publicOnlyDial("tcp", "127.0.0.1:7621", nil); err == nil {
		t.Error("loopback dial allowed")
	}
	if err := publicOnlyDial("tcp", "[2606:4700::1111]:443", nil); err != nil {
		t.Error(err)
	}
}

func TestProxiedImageType(t *testing.T) {
	for in, want := range map[string]string{
		"image/png": "image/png", "IMAGE/JPEG; charset=x": "image/jpeg", "image/webp": "image/webp",
		"text/html": "", "image/svg+xml": "", "application/javascript": "", "text/html; charset=utf-8": "",
		"application/octet-stream": "image/jpeg", "": "image/jpeg",
	} {
		if got := proxiedImageType(in); got != want {
			t.Errorf("proxiedImageType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnzipKeepsBrokenZip(t *testing.T) {
	dir := t.TempDir()
	zp := filepath.Join(dir, "m.zip")
	f, _ := os.Create(zp)
	zw := zip.NewWriter(f)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "Model/part.stl", Method: zip.Store})
	w.Write([]byte("solid part"))
	w, _ = zw.CreateHeader(&zip.FileHeader{Name: "Model/readme.txt", Method: zip.Store})
	w.Write([]byte("hello"))
	zw.Close()
	f.Close()
	if err := unzipInto(zp, dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "part.stl")); string(b) != "solid part" {
		t.Fatalf("part.stl = %q", b)
	}
	// Damage one file's data: unzip must say so (callers then keep the zip).
	b, _ := os.ReadFile(zp)
	i := bytes.Index(b, []byte("solid part"))
	if i < 0 {
		t.Fatal("test zip not stored")
	}
	b[i] ^= 0xff
	os.WriteFile(zp, b, 0o644)
	out := t.TempDir()
	if err := unzipInto(zp, out); err == nil {
		t.Fatal("a damaged zip unpacked without an error")
	}
	if _, err := os.Stat(filepath.Join(out, "part.stl")); err == nil {
		t.Error("a half-unpacked file was left behind")
	}
}

func TestMoveAndReplace(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a.stl"), filepath.Join(dir, "b.stl")
	os.WriteFile(src, []byte("new"), 0o644)
	os.WriteFile(dst, []byte("old"), 0o444) // read-only target, like a project's original
	if err := replaceFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Fatalf("dst = %q", b)
	}
	if _, err := os.Stat(src); err == nil {
		t.Error("source still there")
	}
	os.Chmod(dst, 0o644)
	if err := moveFile(dst, filepath.Join(dir, "sub-missing", "c.stl")); err == nil {
		t.Error("moving into a missing folder should fail")
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Error("a failed move lost the file")
	}
}

// The connection itself is checked, not just the name: a public-looking name
// that points at this computer or the LAN must be refused.
func TestPublicDialRefusesPrivate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := publicRT.checked.RoundTrip(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("connected to a loopback address")
	}
	if !strings.Contains(err.Error(), "private address") {
		t.Fatalf("unexpected error: %v", err)
	}
}
