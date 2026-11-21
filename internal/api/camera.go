package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // decoders for camera frames
	_ "image/png"
	"log"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thefrayedend/printharbor/internal/mdns"
	"github.com/thefrayedend/printharbor/internal/printers"
)

// ---------- alerts ----------

type Alert struct {
	ID          int       `json:"id"`
	Time        time.Time `json:"time"`
	PrinterID   string    `json:"printer_id,omitempty"`
	PrinterName string    `json:"printer_name,omitempty"`
	Level       string    `json:"level"` // info | warning | error
	Text        string    `json:"text"`
}

type alertLog struct {
	mu     sync.Mutex
	next   int
	alerts []Alert
}

// Alert records an alert, and posts it to Discord (with a camera frame when
// there is one) if a webhook is configured.
func (a *App) Alert(printerID, level, text string, frame []byte) {
	name := ""
	if v, ok := a.Printers.Get(printerID); ok {
		name = v.Name
	}
	a.alerts.mu.Lock()
	a.alerts.next++
	al := Alert{ID: a.alerts.next, Time: time.Now(), PrinterID: printerID, PrinterName: name, Level: level, Text: text}
	a.alerts.alerts = append(a.alerts.alerts, al)
	if len(a.alerts.alerts) > 200 {
		a.alerts.alerts = a.alerts.alerts[len(a.alerts.alerts)-200:]
	}
	a.alerts.mu.Unlock()
	log.Printf("alert [%s] %s: %s", level, name, text)
	if hook := strings.TrimSpace(a.cfg().DiscordWebhook); hook != "" {
		go postDiscord(hook, fmt.Sprintf("**%s** %s", name, text), frame)
	}
}

func (a *App) Alerts(since int) []Alert {
	a.alerts.mu.Lock()
	defer a.alerts.mu.Unlock()
	out := []Alert{}
	for _, al := range a.alerts.alerts {
		if al.ID > since {
			out = append(out, al)
		}
	}
	return out
}

func postDiscord(hook, text string, frame []byte) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	j, _ := json.Marshal(map[string]string{"content": text, "username": "PrintHarbor"})
	_ = mw.WriteField("payload_json", string(j))
	if len(frame) > 0 {
		fw, _ := mw.CreateFormFile("files[0]", "camera.jpg")
		fw.Write(frame)
	}
	mw.Close()
	req, err := http.NewRequest("POST", hook, &body)
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	c := &http.Client{Timeout: 15 * time.Second}
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
	}
}

// ---------- empty-bed detection ----------

const bedThumbW, bedThumbH = 64, 48

// bedSignature reduces a camera frame to a small normalized greyscale grid
// so lighting changes matter less than objects on the bed.
func bedSignature(img []byte) ([]float64, error) {
	im, _, err := image.Decode(bytes.NewReader(img))
	if err != nil {
		return nil, err
	}
	b := im.Bounds()
	sig := make([]float64, bedThumbW*bedThumbH)
	for y := 0; y < bedThumbH; y++ {
		for x := 0; x < bedThumbW; x++ {
			// Box-average the source pixels in this cell.
			x0, x1 := b.Min.X+x*b.Dx()/bedThumbW, b.Min.X+(x+1)*b.Dx()/bedThumbW
			y0, y1 := b.Min.Y+y*b.Dy()/bedThumbH, b.Min.Y+(y+1)*b.Dy()/bedThumbH
			var sum float64
			n := 0
			for yy := y0; yy < y1; yy += 2 {
				for xx := x0; xx < x1; xx += 2 {
					r, g, bl, _ := im.At(xx, yy).RGBA()
					sum += 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)
					n++
				}
			}
			if n > 0 {
				sig[y*bedThumbW+x] = sum / float64(n)
			}
		}
	}
	var mean, sd float64
	for _, v := range sig {
		mean += v
	}
	mean /= float64(len(sig))
	for _, v := range sig {
		sd += (v - mean) * (v - mean)
	}
	sd = math.Sqrt(sd / float64(len(sig)))
	if sd < 1 {
		sd = 1
	}
	for i := range sig {
		sig[i] = (sig[i] - mean) / sd
	}
	return sig, nil
}

