package api

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/thefrayedend/printharbor/internal/printers"
	"github.com/thefrayedend/printharbor/internal/slicer"
)

// ControlRequest is one button press on the printer control page.
type ControlRequest struct {
	Op       string  `json:"op"`                 // temp | home | jog | motors_off | fan | speed | flow | zoffset
	Heater   string  `json:"heater"`             // nozzle | bed | chamber
	Axis     string  `json:"axis"`               // x | y | z | xy... for home
	Distance float64 `json:"distance"`           // mm for jog, mm adjust for zoffset
	Value    float64 `json:"value"`              // °C, percent
	Internal *bool   `json:"internal,omitempty"` // filtration (FlashBridge)
	External *bool   `json:"external,omitempty"`
}

// controlGcode turns a control request into G-code, with limits that keep a
// slipped finger from doing damage.
func controlGcode(req ControlRequest, v printers.View, driver string) (string, error) {
	busy := v.Status.State == printers.StatePrinting || v.Status.State == printers.StatePaused
	switch req.Op {
	case "temp":
		t := math.Round(req.Value)
		switch req.Heater {
		case "nozzle":
			if t < 0 || t > 320 {
				return "", fmt.Errorf("nozzle temperature must be 0–320 °C")
			}
			return fmt.Sprintf("M104 S%.0f", t), nil
		case "bed":
			if t < 0 || t > 130 {
				return "", fmt.Errorf("bed temperature must be 0–130 °C")
			}
			return fmt.Sprintf("M140 S%.0f", t), nil
		case "chamber":
			if t < 0 || t > 80 {
				return "", fmt.Errorf("chamber temperature must be 0–80 °C")
			}
			if h := v.Status.ChamberHeater; h != "" {
				return fmt.Sprintf("SET_HEATER_TEMPERATURE HEATER=%s TARGET=%.0f", h, t), nil
			}
			return fmt.Sprintf("M141 S%.0f", t), nil
		}
		// tool0, tool1, ... on multi-nozzle printers
		var n int
		if _, err := fmt.Sscanf(req.Heater, "tool%d", &n); err == nil && n >= 0 && n < 16 {
			if t < 0 || t > 320 {
				return "", fmt.Errorf("nozzle temperature must be 0–320 °C")
			}
			return fmt.Sprintf("M104 T%d S%.0f", n, t), nil
		}
		return "", fmt.Errorf("unknown heater %q", req.Heater)
	case "home":
		if busy {
			return "", fmt.Errorf("can't home while a print is running")
		}
		axes := strings.ToUpper(strings.Trim(req.Axis, " "))
		if axes == "" || axes == "ALL" {
			return "G28", nil
		}
		var parts []string
		for _, a := range axes {
			if !strings.ContainsRune("XYZ", a) {
				return "", fmt.Errorf("bad axis %q", req.Axis)
			}
			parts = append(parts, string(a))
		}
		return "G28 " + strings.Join(parts, " "), nil
	case "jog":
		if busy {
			return "", fmt.Errorf("can't move the toolhead while a print is running")
		}
		axis := strings.ToUpper(req.Axis)
		if len(axis) != 1 || !strings.Contains("XYZ", axis) {
			return "", fmt.Errorf("bad axis %q", req.Axis)
		}
		d := req.Distance
		if d == 0 || math.Abs(d) > 100 {
			return "", fmt.Errorf("move must be between 0.01 and 100 mm")
		}
		if v.Status.Homed != "" && !strings.Contains(strings.ToUpper(v.Status.Homed), axis) {
			return "", fmt.Errorf("home %s first", axis)
		}
		feed := 6000
		if axis == "Z" {
			feed = 600
		}
		return fmt.Sprintf("G91\nG1 %s%.2f F%d\nG90", axis, d, feed), nil
	case "motors_off":
		if busy {
			return "", fmt.Errorf("can't turn motors off while printing")
		}
		return "M84", nil
	case "fan":
		p := math.Max(0, math.Min(100, req.Value))
		if p == 0 {
			return "M107", nil
		}
		return fmt.Sprintf("M106 S%.0f", p*255/100), nil
	case "speed":
		if req.Value < 10 || req.Value > 300 {
			return "", fmt.Errorf("speed must be 10–300 %%")
		}
		return fmt.Sprintf("M220 S%.0f", req.Value), nil
	case "flow":
		if req.Value < 50 || req.Value > 150 {
			return "", fmt.Errorf("flow must be 50–150 %%")
		}
		return fmt.Sprintf("M221 S%.0f", req.Value), nil
	case "zoffset":
		if driver != "moonraker" && driver != "mock" {
			return "", printers.ErrUnsupported
		}
		if math.Abs(req.Distance) > 0.1 || req.Distance == 0 {
			return "", fmt.Errorf("adjust Z in steps up to 0.1 mm")
		}
		return fmt.Sprintf("SET_GCODE_OFFSET Z_ADJUST=%.3f MOVE=1", req.Distance), nil
	}
	return "", fmt.Errorf("unknown control %q", req.Op)
}

