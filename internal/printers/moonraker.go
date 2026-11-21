package printers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Moonraker covers every Klipper printer running Moonraker: Voron, Creality
// K-series, Elegoo Neptune 4, Sovol, Qidi and many more.
func init() {
	Register(DriverInfo{Name: "moonraker", Label: "Klipper (Moonraker)", AddressHint: "http://192.168.1.50:7125",
		NeedsKey: false, KeyLabel: "API key (only if Moonraker requires one)"},
		func(p Printer) (Driver, error) { return NewMoonraker(p.Address, p.APIKey) })
}

type Moonraker struct {
	base   *url.URL
	key    string
	client *http.Client

	camMu      sync.Mutex
	camChecked time.Time
	camSnap    string // snapshot path/URL as Moonraker reports it
	camStream  string
	camWorking string    // snapshot URL that last worked
	camMonitor bool      // Snapmaker-style camera: needs camera.start_monitor keepalives
	camKick    time.Time // last keepalive sent
	camFailed  time.Time // last time every candidate failed

	objMu      sync.Mutex
	objChecked time.Time
	tools      []string // extruder, extruder1, ...
	chamber    string   // "heater_generic chamber" or "temperature_sensor chamber"
	lightPin   string   // "output_pin caselight" etc.
	taskConfig bool     // Snapmaker U1: print_task_config holds the loaded filament
	slotInfo   []mrSlot // last read, for SetSlot
}

// mrSlot is one Snapmaker U1 toolhead's filament (print_task_config).
type mrSlot struct{ vendor, typ, sub string }