// bedDifference is the mean absolute difference of two signatures
// (0 = identical; ~0.3+ usually means something is on the bed).
func bedDifference(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return math.Inf(1)
	}
	var d float64
	for i := range a {
		d += math.Abs(a[i] - b[i])
	}
	return d / float64(len(a))
}

const bedEmptyThreshold = 0.28

func (a *App) bedDir(id string) string { return filepath.Join(a.Store.Dir(), "beds", id) }

// hasEmptyBed reports whether this printer has empty-bed reference photos.
// Having them is what turns on camera bed-clear detection: a printer with a
// camera starts detecting as soon as it has been shown its empty bed, with no
// separate opt-in. Clearing the photos (DELETE .../empty-bed) turns it back off.
func (a *App) hasEmptyBed(id string) bool {
	m, _ := filepath.Glob(filepath.Join(a.bedDir(id), "*.jpg"))
	return len(m) > 0
}

// PrinterView is a printer View as the UI sees it, plus whether it has empty-bed
// reference photos (the UI shows bed-clear detection as on when it does).
type PrinterView struct {
	printers.View
	HasEmptyBed bool `json:"has_empty_bed"`
}

// PrintersForUI returns every printer view tagged with has_empty_bed.
func (a *App) PrintersForUI() []PrinterView {
	list := a.Printers.List()
	out := make([]PrinterView, len(list))
	for i, v := range list {
		out[i] = PrinterView{View: v, HasEmptyBed: a.hasEmptyBed(v.ID)}
	}
	return out
}

// bedFreshWait is how long to wait between two pictures to see that the camera
// is really giving new ones.
var bedFreshWait = 1200 * time.Millisecond

// freshSnapshot gets a picture that shows the bed as it is NOW. Some cameras
// (the Snapmaker U1's) go to sleep and keep handing out the last picture they
// took, which can be days old, until they have been awake for a few seconds.
// Two pictures of a live camera are never the same byte for byte (there is
// always a little noise), so it asks until the picture changes. fresh is false
// when it never did: the camera may be asleep, or frozen.
func (a *App) freshSnapshot(ctx context.Context, id string) (img []byte, fresh bool, err error) {
	first, _, err := a.Printers.Snapshot(ctx, id)
	if err != nil {
		return nil, false, err
	}
	img = first
	for i := 0; i < 4; i++ {
		select {
		case <-ctx.Done():
			return img, false, nil
		case <-time.After(bedFreshWait):
		}
		next, _, err := a.Printers.Snapshot(ctx, id)
		if err != nil {
			continue
		}
		if !bytes.Equal(next, first) {
			return next, true, nil
		}
	}
	return img, false, nil
}