func (a *App) controlRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/printers/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, a.Printers.History(r.PathValue("id")))
	})
	mux.HandleFunc("GET /api/printers/{id}/console", func(w http.ResponseWriter, r *http.Request) {
		lines, err := a.Printers.Console(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, 400, err)
			return
		}
		if lines == nil {
			lines = []printers.ConsoleLine{}
		}
		writeJSON(w, 200, lines)
	})
	mux.HandleFunc("POST /api/printers/{id}/gcode", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Script string `json:"script"`
		}
		if err := readJSON(r, &in); err != nil || strings.TrimSpace(in.Script) == "" {
			fail(w, 400, fmt.Errorf("type a command"))
			return
		}
		if err := a.Printers.Gcode(r.Context(), r.PathValue("id"), in.Script); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/printers/{id}/control", func(w http.ResponseWriter, r *http.Request) {
		var req ControlRequest
		if err := readJSON(r, &req); err != nil {
			fail(w, 400, err)
			return
		}
		id := r.PathValue("id")
		v, ok := a.Printers.Get(id)
		raw, _ := a.Printers.Raw(id)
		if !ok {
			fail(w, 404, fmt.Errorf("printer not found"))
			return
		}
		// Printers with their own controls (FlashBridge) get the request as is.
		if d, err := a.Printers.Driver(id); err == nil {
			if c, ok := d.(printers.Controller); ok {
				args := map[string]any{"heater": req.Heater, "value": req.Value, "distance": req.Distance}
				if req.Internal != nil {
					args["internal"] = *req.Internal
				}
				if req.External != nil {
					args["external"] = *req.External
				}
				err := c.Control(r.Context(), req.Op, args)
				if err == nil {
					a.Printers.PollOnce(r.Context())
					writeJSON(w, 200, map[string]bool{"ok": true})
					return
				}
				if err != printers.ErrUnsupported {
					fail(w, 400, err)
					return
				}
				// not one of its own controls: fall through to G-code
			}
		}
		g, err := controlGcode(req, v, raw.Driver)
		if err != nil {
			fail(w, 400, err)
			return
		}
		if err := a.Printers.Gcode(r.Context(), id, g); err != nil {
			fail(w, 400, err)
			return
		}
		a.Printers.PollOnce(r.Context())
		writeJSON(w, 200, map[string]string{"sent": g})
	})
	mux.HandleFunc("GET /api/printers/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		fm, err := a.Printers.FileManager(r.PathValue("id"))
		if err != nil {
			fail(w, 400, err)
			return
		}
		files, err := fm.Files(r.Context())
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, files)
	})
	mux.HandleFunc("POST /api/printers/{id}/files/start", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string `json:"name"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		id := r.PathValue("id")
		if v, _ := a.Printers.Get(id); !v.Ready {
			fail(w, 400, fmt.Errorf("the printer isn't ready (it's busy, or the bed hasn't been marked clear)"))
			return
		}
		fm, err := a.Printers.FileManager(id)
		if err == nil {
			err = fm.StartFile(r.Context(), in.Name)
		}
		if err != nil {
			fail(w, 400, err)
			return
		}
		_ = a.Printers.SetBedClear(id, false)
		a.Printers.PollOnce(r.Context())
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("DELETE /api/printers/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		fm, err := a.Printers.FileManager(r.PathValue("id"))
		if err == nil {
			err = fm.DeleteFile(r.Context(), r.URL.Query().Get("name"))
		}
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// Upload a sliced file straight to the printer (the "Print a file" sheet).
	mux.HandleFunc("POST /api/printers/{id}/upload", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			fail(w, 400, err)
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			fail(w, 400, fmt.Errorf("choose a file"))
			return
		}
		defer f.Close()
		start := r.FormValue("start") == "1"
		if start {
			if v, _ := a.Printers.Get(id); !v.Ready {
				fail(w, 400, fmt.Errorf("the printer isn't ready (it's busy, or the bed hasn't been marked clear)"))
				return
			}
		}
		name := filepath.Base(hdr.Filename)
		if err := a.Printers.Send(r.Context(), id, name, f, start); err != nil {
			fail(w, 400, err)
			return
		}
		if start {
			_ = a.Printers.SetBedClear(id, false)
		}
		a.Printers.PollOnce(r.Context())
		writeJSON(w, 200, map[string]any{"ok": true, "name": name})
	})
	// Per-printer switches from the Controls card.
	mux.HandleFunc("POST /api/printers/{id}/option", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			AutoClear *bool            `json:"auto_clear"`
			ClearFin  *bool            `json:"clear_finished"`
			AISens    *string          `json:"ai_sensitivity"`
			AIIntv    *int             `json:"ai_interval"`
			AIDetect  *bool            `json:"ai_detect"`
			AIPause   *bool            `json:"ai_pause"`
			Slots     *[]printers.Slot `json:"slots"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		err := a.Printers.Tweak(r.PathValue("id"), func(p *printers.Printer) {
			if in.AutoClear != nil {
				p.AutoClear = *in.AutoClear
			}
			if in.ClearFin != nil {
				p.ClearFinished = *in.ClearFin
			}
			if in.AISens != nil {
				switch *in.AISens {
				case "low", "medium", "high":
					p.AISensitivity = *in.AISens
				}
			}
			if in.AIIntv != nil {
				p.AIInterval = max(5, min(120, *in.AIIntv))
			}
			if in.AIDetect != nil {
				p.AIDetect = *in.AIDetect
			}
			if in.AIPause != nil {
				p.AIPause = *in.AIPause
			}
			if in.Slots != nil {
				p.Slots = *in.Slots
			}
		})
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/restart", func(w http.ResponseWriter, r *http.Request) {
		if a.Restart == nil {
			fail(w, 400, fmt.Errorf("restarting from the web page isn't available here; restart the service instead"))
			return
		}
		busy := 0
		for _, j := range a.Queue.List() {
			if j.State == slicer.Slicing || j.State == slicer.Sending {
				busy++
			}
		}
		if busy > 0 && r.URL.Query().Get("force") != "1" {
			fail(w, 409, fmt.Errorf("a plate is being sliced right now; restarting stops it"))
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		go func() {
			time.Sleep(300 * time.Millisecond) // let the answer reach the browser
			a.Restart()
		}()
	})
	// Which IP address a printer's name (e.g. "creator5.local") points to.
	// Browsers on phones and some PCs can't look up .local names.
	mux.HandleFunc("GET /api/printers/{id}/where", func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.Printers.Raw(r.PathValue("id"))
		if !ok {
			fail(w, 404, fmt.Errorf("printer not found"))
			return
		}
		host := p.Address
		if u, err := url.Parse(p.Address); err == nil && u.Host != "" {
			host = u.Hostname()
		} else if h, _, err := net.SplitHostPort(p.Address); err == nil {
			host = h
		}
		ips, err := net.DefaultResolver.LookupHost(r.Context(), host)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{"host": host, "ips": ips})
	})
	// Change what a filament slot holds, on printers that keep track of it.
	mux.HandleFunc("POST /api/printers/{id}/slot", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Slot     int    `json:"slot"`
			Material string `json:"material"`
			Color    string `json:"color"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		d, err := a.Printers.Driver(r.PathValue("id"))
		if err == nil {
			if ss, ok := d.(printers.SlotSetter); ok {
				err = ss.SetSlot(r.Context(), in.Slot, in.Material, in.Color)
			} else {
				err = printers.ErrUnsupported
			}
		}
		if err != nil {
			fail(w, 400, err)
			return
		}
		a.Printers.PollOnce(r.Context())
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
}