func NewMoonraker(address, key string) (*Moonraker, error) {
	a := strings.TrimSpace(address)
	if a == "" {
		return nil, fmt.Errorf("address is required")
	}
	if !strings.Contains(a, "://") {
		a = "http://" + a
	}
	u, err := url.Parse(strings.TrimRight(a, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad address %q", address)
	}
	if u.Port() == "" {
		u.Host += ":7125"
	}
	return &Moonraker{base: u, key: key, client: &http.Client{Timeout: 10 * time.Minute}}, nil
}

func (m *Moonraker) Capabilities() Capabilities {
	m.objMu.Lock()
	light := m.lightPin != ""
	slots := m.taskConfig
	m.objMu.Unlock()
	return Capabilities{Status: true, Pause: true, Resume: true, Cancel: true, Upload: true, StartPrint: true,
		Temperatures: true, Camera: true, Gcode: true, Console: true, Files: true, SpeedFactor: true, ZOffset: true, Jog: true,
		Light: light, FilamentSlots: slots, SetSlots: slots}
}

// objects looks up which heaters and lights this printer has (every few
// minutes; it only changes when the Klipper config does).
func (m *Moonraker) objects(ctx context.Context) {
	m.objMu.Lock()
	fresh := !m.objChecked.IsZero() && time.Since(m.objChecked) < 5*time.Minute
	m.objMu.Unlock()
	if fresh {
		return
	}
	var r struct {
		Result struct {
			Objects []string `json:"objects"`
		} `json:"result"`
	}
	if err := m.do(ctx, "GET", "/printer/objects/list", nil, "", &r); err != nil {
		return
	}
	tools, chamber, light := klipperObjects(r.Result.Objects)
	task := false
	for _, o := range r.Result.Objects {
		if o == "print_task_config" {
			task = true
		}
	}
	m.objMu.Lock()
	m.tools, m.chamber, m.lightPin, m.objChecked, m.taskConfig = tools, chamber, light, time.Now(), task
	m.objMu.Unlock()
}

// klipperObjects picks the nozzles, chamber and light out of Klipper's
// object list.
func klipperObjects(objs []string) (tools []string, chamber, light string) {
	has := map[string]bool{}
	for _, o := range objs {
		has[o] = true
	}
	if has["extruder"] {
		tools = append(tools, "extruder")
	}
	for i := 1; i < 16; i++ {
		if n := fmt.Sprintf("extruder%d", i); has[n] {
			tools = append(tools, n)
		}
	}
	for _, o := range objs {
		lo := strings.ToLower(o)
		switch {
		case strings.HasPrefix(lo, "heater_generic ") && strings.Contains(lo, "chamber"):
			chamber = o
		case chamber == "" && strings.HasPrefix(lo, "temperature_sensor ") && strings.Contains(lo, "chamber"):
			chamber = o
		case light == "" && strings.HasPrefix(lo, "output_pin ") && (strings.Contains(lo, "light") || strings.Contains(lo, "led") || strings.Contains(lo, "lamp")):
			light = o
		}
	}
	return
}

// SetLight switches the printer's light (a Klipper output_pin).
func (m *Moonraker) SetLight(ctx context.Context, on bool) error {
	m.objMu.Lock()
	pin := m.lightPin
	m.objMu.Unlock()
	if pin == "" {
		return ErrUnsupported
	}
	v := 0
	if on {
		v = 1
	}
	return m.SendGcode(ctx, fmt.Sprintf("SET_PIN PIN=%s VALUE=%d", strings.TrimPrefix(pin, "output_pin "), v))
}

func (m *Moonraker) OutputFormat() string { return "gcode" }

func (m *Moonraker) do(ctx context.Context, method, path string, body io.Reader, ctype string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, m.base.String()+path, body)
	if err != nil {
		return err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if m.key != "" {
		req.Header.Set("X-Api-Key", m.key)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("moonraker %s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type mrHeater struct {
	Temperature float64 `json:"temperature"`
	Target      float64 `json:"target"`
}

type mrQuery struct {
	Result struct {
		Status struct {
			Webhooks struct {
				State        string `json:"state"`
				StateMessage string `json:"state_message"`
			} `json:"webhooks"`
			PrintStats struct {
				State         string  `json:"state"`
				Filename      string  `json:"filename"`
				PrintDuration float64 `json:"print_duration"`
				TotalDuration float64 `json:"total_duration"`
				Message       string  `json:"message"`
				Info          struct {
					CurrentLayer int `json:"current_layer"`
					TotalLayer   int `json:"total_layer"`
				} `json:"info"`
			} `json:"print_stats"`
			Toolhead struct {
				Position  []float64 `json:"position"`
				HomedAxes string    `json:"homed_axes"`
			} `json:"toolhead"`
			GcodeMove struct {
				SpeedFactor   float64   `json:"speed_factor"`
				ExtrudeFactor float64   `json:"extrude_factor"`
				HomingOrigin  []float64 `json:"homing_origin"`
			} `json:"gcode_move"`
			Fan struct {
				Speed float64 `json:"speed"`
			} `json:"fan"`
			VirtualSD struct {
				Progress float64 `json:"progress"`
			} `json:"virtual_sdcard"`
			Extruder  mrHeater `json:"extruder"`
			HeaterBed mrHeater `json:"heater_bed"`
		} `json:"status"`
	} `json:"result"`
}

func (m *Moonraker) Status(ctx context.Context) (Status, error) {
	m.objects(ctx)
	m.objMu.Lock()
	tools, chamber, light, task := m.tools, m.chamber, m.lightPin, m.taskConfig
	m.objMu.Unlock()
	path := "/printer/objects/query?webhooks&print_stats&virtual_sdcard&extruder&heater_bed&toolhead&gcode_move&fan"
	objs := append(append([]string{}, tools...), chamber, light)
	if task {
		objs = append(objs, "print_task_config")
	}
	for _, o := range objs {
		if o != "" && o != "extruder" {
			path += "&" + url.QueryEscape(o)
		}
	}
	var raw json.RawMessage
	if err := m.do(ctx, "GET", path, nil, "", &raw); err != nil {
		return Status{State: StateOffline}, err
	}
	var q mrQuery
	_ = json.Unmarshal(raw, &q)
	var extra struct {
		Result struct {
			Status map[string]struct {
				Temperature *float64 `json:"temperature"`
				Target      *float64 `json:"target"`
				Value       *float64 `json:"value"`
			} `json:"status"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &extra)
	s := q.Result.Status
	var u1 struct {
		Result struct {
			Status struct {
				PTC *struct {
					Vendor []string `json:"filament_vendor"`
					Type   []string `json:"filament_type"`
					Sub    []string `json:"filament_sub_type"`
					RGBA   []string `json:"filament_color_rgba"`
					Exist  []bool   `json:"filament_exist"`
				} `json:"print_task_config"`
			} `json:"status"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &u1)
	st := Status{
		JobName:      s.PrintStats.Filename,
		Progress:     s.VirtualSD.Progress,
		Nozzle:       s.Extruder.Temperature,
		NozzleTarget: s.Extruder.Target,
		Bed:          s.HeaterBed.Temperature,
		BedTarget:    s.HeaterBed.Target,
		Message:      s.PrintStats.Message,
		Homed:        s.Toolhead.HomedAxes,
		Fan:          s.Fan.Speed,
		SpeedFactor:  s.GcodeMove.SpeedFactor,
		FlowFactor:   s.GcodeMove.ExtrudeFactor,
		Layer:        s.PrintStats.Info.CurrentLayer,
		TotalLayers:  s.PrintStats.Info.TotalLayer,
		ElapsedSec:   int(s.PrintStats.TotalDuration),
	}
	if len(s.Toolhead.Position) >= 3 {
		st.Position = s.Toolhead.Position[:3]
	}
	f := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}
	if len(tools) > 1 {
		for _, t := range tools {
			o := extra.Result.Status[t]
			st.Tools = append(st.Tools, ToolTemp{Temp: f(o.Temperature), Target: f(o.Target)})
		}
	}
	if chamber != "" {
		o := extra.Result.Status[chamber]
		st.Chamber = f(o.Temperature)
		if strings.HasPrefix(chamber, "heater_generic ") {
			st.ChamberTarget = f(o.Target)
			st.ChamberHeater = strings.TrimPrefix(chamber, "heater_generic ")
		}
	}
	if p := u1.Result.Status.PTC; p != nil {
		// Snapmaker U1: what each toolhead holds, as Snapmaker Orca reads it.
		at := func(l []string, i int) string {
			if i < len(l) {
				return l[i]
			}
			return ""
		}
		var info []mrSlot
		for i := 0; i < len(p.Type); i++ {
			typ, rgba := at(p.Type, i), at(p.RGBA, i)
			exist := i >= len(p.Exist) || p.Exist[i]
			sl := Slot{Index: i + 1}
			if typ != "" && !strings.EqualFold(typ, "NONE") && exist {
				sl.Type = typ
				if sub := at(p.Sub, i); sub != "" && !strings.EqualFold(sub, "NONE") && !strings.EqualFold(sub, "Basic") {
					sl.Name = sub
				}
				if len(rgba) >= 6 {
					sl.Color = "#" + strings.ToUpper(rgba[:6])
				}
			}
			st.Slots = append(st.Slots, sl)
			info = append(info, mrSlot{vendor: at(p.Vendor, i), typ: typ, sub: at(p.Sub, i)})
		}
		m.objMu.Lock()
		m.slotInfo = info
		m.objMu.Unlock()
	}
	if light != "" {
		if f(extra.Result.Status[light].Value) > 0 {
			st.Light = "on"
		} else {
			st.Light = "off"
		}
	}
	if len(s.GcodeMove.HomingOrigin) >= 3 {
		st.ZOffset = s.GcodeMove.HomingOrigin[2]
	}
	if s.Webhooks.State != "" && s.Webhooks.State != "ready" {
		st.State, st.Message = StateError, s.Webhooks.StateMessage
		return st, nil
	}
	switch s.PrintStats.State {
	case "printing":
		st.State = StatePrinting
		if st.Progress > 0.01 {
			total := s.PrintStats.PrintDuration / st.Progress
			st.RemainingSec = int(total - s.PrintStats.PrintDuration)
		}
	case "paused":
		st.State = StatePaused
	case "complete":
		st.State = StateFinished
	case "error":
		st.State = StateError
	case "cancelled":
		st.State, st.Stopped = StateIdle, true
	default: // standby
		st.State = StateIdle
	}
	m.addCamera(ctx, &st)
	return st, nil
}

// addCamera looks up the printer's webcam (cached for a minute: it rarely
// changes and we poll every few seconds).
func (m *Moonraker) addCamera(ctx context.Context, st *Status) {
	m.camMu.Lock()
	fresh := time.Since(m.camChecked) < time.Minute
	m.camMu.Unlock()
	if !fresh {
		var w struct {
			Result struct {
				Webcams []struct {
					Enabled     bool   `json:"enabled"`
					StreamURL   string `json:"stream_url"`
					SnapshotURL string `json:"snapshot_url"`
				} `json:"webcams"`
			} `json:"result"`
		}
		snap, stream := "", ""
		if m.do(ctx, "GET", "/server/webcams/list", nil, "", &w) == nil {
			for _, c := range w.Result.Webcams {
				if c.Enabled {
					snap, stream = c.SnapshotURL, c.StreamURL
					break
				}
			}
		}
		if snap == "" && strings.Contains(stream, "action=stream") {
			snap = strings.Replace(stream, "action=stream", "action=snapshot", 1)
		}
		monitor := false
		if snap == "" && m.hasMonitorCamera(ctx) {
			// Snapmaker U1 stock firmware: no registered webcam, but the
			// printer writes frames to a file while it's kept awake.
			snap, monitor = snapmakerMonitorPath, true
		}
		m.camMu.Lock()
		if snap != m.camSnap {
			m.camWorking, m.camFailed = "", time.Time{}
		}
		m.camSnap, m.camStream, m.camChecked, m.camMonitor = snap, stream, time.Now(), monitor
		m.camMu.Unlock()
	}
	m.camMu.Lock()
	defer m.camMu.Unlock()
	if m.camSnap != "" {
		st.SnapshotURL = m.camSnap
		st.CameraURL = m.camStream
	}
}

const snapmakerMonitorPath = "/server/files/camera/monitor.jpg"

// hasMonitorCamera reports whether the printer supports Snapmaker's
// camera.start_monitor call (open-source U1 firmware). Calling it also
// wakes the camera so monitor.jpg starts updating.
func (m *Moonraker) hasMonitorCamera(ctx context.Context) bool {
	if m.startMonitor(ctx) == nil {
		return true
	}
	// Some firmware versions don't answer at all; the snapshot file existing
	// is proof enough.
	_, _, err := fetchImage(ctx, m.client, m.base.String()+snapmakerMonitorPath)
	return err == nil
}

// kickMonitor keeps a Snapmaker camera awake; it stops refreshing its
// snapshot unless poked every ~10 s. Only runs while someone is viewing.
func (m *Moonraker) kickMonitor(ctx context.Context) {
	m.camMu.Lock()
	due := time.Since(m.camKick) > 8*time.Second
	if due {
		m.camKick = time.Now()
	}
	m.camMu.Unlock()
	if due {
		// In the background: slow or silent firmware shouldn't delay the frame.
		go func() { _ = m.startMonitor(context.Background()) }()
	}
}

// startMonitor sends camera.start_monitor {domain: lan, interval: 0}. The
// U1 only accepts it over Moonraker's websocket ("Method not found for
// transport HTTP" otherwise), so that's what we use.
func (m *Moonraker) startMonitor(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := wsCall(cctx, m.base, "camera.start_monitor", map[string]any{"domain": "lan", "interval": 0})
	if err == errNoReply {
		return nil // delivered; older firmware just doesn't answer
	}
	return err
}

// snapshotCandidates lists where a relative webcam path might be served:
// the web UI on port 80 (normal Klipper setups), Moonraker's own port
// (bridges like FlashBridge), then 8080 (stand-alone camera streamers).
func (m *Moonraker) snapshotCandidates(p string) []string {
	if strings.Contains(p, "://") {
		return []string{p}
	}
	path := "/" + strings.TrimLeft(p, "/")
	host := m.base.Hostname()
	return []string{
		m.base.Scheme + "://" + host + path,
		m.base.String() + path,
		m.base.Scheme + "://" + host + ":8080" + path,
	}
}

// Snapshot fetches one camera frame. Failing cameras are retried at most
// every 30 seconds so a dead camera doesn't slow everything down.
func (m *Moonraker) Snapshot(ctx context.Context) ([]byte, string, error) {
	m.camMu.Lock()
	snap, working, failed := m.camSnap, m.camWorking, m.camFailed
	m.camMu.Unlock()
	if snap == "" {
		return nil, "", ErrUnsupported
	}
	if working == "" && time.Since(failed) < 30*time.Second {
		return nil, "", fmt.Errorf("camera not reachable")
	}
	if m.camMonitor {
		m.kickMonitor(ctx)
	}
	cands := m.snapshotCandidates(snap)
	if working != "" {
		cands = append([]string{working}, cands...)
	}
	tries := 1
	if m.camMonitor {
		// A freshly woken Snapmaker camera needs a moment to write its first frame.
		tries = 4
	}
	for t := 0; t < tries; t++ {
		if t > 0 {
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-time.After(time.Second):
			}
		}
		for _, u := range cands {
			b, ct, err := m.fetchImage(ctx, u)
			if err == nil {
				m.camMu.Lock()
				m.camWorking = u
				m.camMu.Unlock()
				return b, ct, nil
			}
		}
	}
	m.camMu.Lock()
	m.camWorking, m.camFailed = "", time.Now()
	m.camMu.Unlock()
	return nil, "", fmt.Errorf("camera not reachable")
}

func (m *Moonraker) fetchImage(ctx context.Context, u string) ([]byte, string, error) {
	return fetchImage(ctx, m.client, u)
}

// fetchImage downloads one camera frame and checks it really is an image.
func fetchImage(ctx context.Context, client *http.Client, u string) ([]byte, string, error) {
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", u, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode != 200 || !strings.HasPrefix(ct, "image/") {
		return nil, "", fmt.Errorf("%s: %s %s", u, resp.Status, ct)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	return b, ct, err
}

func (m *Moonraker) Pause(ctx context.Context) error {
	return m.do(ctx, "POST", "/printer/print/pause", nil, "", nil)
}
func (m *Moonraker) Resume(ctx context.Context) error {
	return m.do(ctx, "POST", "/printer/print/resume", nil, "", nil)
}
func (m *Moonraker) Cancel(ctx context.Context) error {
	return m.do(ctx, "POST", "/printer/print/cancel", nil, "", nil)
}

func (m *Moonraker) Upload(ctx context.Context, name string, r io.Reader, start bool) error {
	// Build the whole multipart body into a temp file first, so the upload is
	// sent with a real Content-Length instead of chunked transfer encoding.
	// Some Moonraker stacks — notably the Snapmaker U1's — reject a chunked
	// upload and drop the connection mid-send ("connection forcibly closed").
	tmp, err := os.CreateTemp("", "mr-upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	mw := multipart.NewWriter(tmp)
	_ = mw.WriteField("root", "gcodes")
	if start {
		_ = mw.WriteField("print", "true")
	}
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		return err
	}
	if _, err := io.Copy(fw, r); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", m.base.String()+"/server/files/upload", tmp)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.ContentLength = size
	if m.key != "" {
		req.Header.Set("X-Api-Key", m.key)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("moonraker upload: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// --- control page ---

func (m *Moonraker) SendGcode(ctx context.Context, script string) error {
	return m.do(ctx, "POST", "/printer/gcode/script?script="+url.QueryEscape(script), nil, "", nil)
}

func (m *Moonraker) Console(ctx context.Context) ([]ConsoleLine, error) {
	var r struct {
		Result struct {
			Store []ConsoleLine `json:"gcode_store"`
		} `json:"result"`
	}
	if err := m.do(ctx, "GET", "/server/gcode_store?count=150", nil, "", &r); err != nil {
		return nil, err
	}
	return r.Result.Store, nil
}

func (m *Moonraker) Files(ctx context.Context) ([]PrinterFile, error) {
	var r struct {
		Result []struct {
			Path     string  `json:"path"`
			Size     int64   `json:"size"`
			Modified float64 `json:"modified"`
		} `json:"result"`
	}
	if err := m.do(ctx, "GET", "/server/files/list?root=gcodes", nil, "", &r); err != nil {
		return nil, err
	}
	out := []PrinterFile{}
	for _, f := range r.Result {
		out = append(out, PrinterFile{Name: f.Path, Size: f.Size, Modified: f.Modified})
	}
	return out, nil
}

func (m *Moonraker) StartFile(ctx context.Context, name string) error {
	return m.do(ctx, "POST", "/printer/print/start?filename="+url.QueryEscape(name), nil, "", nil)
}

func (m *Moonraker) DeleteFile(ctx context.Context, name string) error {
	return m.do(ctx, "DELETE", "/server/files/gcodes/"+escapePath(name), nil, "", nil)
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// HasStream: mjpeg-streamer style cameras; not Snapmaker's snapshot camera.
func (m *Moonraker) HasStream() bool {
	m.camMu.Lock()
	defer m.camMu.Unlock()
	return m.camStream != "" && !m.camMonitor
}

// Stream opens the printer's live MJPEG camera (mjpeg-streamer / crowsnest
// "?action=stream"). Cameras that only have snapshots (Snapmaker U1) return
// ErrUnsupported; PrintHarbor then streams their snapshots instead.
func (m *Moonraker) Stream(ctx context.Context) (*http.Response, error) {
	m.camMu.Lock()
	stream, working := m.camStream, m.camWorking
	m.camMu.Unlock()
	if stream == "" || m.camMonitor {
		return nil, ErrUnsupported
	}
	cands := m.snapshotCandidates(stream)
	// Try the host that served the last good snapshot first.
	if working != "" {
		if wu, err := url.Parse(working); err == nil {
			if su, err := wu.Parse("/" + strings.TrimLeft(stream, "/")); err == nil && !strings.Contains(stream, "://") {
				cands = append([]string{su.String()}, cands...)
			}
		}
	}
	client := &http.Client{} // no timeout: the stream runs as long as someone watches
	for _, u := range cands {
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode == 200 && strings.HasPrefix(resp.Header.Get("Content-Type"), "multipart/") {
			return resp, nil
		}
		resp.Body.Close()
	}
	return nil, fmt.Errorf("camera stream not reachable")
}

// SetSlot changes a Snapmaker U1 toolhead's filament with the firmware's own
// SET_PRINT_FILAMENT_CONFIG (klippy/extras/print_task_config.py). FORCE=1
// lets it replace a Snapmaker RFID spool's details too.
func (m *Moonraker) SetSlot(ctx context.Context, slot int, material, color string) error {
	m.objMu.Lock()
	task, info := m.taskConfig, m.slotInfo
	m.objMu.Unlock()
	if !task {
		return ErrUnsupported
	}
	i := slot - 1
	if i < 0 || i > 15 {
		return fmt.Errorf("bad slot %d", slot)
	}
	clean := func(s, def string) string {
		s = strings.Map(func(r rune) rune {
			if r == ' ' || r == '#' || r == ';' || r == '*' || r == '=' || r < 32 {
				return -1
			}
			return r
		}, strings.TrimSpace(s))
		if s == "" || strings.EqualFold(s, "NONE") {
			return def
		}
		return s
	}
	vendor, sub := "Generic", "Basic"
	if i < len(info) {
		// keep the vendor and sub-type when only the colour or type changes
		if info[i].typ == material {
			vendor, sub = clean(info[i].vendor, "Generic"), clean(info[i].sub, "Basic")
		}
	}
	rgb := strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(color), "#"))
	if len(rgb) != 6 {
		return fmt.Errorf("bad colour %q", color)
	}
	if _, err := strconv.ParseUint(rgb, 16, 32); err != nil {
		return fmt.Errorf("bad colour %q", color)
	}
	cmd := fmt.Sprintf("SET_PRINT_FILAMENT_CONFIG CONFIG_EXTRUDER=%d VENDOR=%s FILAMENT_TYPE=%s FILAMENT_SUBTYPE=%s FILAMENT_COLOR_RGBA=%sFF FORCE=1",
		i, vendor, clean(material, "PLA"), sub, rgb)
	return m.SendGcode(ctx, cmd)
}
