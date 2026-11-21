package api

// Endpoints for "Test connection" and Bambu protocol capture (see
// internal/printers/capture.go). Capture lets us learn a new printer's
// protocol — the H3C tool changer especially — from the real machine:
// start it, drive the printer from Bambu's own app, stop it, send us the log.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/thefrayedend/printharbor/internal/printers"
)

func (a *App) captureDir() string { return filepath.Join(a.Store.Dir(), "captures") }

func (a *App) captureRoutesProto(mux *http.ServeMux) {
	// Test connection: try to reach an unsaved printer and say what's wrong.
	mux.HandleFunc("POST /api/printers/test", func(w http.ResponseWriter, r *http.Request) {
		var p printers.Printer
		if err := readJSON(r, &p); err != nil {
			fail(w, 400, err)
			return
		}
		// Editing without retyping the secret: use the stored one.
		if old, ok := a.Printers.Raw(p.ID); ok && (p.APIKey == "••••" || p.APIKey == "") {
			p.APIKey = old.APIKey
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := printers.Probe(ctx, p); err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "message": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "message": "Connected. The printer answered and will accept files and commands."})
	})

	// Start a protocol capture for a saved printer.
	mux.HandleFunc("POST /api/printers/{id}/capture", func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.Printers.Raw(r.PathValue("id"))
		if !ok {
			fail(w, 404, fmt.Errorf("printer not found"))
			return
		}
		a.capMu.Lock()
		defer a.capMu.Unlock()
		if a.captures == nil {
			a.captures = map[string]*printers.Capture{}
		}
		if c := a.captures[p.ID]; c != nil {
			c.Close()
			delete(a.captures, p.ID)
		}
		_ = os.MkdirAll(a.captureDir(), 0o755)
		path := filepath.Join(a.captureDir(), fmt.Sprintf("%s-%s.jsonl", p.ID, time.Now().Format("20060102-150405")))
		c, err := printers.StartCapture(p, path)
		if err != nil {
			fail(w, 400, err)
			return
		}
		a.captures[p.ID] = c
		writeJSON(w, 200, map[string]any{"ok": true, "message": "Recording. Now drive the printer from Bambu Studio or Handy — tool changes, a multi-colour print — then stop and send the file."})
	})

	mux.HandleFunc("POST /api/printers/{id}/capture/stop", func(w http.ResponseWriter, r *http.Request) {
		a.capMu.Lock()
		c := a.captures[r.PathValue("id")]
		delete(a.captures, r.PathValue("id"))
		a.capMu.Unlock()
		if c == nil {
			writeJSON(w, 200, map[string]any{"ok": true, "running": false})
			return
		}
		count, _, _ := c.Stats()
		c.Close()
		writeJSON(w, 200, map[string]any{"ok": true, "running": false, "count": count, "file": filepath.Base(c.Path())})
	})

	mux.HandleFunc("GET /api/printers/{id}/capture", func(w http.ResponseWriter, r *http.Request) {
		a.capMu.Lock()
		c := a.captures[r.PathValue("id")]
		a.capMu.Unlock()
		out := map[string]any{"ok": true, "running": c != nil}
		if c != nil {
			count, started, last := c.Stats()
			out["count"], out["started"], out["last"], out["file"] = count, started, last, filepath.Base(c.Path())
		}
		writeJSON(w, 200, out)
	})

	// Download a capture file (running or finished).
	mux.HandleFunc("GET /api/captures/{file}", func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.PathValue("file"))
		path := filepath.Join(a.captureDir(), name)
		if _, err := os.Stat(path); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		w.Header().Set("Content-Type", "application/x-ndjson")
		http.ServeFile(w, r, path)
	})
}
