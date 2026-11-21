package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/printers"
)

// bedJPEG draws a textured bed, optionally with a part on it, at a brightness.
func bedJPEG(part bool, bright int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			v := uint8(min(255, 70+bright+(x/16+y/16)%2*25))
			img.Set(x, y, color.RGBA{v, v, v, 255})
		}
	}
	if part {
		for y := 80; y < 170; y++ {
			for x := 110; x < 220; x++ {
				img.Set(x, y, color.RGBA{220, 60, 40, 255})
			}
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 85})
	return b.Bytes()
}

func TestBedSignature(t *testing.T) {
	empty, _ := bedSignature(bedJPEG(false, 0))
	brighter, _ := bedSignature(bedJPEG(false, 40))
	full, _ := bedSignature(bedJPEG(true, 0))
	if d := bedDifference(empty, brighter); d >= bedEmptyThreshold {
		t.Fatalf("lighting change should still look empty: %.3f", d)
	}
	if d := bedDifference(empty, full); d < bedEmptyThreshold {
		t.Fatalf("a part on the bed should not look empty: %.3f", d)
	}
}

func TestCameraFeatures(t *testing.T) {
	var mu sync.Mutex
	frame := bedJPEG(false, 0)
	cam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(frame)
	}))
	defer cam.Close()
	calls := 0
	ml := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("img"), "/snapshot") {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n <= 35 { // a good print first, then spaghetti
			io.WriteString(w, `{"detections":[]}`)
			return
		}
		io.WriteString(w, `{"detections":[["failure",0.92,[1,2,3,4]],["failure",0.6,[1,2,3,4]]]}`)
	}))
	defer ml.Close()
	var discord []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		_, fh, _ := r.FormFile("files[0]")
		mu.Lock()
		discord = append(discord, r.FormValue("payload_json")+boolStr(fh != nil))
		mu.Unlock()
	}))
	defer hook.Close()

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir, cfg.ObicoMLURL, cfg.DiscordWebhook, cfg.PublicURL = filepath.Join(dir, "data"), ml.URL, hook.URL, "http://127.0.0.1:1"
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := a.Printers.Put(printers.Printer{Name: "Cam", Driver: "mock", Address: "60", SnapshotURL: cam.URL, AIDetect: true, AIPause: true})
	ctx := context.Background()

	// Empty-bed learning and checking.
	if _, _, _, err := a.BedCheck(ctx, p.ID); err == nil {
		t.Fatal("no references yet should be an error")
	}
	if n, err := a.SaveEmptyBed(ctx, p.ID); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if empty, _, _, err := a.BedCheck(ctx, p.ID); err != nil || !empty {
		t.Fatal("same frame should look empty", err)
	}
	mu.Lock()
	frame = bedJPEG(true, 10)
	mu.Unlock()
	if empty, diff, _, _ := a.BedCheck(ctx, p.ID); empty {
		t.Fatalf("part on bed looked empty (%.3f)", diff)
	}

	// AI (Obico's scoring): a good start, then spaghetti → warning, then pause.
	a.Printers.Send(ctx, p.ID, "x.gcode", strings.NewReader("G28"), true)
	a.Printers.PollOnce(ctx)
	v, _ := a.Printers.Get(p.ID)
	a.aim.mu.Lock()
	s := a.aiFor(p.ID)
	a.aiTrackJob(v, s)
	a.aim.mu.Unlock()
	for i := 0; i < 60; i++ {
		a.aiCheck(ctx, v, s)
		a.Printers.PollOnce(ctx)
		if cur, _ := a.Printers.Get(p.ID); cur.Status.State != printers.StatePrinting {
			break
		}
	}
	a.Printers.PollOnce(ctx)
	if v, _ := a.Printers.Get(p.ID); v.Status.State != printers.StatePaused {
		t.Fatalf("AI should have paused the print: %s", v.Status.State)
	}
	als := a.Alerts(0)
	foundErr := false
	for _, al := range als {
		foundErr = foundErr || (al.Level == "error" && strings.Contains(al.Text, "PAUSED"))
	}
	if !foundErr {
		t.Fatalf("alerts %+v", als)
	}
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(discord) == 0 || !strings.HasSuffix(discord[len(discord)-1], "true") {
		t.Fatalf("discord %v", discord)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestPrinterViewJSON checks the UI payload: the empty-bed flag is present and
// the embedded printer/status fields stay flattened (the web UI reads p.name,
// p.bed_clear and p.has_empty_bed off the same object).
func TestPrinterViewJSON(t *testing.T) {
	pv := PrinterView{HasEmptyBed: true}
	pv.Name = "Snapmaker U1"
	pv.BedClear = true
	b, err := json.Marshal(pv)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"has_empty_bed":true`, `"name":"Snapmaker U1"`, `"bed_clear":true`} {
		if !strings.Contains(s, want) {
			t.Fatalf("printer view JSON missing %s\n%s", want, s)
		}
	}
}