// SaveEmptyBed stores the current frame as an "empty bed" reference
// (keeps the 6 most recent).
func (a *App) SaveEmptyBed(ctx context.Context, id string) (int, error) {
	img, _, err := a.freshSnapshot(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("couldn't get a camera picture: %w", err)
	}
	if _, err := bedSignature(img); err != nil {
		return 0, fmt.Errorf("camera picture unreadable: %w", err)
	}
	dir := a.bedDir(id)
	_ = os.MkdirAll(dir, 0o755)
	// The very same picture as one already kept teaches nothing, and means the
	// camera handed out an old picture instead of looking.
	if old, _ := filepath.Glob(filepath.Join(dir, "*.jpg")); len(old) > 0 {
		for _, p := range old {
			if b, err := os.ReadFile(p); err == nil && bytes.Equal(b, img) {
				return len(old), fmt.Errorf("the camera gave the same picture as last time, so it isn't showing the bed as it is now (it may be asleep). Open this printer's camera for a few seconds, then try again")
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.jpg", time.Now().UnixNano())), img, 0o644); err != nil {
		return 0, err
	}
	refs, _ := filepath.Glob(filepath.Join(dir, "*.jpg"))
	sort.Strings(refs)
	for len(refs) > 6 {
		_ = os.Remove(refs[0])
		refs = refs[1:]
	}
	return len(refs), nil
}

// BedCheck compares the current frame with the empty-bed references.
func (a *App) BedCheck(ctx context.Context, id string) (empty bool, diff float64, refs int, err error) {
	paths, _ := filepath.Glob(filepath.Join(a.bedDir(id), "*.jpg"))
	if len(paths) == 0 {
		return false, 0, 0, fmt.Errorf("no empty-bed pictures yet: clear the bed and click \"This is my empty bed\"")
	}
	img, _, err := a.freshSnapshot(ctx, id)
	if err != nil {
		return false, 0, len(paths), err
	}
	cur, err := bedSignature(img)
	if err != nil {
		return false, 0, len(paths), err
	}
	diff = math.Inf(1)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if ref, err := bedSignature(b); err == nil {
			diff = math.Min(diff, bedDifference(cur, ref))
		}
	}
	return diff < bedEmptyThreshold, diff, len(paths), nil
}

// selfURL is how other machines on the LAN (the Obico ML server) reach us.
func (a *App) selfURL() string {
	if u := strings.TrimRight(a.cfg().PublicURL, "/"); u != "" {
		return u
	}
	port := listenPort(a.cfg().Listen)
	host := "127.0.0.1"
	if ips := mdns.LocalIPv4(); len(ips) > 0 {
		host = ips[0].String()
	}
	return "http://" + net.JoinHostPort(host, port)
}

// cameraLoop runs the empty-bed check on finished printers (AI detection
// has its own loop, ai.go).
func (a *App) cameraLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	tick := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		tick++
		for _, v := range a.Printers.List() {
			if v.Status.SnapshotURL == "" {
				continue
			}
			// A bed waiting to be cleared: after a finished print, and also after a
			// stopped one (the printer is then idle, not "finished"; before build
			// 63 those were never looked at).
			waiting := !v.BedClear && (v.Status.State == printers.StateFinished || v.Status.State == printers.StateIdle)
			// Any printer with a camera that has been shown its empty bed gets
			// bed-clear detection — no per-printer opt-in. BedCheck returns early
			// (no snapshot taken) for a printer that has no empty-bed photos, so
			// this stays cheap for cameras that were never taught.
			if waiting && tick%2 == 0 {
				go func(v printers.View) {
					empty, diff, _, err := a.BedCheck(ctx, v.ID)
					if err == nil && empty {
						_ = a.Printers.SetBedClear(v.ID, true)
						a.Alert(v.ID, "info", fmt.Sprintf("The camera sees an empty bed (difference %.2f), so the printer is ready again.", diff), nil)
						a.emitEvent("bed_cleared", v.ID, map[string]any{"by": "camera"})
					}
				}(v)
			}
		}
	}
}

func (a *App) cameraRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/printers/{id}/empty-bed", func(w http.ResponseWriter, r *http.Request) {
		n, err := a.SaveEmptyBed(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]int{"references": n})
	})
	mux.HandleFunc("DELETE /api/printers/{id}/empty-bed", func(w http.ResponseWriter, r *http.Request) {
		_ = os.RemoveAll(a.bedDir(r.PathValue("id")))
		writeJSON(w, 200, map[string]int{"references": 0})
	})
	mux.HandleFunc("GET /api/printers/{id}/bed-check", func(w http.ResponseWriter, r *http.Request) {
		empty, diff, refs, err := a.BedCheck(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{"empty": empty, "difference": math.Round(diff*1000) / 1000, "references": refs})
	})
	mux.HandleFunc("GET /api/alerts", func(w http.ResponseWriter, r *http.Request) {
		since := 0
		fmt.Sscan(r.URL.Query().Get("since"), &since)
		writeJSON(w, 200, a.Alerts(since))
	})
}
