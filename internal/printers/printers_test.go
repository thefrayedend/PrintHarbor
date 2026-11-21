package printers

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/store"
)

func TestMoonraker(t *testing.T) {
	var uploaded, printFlag string
	var paused bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/printer/objects/query":
			io.WriteString(w, `{"result":{"status":{"webhooks":{"state":"ready"},
			  "print_stats":{"state":"printing","filename":"part.gcode","print_duration":600},
			  "virtual_sdcard":{"progress":0.25},"extruder":{"temperature":214.8,"target":215},
			  "heater_bed":{"temperature":60,"target":60}}}}`)
		case "/server/webcams/list":
			io.WriteString(w, `{"result":{"webcams":[{"enabled":true,"stream_url":"/webcam/?action=stream","snapshot_url":"/webcam/?action=snapshot"}]}}`)
		case "/webcam/":
			w.Header().Set("Content-Type", "image/jpeg")
			io.WriteString(w, "JPEG")
		case "/printer/print/pause":
			paused = true
			io.WriteString(w, `{"result":"ok"}`)
		case "/server/files/upload":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			f, h, _ := r.FormFile("file")
			b, _ := io.ReadAll(f)
			uploaded = h.Filename + ":" + string(b)
			printFlag = r.FormValue("print")
			io.WriteString(w, `{"result":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	m, err := NewMoonraker(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StatePrinting || st.Progress != 0.25 || st.RemainingSec != 1800 || st.NozzleTarget != 215 {
		t.Fatalf("bad status %+v", st)
	}
	if st.SnapshotURL != "/webcam/?action=snapshot" {
		t.Fatal(st.SnapshotURL)
	}
	// The relative path isn't on port 80 of the test server, so the driver
	// must fall back to Moonraker's own port, where we serve it.
	img, ct, err := m.Snapshot(ctx)
	if err != nil || ct != "image/jpeg" || string(img) != "JPEG" {
		t.Fatalf("snapshot: %v %q %q", err, ct, img)
	}
	if err := m.Pause(ctx); err != nil || !paused {
		t.Fatal("pause failed", err)
	}
	if err := m.Upload(ctx, "plate.gcode", strings.NewReader("G28"), true); err != nil {
		t.Fatal(err)
	}
	if uploaded != "plate.gcode:G28" || printFlag != "true" {
		t.Fatal(uploaded, printFlag)
	}
}

func TestMoonrakerAddress(t *testing.T) {
	m, err := NewMoonraker("192.168.1.9", "")
	if err != nil || m.base.String() != "http://192.168.1.9:7125" {
		t.Fatal(m.base, err)
	}
}

func TestRegistryBedClearFlow(t *testing.T) {
	s, _ := store.New(t.TempDir())
	r, err := NewRegistry(s)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Put(Printer{Name: "Sim", Driver: "mock", Address: "1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r.PollOnce(ctx)
	if v, _ := r.Get(p.ID); !v.Ready {
		t.Fatalf("new printer should be ready: %+v", v)
	}
	if err := r.Send(ctx, p.ID, "a.gcode", strings.NewReader("x"), true); err != nil {
		t.Fatal(err)
	}
	r.PollOnce(ctx)
	if v, _ := r.Get(p.ID); v.Status.State != StatePrinting || v.Ready {
		t.Fatalf("should be printing: %+v", v.Status)
	}
	time.Sleep(1100 * time.Millisecond)
	r.PollOnce(ctx)
	r.PollOnce(ctx)
	v, _ := r.Get(p.ID)
	if v.Status.State != StateFinished || v.Ready {
		t.Fatalf("finished print must wait for bed clear: %+v", v.Status)
	}
	if err := r.SetBedClear(p.ID, true); err != nil {
		t.Fatal(err)
	}
	r.PollOnce(ctx)
	if v, _ := r.Get(p.ID); !v.Ready {
		t.Fatalf("should be ready after clearing: %+v", v)
	}
	// Reload from disk keeps printers.
	r2, _ := NewRegistry(s)
	if len(r2.List()) != 1 {
		t.Fatal("printer not persisted")
	}
}

func TestSnapmakerMonitorCamera(t *testing.T) {
	var kicks int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/printer/objects/query":
			io.WriteString(w, `{"result":{"status":{"webhooks":{"state":"ready"},"print_stats":{"state":"standby"}}}}`)
		case "/server/webcams/list":
			io.WriteString(w, `{"result":{"webcams":[]}}`)
		case "/websocket":
			wsServe(t, w, r, func(req map[string]any) any {
				if req["method"] == "camera.start_monitor" {
					atomic.AddInt32(&kicks, 1)
					return map[string]any{"state": "success", "url": "/files/camera/monitor.jpg"}
				}
				return nil
			})
		case "/server/files/camera/monitor.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			io.WriteString(w, "U1JPEG")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m, _ := NewMoonraker(srv.URL, "")
	ctx := context.Background()
	st, _ := m.Status(ctx)
	if st.SnapshotURL != "/server/files/camera/monitor.jpg" {
		t.Fatalf("U1 camera not detected: %q", st.SnapshotURL)
	}
	img, _, err := m.Snapshot(ctx)
	// The keepalive during viewing runs in the background; give it a moment.
	for i := 0; i < 50 && atomic.LoadInt32(&kicks) < 2; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || string(img) != "U1JPEG" || atomic.LoadInt32(&kicks) < 2 {
		t.Fatalf("snapshot %q err %v kicks %d", img, err, kicks)
	}
}

// wsServe is a minimal websocket server for tests: it answers one JSON-RPC
// request (after sending a notification first, like Moonraker does).
func wsServe(t *testing.T, w http.ResponseWriter, r *http.Request, handle func(map[string]any) any) {
	h := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	conn, brw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Close()
	io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+
		base64.StdEncoding.EncodeToString(h[:])+"\r\n\r\n")
	_, payload, err := wsRead(bufio.NewReader(brw))
	if err != nil {
		t.Error(err)
		return
	}
	var req map[string]any
	json.Unmarshal(payload, &req)
	send := func(v any) {
		b, _ := json.Marshal(v)
		frame := []byte{0x81, 126, byte(len(b) >> 8), byte(len(b))}
		conn.Write(append(frame, b...))
	}
	send(map[string]any{"jsonrpc": "2.0", "method": "notify_proc_stat_update", "params": []any{}})
	send(map[string]any{"jsonrpc": "2.0", "result": handle(req), "id": req["id"]})
}

func TestSnapmakerSilentFirmware(t *testing.T) {
	kicked := make(chan bool, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/printer/objects/query":
			io.WriteString(w, `{"result":{"status":{"webhooks":{"state":"ready"},"print_stats":{"state":"standby"}}}}`)
		case "/server/webcams/list":
			io.WriteString(w, `{"result":{"webcams":[]}}`)
		case "/websocket":
			// Accept the request and never answer (Moonraker 1.5.x on the U1).
			h := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			conn, brw, _ := w.(http.Hijacker).Hijack()
			defer conn.Close()
			io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+
				base64.StdEncoding.EncodeToString(h[:])+"\r\n\r\n")
			if _, p, err := wsRead(bufio.NewReader(brw)); err == nil && strings.Contains(string(p), "start_monitor") {
				kicked <- true
			}
			time.Sleep(3 * time.Second)
		case "/server/files/camera/monitor.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			io.WriteString(w, "OLDU1")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m, _ := NewMoonraker(srv.URL, "")
	st, _ := m.Status(context.Background())
	if st.SnapshotURL == "" || len(kicked) == 0 {
		t.Fatalf("silent U1 camera not detected: %q kicks=%d", st.SnapshotURL, len(kicked))
	}
}

func TestLevelGcode(t *testing.T) {
	// G-code drivers: the leveling command baked into start g-code.
	for d, want := range map[string]string{
		"moonraker": "BED_MESH_CALIBRATE", "elegoo": "BED_MESH_CALIBRATE", "prusalink": "G80",
		"flashforge": "", "bambu": "", "mock": "", "": "",
	} {
		if got := DefaultLevelGcode(d); got != want {
			t.Errorf("DefaultLevelGcode(%q) = %q, want %q", d, got, want)
		}
	}
	// FlashForge levels through its own print API; Bambu levels every print.
	if !NativeLevel("flashforge") {
		t.Error("flashforge should level natively")
	}
	if !AlwaysLevels("bambu") {
		t.Error("bambu should always level")
	}
	for _, d := range []string{"moonraker", "elegoo", "prusalink", "bambu"} {
		if NativeLevel(d) {
			t.Errorf("%s is not native-level", d)
		}
	}
	// A per-printer command (trimmed) overrides the default, for any driver.
	if g := LevelGcodeFor(Printer{Driver: "elegoo", LevelGcode: "  G29 Z  "}); g != "G29 Z" {
		t.Errorf("override effective = %q", g)
	}
	if g := LevelGcodeFor(Printer{Driver: "prusalink"}); g != "G80" {
		t.Errorf("prusa default = %q", g)
	}
}
