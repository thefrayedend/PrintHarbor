package api

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/thefrayedend/printharbor/internal/printers"
)

// The live camera, like FlashBridge's: one MJPEG stream per viewer. Printers
// with a real stream are passed straight through; cameras that only take
// snapshots are turned into a stream here (a few frames a second).
func (a *App) streamRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/printers/{id}/stream", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		w.Header().Set("Cache-Control", "no-store")
		const boundary = "printharborframe"
		writeFrame := func(b []byte) bool {
			if _, err := fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", boundary, len(b)); err != nil {
				return false
			}
			if _, err := w.Write(append(b, '\r', '\n')); err != nil {
				return false
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return true
		}
		// Live cameras: one shared connection to the printer, any number of viewers.
		if frames, leave, lastErr, ok := a.Printers.Watch(id); ok {
			defer leave()
			started := false
			for {
				select {
				case <-r.Context().Done():
					return
				case b := <-frames:
					if !started {
						w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
						started = true
					}
					if !writeFrame(b) {
						return
					}
				case <-time.After(10 * time.Second):
					if !started {
						msg := lastErr()
						if msg == "" {
							msg = "no picture from the camera"
						}
						http.Error(w, msg, http.StatusServiceUnavailable)
						return
					}
				}
			}
		}
		// Snapshot cameras: make a stream from frames.
		first := true
		for {
			b, ct, err := a.Printers.Snapshot(r.Context(), id)
			if err != nil {
				if first {
					http.Error(w, err.Error(), http.StatusNotFound)
					return
				}
			} else {
				if first {
					w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
					first = false
				}
				if _, err := fmt.Fprintf(w, "--%s\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n", boundary, ct, len(b)); err != nil {
					return
				}
				if _, err := w.Write(append(b, '\r', '\n')); err != nil {
					return
				}
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(400 * time.Millisecond):
			}
		}
	})
	mux.HandleFunc("GET /api/printers/{id}/thumb", func(w http.ResponseWriter, r *http.Request) {
		d, err := a.Printers.Driver(r.PathValue("id"))
		if err == nil {
			if t, ok := d.(printers.Thumber); ok {
				b, ct, err := t.Thumb(r.Context())
				if err == nil {
					w.Header().Set("Content-Type", ct)
					w.Header().Set("Cache-Control", "no-store")
					w.Write(b)
					return
				}
			}
		}
		http.NotFound(w, r)
	})
}

// flushCopy copies a stream to the browser, sending each chunk right away.
func flushCopy(w http.ResponseWriter, r io.Reader) {
	f, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if f != nil {
				f.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
