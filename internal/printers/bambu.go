package printers

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bambu Lab printers in LAN mode: status and control over MQTT (port 8883),
// file upload over implicit FTPS (990), camera on port 6000 (P1/A1) or RTSP
// (X1/H2, needs ffmpeg installed for snapshots).
//
// On current firmware, third-party control needs "LAN Only Mode" and
// "Developer Mode" enabled on the printer's screen. Address = printer IP,
// Serial = serial number, API key = the LAN access code.
func init() {
	Register(DriverInfo{Name: "bambu", Label: "Bambu Lab (LAN mode)", AddressHint: "192.168.1.60",
		NeedsKey: true, KeyLabel: "LAN access code", NeedsSerial: true,
		Help: "On the printer screen, turn on BOTH LAN Only Mode and Developer Mode (Settings → network/general). Developer Mode is what lets PrintHarbor control the printer, not just watch it. Address = the printer's IP; the access code and serial number are on the same settings screen. Then use Test connection."},
		func(p Printer) (Driver, error) { return NewBambu(p.Address, p.Serial, p.APIKey) })
}

type Bambu struct {
	host, serial, code         string
	mqttPort, ftpPort, camPort int
	tls                        *tls.Config

	mu        sync.Mutex
	state     map[string]any // merged "print" report
	connected bool
	lastErr   error
	client    *mqttClient
	started   bool
	stop      chan struct{}

	camMu   sync.Mutex
	camLast []byte
	camAt   time.Time
}

func NewBambu(host, serial, code string) (*Bambu, error) {
	host = strings.TrimSpace(host)
	if host == "" || strings.TrimSpace(serial) == "" || strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("Bambu printers need the IP address, serial number and LAN access code")
	}
	return &Bambu{host: host, serial: strings.TrimSpace(serial), code: strings.TrimSpace(code),
		mqttPort: 8883, ftpPort: 990, camPort: 6000,
		// The printer uses a self-signed certificate; this is LAN-only traffic.
		tls:   &tls.Config{InsecureSkipVerify: true, ServerName: strings.TrimSpace(serial)},
		state: map[string]any{}, stop: make(chan struct{})}, nil
}

// Probe checks a Bambu is reachable and will accept control: MQTT (status +
// commands) and FTPS (file upload). It reports, in plain words, which part
// failed and the likely cause, so setup isn't guesswork.
func (b *Bambu) Probe(ctx context.Context) error {
	addr := net.JoinHostPort(b.host, strconv.Itoa(b.mqttPort))
	c, _, err := mqttDial(addr, "printharbor-test", "bblp", b.code, b.tls, nil, func(string, []byte) {})
	if err != nil {
		if strings.Contains(err.Error(), "access code") {
			return fmt.Errorf("the printer refused the LAN access code. Check the code on the printer screen (Settings → LAN) — it changes when you toggle LAN Only Mode or update firmware")
		}
		return fmt.Errorf("couldn't reach the printer at %s. Check the IP address, that the printer is on, and that LAN Only Mode is on (Settings → network): %v", addr, err)
	}
	_ = c.Close()
	// FTPS on 990 is how sliced files get to the printer; a print can't start without it.
	s, err := ftpsLogin(net.JoinHostPort(b.host, strconv.Itoa(b.ftpPort)), "bblp", b.code, b.tls, 20*time.Second)
	if err != nil {
		return fmt.Errorf("status works, but sending files failed (FTPS on port %d): %v. On the newest firmware this usually means Developer Mode is off — turn it on on the printer screen", b.ftpPort, err)
	}
	s.Close()
	return nil
}

func (b *Bambu) Capabilities() Capabilities {
	return Capabilities{Status: true, Pause: true, Resume: true, Cancel: true, Upload: true, StartPrint: true,
		Temperatures: true, Camera: true, FilamentSlots: true, Light: true, Gcode: true, Jog: true,
		Files: true, SpeedFactor: true, SetSlots: true}
}

// Bambu printers take the sliced 3MF ("gcode.3mf"), not bare G-code.
func (b *Bambu) OutputFormat() string { return "gcode.3mf" }

func (b *Bambu) ensure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.started {
		b.started = true
		go b.loop()
	}
}

func (b *Bambu) loop() {
	backoff := time.Second
	for {
		select {
		case <-b.stop:
			return
		default:
		}
		c, done, err := mqttDial(net.JoinHostPort(b.host, strconv.Itoa(b.mqttPort)), "printharbor-"+strconv.FormatInt(time.Now().UnixNano()%1e6, 10),
			"bblp", b.code, b.tls, []string{"device/" + b.serial + "/report"}, b.onMessage)
		if err != nil {
			b.mu.Lock()
			b.connected, b.lastErr = false, err
			b.mu.Unlock()
			select {
			case <-b.stop:
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		b.mu.Lock()
		b.client, b.connected, b.lastErr = c, true, nil
		b.mu.Unlock()
		_ = b.publish(map[string]any{"pushing": map[string]any{"sequence_id": "0", "command": "pushall"}})
		// P1/A1 send only changes; ask for everything now and then, as
		// Bambu Studio and Home Assistant do.
		tick := time.NewTicker(5 * time.Minute)
		go func() {
			for range tick.C {
				if b.publish(map[string]any{"pushing": map[string]any{"sequence_id": "0", "command": "pushall"}}) != nil {
					return
				}
			}
		}()
		err = <-done
		tick.Stop()
		b.mu.Lock()
		b.connected, b.client = false, nil
		if err != nil {
			b.lastErr = err
		}
		b.mu.Unlock()
	}
}

func (b *Bambu) onMessage(topic string, payload []byte) {
	var msg map[string]any
	if json.Unmarshal(payload, &msg) != nil {
		return
	}
	p, ok := msg["print"].(map[string]any)
	if !ok {
		return
	}
	b.mu.Lock()
	deepMerge(b.state, p)
	b.mu.Unlock()
}

// deepMerge applies an incremental report: objects merge, everything else replaces.
func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				deepMerge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

func (b *Bambu) publish(v any) error {
	b.mu.Lock()
	c := b.client
	b.mu.Unlock()
	if c == nil {
		return fmt.Errorf("not connected to the printer")
	}
	j, _ := json.Marshal(v)
	return c.Publish("device/"+b.serial+"/request", j)
}

func num64(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return 0
}

func strv(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func (b *Bambu) Status(ctx context.Context) (Status, error) {
	b.ensure()
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.connected && len(b.state) == 0 {
		if b.lastErr != nil {
			return Status{State: StateOffline}, b.lastErr
		}
		return Status{State: StateOffline, Message: "connecting…"}, nil
	}
	s := b.state
	st := Status{
		JobName:      strv(s["subtask_name"]),
		Progress:     num64(s["mc_percent"]) / 100,
		RemainingSec: int(num64(s["mc_remaining_time"]) * 60),
		Nozzle:       num64(s["nozzle_temper"]),
		NozzleTarget: num64(s["nozzle_target_temper"]),
		Bed:          num64(s["bed_temper"]),
		BedTarget:    num64(s["bed_target_temper"]),
		Chamber:      num64(s["chamber_temper"]),
		SnapshotURL:  "bambu",
	}
	switch strings.ToUpper(strv(s["gcode_state"])) {
	case "RUNNING", "PREPARE", "SLICING":
		st.State = StatePrinting
	case "PAUSE":
		st.State = StatePaused
	case "FINISH":
		st.State = StateFinished
	case "FAILED":
		st.State, st.Message, st.Stopped = StateFinished, "last print failed or was stopped", true
	default:
		st.State = StateIdle
	}
	if !b.connected {
		st.State, st.Message = StateOffline, "reconnecting…"
	}
	if code := num64(s["print_error"]); code != 0 && st.State != StateIdle {
		st.Message = fmt.Sprintf("printer error %08X (see the printer screen)", int64(code))
	}
	// AMS trays → slots (index = ams*4 + tray + 1), external spool = 99.
	if ams, ok := s["ams"].(map[string]any); ok {
		units, _ := ams["ams"].([]any)
		for _, u := range units {
			um, _ := u.(map[string]any)
			uid := int(num64(um["id"]))
			trays, _ := um["tray"].([]any)
			for _, t := range trays {
				tm, _ := t.(map[string]any)
				sl := Slot{Index: uid*4 + int(num64(tm["id"])) + 1}
				if tt := strv(tm["tray_type"]); tt != "" {
					sl.Type, sl.Color, sl.Name = tt, bambuColor(strv(tm["tray_color"])), strv(tm["tray_sub_brands"])
				}
				st.Slots = append(st.Slots, sl)
			}
		}
	}
	if vt, ok := s["vt_tray"].(map[string]any); ok && strv(vt["tray_type"]) != "" {
		st.Slots = append(st.Slots, Slot{Index: 99, Type: strv(vt["tray_type"]), Color: bambuColor(strv(vt["tray_color"])), Name: "External spool"})
	}
	if ams, ok := s["ams"].(map[string]any); ok {
		switch now := int(num64(ams["tray_now"])); {
		case strv(ams["tray_now"]) == "":
		case now == 254:
			st.CurrentSlot = 99
		case now < 254:
			st.CurrentSlot = now + 1
		}
	}
	if f := num64(s["cooling_fan_speed"]); f > 0 {
		st.Fan = f / 15 // Bambu reports 0..15
	}
	st.Layer, st.TotalLayers = int(num64(s["layer_num"])), int(num64(s["total_layer_num"]))
	if lvl := int(num64(s["spd_lvl"])); lvl > 0 {
		st.SpeedFactor = map[int]float64{1: 0.5, 2: 1, 3: 1.24, 4: 1.66}[lvl]
	}
	if lights, ok := s["lights_report"].([]any); ok {
		for _, l := range lights {
			lm, _ := l.(map[string]any)
			if strv(lm["node"]) == "chamber_light" {
				st.Light = strv(lm["mode"])
			}
		}
	}
	return st, nil
}

func bambuColor(c string) string {
	if len(c) >= 6 {
		return "#" + strings.ToUpper(c[:6])
	}
	return ""
}

func (b *Bambu) command(cmd string) error {
	return b.publish(map[string]any{"print": map[string]any{"sequence_id": "0", "command": cmd, "param": ""}})
}

func (b *Bambu) Pause(ctx context.Context) error  { return b.command("pause") }
func (b *Bambu) Resume(ctx context.Context) error { return b.command("resume") }
func (b *Bambu) Cancel(ctx context.Context) error { return b.command("stop") }

func (b *Bambu) SetLight(ctx context.Context, on bool) error {
	mode := "off"
	if on {
		mode = "on"
	}
	return b.publish(map[string]any{"system": map[string]any{"sequence_id": "0", "command": "ledctrl", "led_node": "chamber_light",
		"led_mode": mode, "led_on_time": 500, "led_off_time": 500, "loop_times": 1, "interval_time": 1000}})
}

// Upload sends the sliced 3MF over FTPS and, if start is set, starts it with
// the AMS mapping chosen in PrintHarbor.
func (b *Bambu) Upload(ctx context.Context, name string, r io.Reader, start bool) error {
	tmp, err := os.CreateTemp("", "bambu-*.3mf")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	plate, err := gcodeInside(tmp.Name())
	if err != nil {
		return fmt.Errorf("Bambu printers need the sliced 3MF: %w", err)
	}
	f, err := os.Open(tmp.Name())
	if err != nil {
		return err
	}
	defer f.Close()
	if !strings.HasSuffix(strings.ToLower(name), ".3mf") {
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ".gcode.3mf"
	}
	if err := ftpsUpload(net.JoinHostPort(b.host, strconv.Itoa(b.ftpPort)), "bblp", b.code, b.tls, name, f, 10*time.Minute); err != nil {
		return fmt.Errorf("upload to printer: %w", err)
	}
	if !start {
		return nil
	}
	b.ensure()
	st, _ := b.Status(ctx)
	hasAMS := false
	for _, s := range st.Slots {
		hasAMS = hasAMS || s.Index != 99
	}
	cmd := map[string]any{
		"sequence_id": "0", "command": "project_file", "param": plate,
		"url": "file:///sdcard/" + name, "subtask_name": strings.TrimSuffix(strings.TrimSuffix(name, ".3mf"), ".gcode"),
		"md5": "", "timelapse": false, "bed_type": "auto", "bed_levelling": true, "bed_leveling": true,
		"flow_cali": false, "vibration_cali": false, "layer_inspect": false,
		"profile_id": "0", "project_id": "0", "subtask_id": "0", "task_id": "0",
	}
	if m := SlotMap(ctx); len(m) > 0 {
		var mapping []int
		usesAMS := false
		for _, slot := range m {
			switch {
			case slot == 99:
				mapping = append(mapping, 254)
			case slot > 0:
				mapping = append(mapping, slot-1)
				usesAMS = true
			default:
				mapping = append(mapping, 0)
				usesAMS = hasAMS
			}
		}
		cmd["ams_mapping"] = mapping
		cmd["use_ams"] = usesAMS
	} else {
		cmd["use_ams"] = hasAMS
	}
	return b.publish(map[string]any{"print": cmd})
}

// gcodeInside finds the plate G-code path inside a sliced 3MF.
func gcodeInside(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	for _, f := range zr.File {
		n := strings.TrimPrefix(f.Name, "/")
		if strings.HasPrefix(n, "Metadata/plate_") && strings.HasSuffix(n, ".gcode") {
			return n, nil
		}
	}
	return "", fmt.Errorf("no plate G-code found in the file")
}

// Snapshot grabs a camera frame: RTSP via ffmpeg on X1/H2-class printers,
// or the JPEG stream on port 6000 (P1/A1). Frames are cached for 2 s.
func (b *Bambu) Snapshot(ctx context.Context) ([]byte, string, error) {
	b.camMu.Lock()
	defer b.camMu.Unlock()
	if time.Since(b.camAt) < 2*time.Second && b.camLast != nil {
		return b.camLast, "image/jpeg", nil
	}
	b.mu.Lock()
	rtsp := ""
	if ip, ok := b.state["ipcam"].(map[string]any); ok {
		rtsp = strv(ip["rtsp_url"])
	}
	b.mu.Unlock()
	var img []byte
	var err error
	if strings.HasPrefix(rtsp, "rtsp") {
		img, err = b.rtspFrame(ctx, rtsp)
	} else {
		img, err = b.jpegFrame(ctx)
	}
	if err != nil {
		return nil, "", err
	}
	b.camLast, b.camAt = img, time.Now()
	return img, "image/jpeg", nil
}

func (b *Bambu) jpegFrame(ctx context.Context) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	d := &tls.Dialer{Config: b.tls}
	conn, err := d.DialContext(cctx, "tcp", net.JoinHostPort(b.host, strconv.Itoa(b.camPort)))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	dl, _ := cctx.Deadline()
	_ = conn.SetDeadline(dl)
	auth := make([]byte, 80)
	binary.LittleEndian.PutUint32(auth[0:], 0x40)
	binary.LittleEndian.PutUint32(auth[4:], 0x3000)
	copy(auth[16:48], "bblp")
	copy(auth[48:80], b.code)
	if _, err := conn.Write(auth); err != nil {
		return nil, err
	}
	hdr := make([]byte, 16)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(hdr)
	if n == 0 || n > 8<<20 {
		return nil, fmt.Errorf("bad camera frame")
	}
	img := make([]byte, n)
	if _, err := io.ReadFull(conn, img); err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(img, []byte{0xFF, 0xD8}) {
		return nil, fmt.Errorf("camera did not send a JPEG")
	}
	return img, nil
}

func (b *Bambu) rtspFrame(ctx context.Context, rtsp string) ([]byte, error) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("this printer streams RTSP; install ffmpeg to see its camera in PrintHarbor")
	}
	// rtsps://IP:322/streaming/live/1 → add credentials.
	u := strings.Replace(rtsp, "://", "://bblp:"+b.code+"@", 1)
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, ff, "-loglevel", "error", "-rtsp_transport", "tcp", "-i", u,
		"-frames:v", "1", "-f", "image2", "-vcodec", "mjpeg", "pipe:1").Output()
	if err != nil || len(out) < 100 {
		return nil, fmt.Errorf("couldn't read the camera stream")
	}
	return out, nil
}

// SendGcode runs G-code through the printer's gcode_line command
// (needs Developer Mode on current firmware).
func (b *Bambu) SendGcode(ctx context.Context, script string) error {
	return b.publish(map[string]any{"print": map[string]any{"sequence_id": "0", "command": "gcode_line", "param": strings.TrimSpace(script) + "\n"}})
}

// Control: Bambu's own speed levels; fans and temperatures as G-code with
// Bambu's fan numbers (P1 part, P2 aux, P3 chamber). Anything else falls
// back to PrintHarbor's G-code controls.
func (b *Bambu) Control(ctx context.Context, op string, args map[string]any) error {
	num := func(k string) float64 { v, _ := args[k].(float64); return v }
	switch op {
	case "speed":
		// silent 50 %, standard 100 %, sport 124 %, ludicrous 166 %
		v, lvl := num("value"), "2"
		switch {
		case v <= 75:
			lvl = "1"
		case v <= 112:
			lvl = "2"
		case v <= 140:
			lvl = "3"
		default:
			lvl = "4"
		}
		return b.publish(map[string]any{"print": map[string]any{"sequence_id": "0", "command": "print_speed", "param": lvl}})
	case "fan":
		return b.SendGcode(ctx, fmt.Sprintf("M106 P1 S%d", int(num("value")*255/100+0.5)))
	case "chamber_fan":
		return b.SendGcode(ctx, fmt.Sprintf("M106 P3 S%d", int(num("value")*255/100+0.5)))
	}
	return ErrUnsupported
}

// bambuFilamentIDs are Bambu's generic filament presets (tray_info_idx).
var bambuFilamentIDs = map[string]string{"PLA": "GFL99", "PETG": "GFG99", "ABS": "GFB99", "ASA": "GFB98",
	"TPU": "GFU99", "PA": "GFN99", "PC": "GFC99", "PVA": "GFS99", "PLA-CF": "GFL98", "PETG-CF": "GFG98", "PA-CF": "GFN98"}

// SetSlot tells the AMS (or external spool, slot 99) what's loaded, with
// the ams_filament_setting command Bambu Studio sends.
func (b *Bambu) SetSlot(ctx context.Context, slot int, material, color string) error {
	amsID, trayID := (slot-1)/4, (slot-1)%4
	if slot == 99 {
		amsID, trayID = 255, 254
	}
	rgb := strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(color), "#"))
	if len(rgb) != 6 {
		return fmt.Errorf("bad colour %q", color)
	}
	tmin, tmax := 190, 240
	switch strings.ToUpper(material) {
	case "PETG", "PETG-CF":
		tmin, tmax = 220, 270
	case "ABS", "ASA":
		tmin, tmax = 240, 280
	case "TPU":
		tmin, tmax = 200, 250
	case "PA", "PA-CF", "PC":
		tmin, tmax = 260, 300
	}
	return b.publish(map[string]any{"print": map[string]any{"sequence_id": "0", "command": "ams_filament_setting",
		"ams_id": amsID, "tray_id": trayID, "tray_color": rgb + "FF", "nozzle_temp_min": tmin, "nozzle_temp_max": tmax,
		"tray_type": strings.ToUpper(material), "tray_info_idx": bambuFilamentIDs[strings.ToUpper(material)], "setting_id": ""}})
}

func (b *Bambu) ftp() (*ftpsSession, error) {
	return ftpsLogin(net.JoinHostPort(b.host, strconv.Itoa(b.ftpPort)), "bblp", b.code, b.tls, 2*time.Minute)
}

// Files lists the printable files on the printer's storage.
func (b *Bambu) Files(ctx context.Context) ([]PrinterFile, error) {
	s, err := b.ftp()
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ents, err := s.List("/")
	if err != nil {
		return nil, err
	}
	out := []PrinterFile{}
	for _, e := range ents {
		n := strings.ToLower(e.Name)
		if !e.IsDir && (strings.HasSuffix(n, ".3mf") || strings.HasSuffix(n, ".gcode")) {
			out = append(out, PrinterFile{Name: e.Name, Size: e.Size})
		}
	}
	return out, nil
}

// StartFile prints a file already on the printer.
func (b *Bambu) StartFile(ctx context.Context, name string) error {
	b.ensure()
	if strings.HasSuffix(strings.ToLower(name), ".gcode") {
		return b.publish(map[string]any{"print": map[string]any{"sequence_id": "0", "command": "gcode_file", "param": "/sdcard/" + name}})
	}
	st, _ := b.Status(ctx)
	hasAMS := false
	for _, s := range st.Slots {
		hasAMS = hasAMS || (s.Index != 99 && s.Type != "")
	}
	return b.publish(map[string]any{"print": map[string]any{
		"sequence_id": "0", "command": "project_file", "param": "Metadata/plate_1.gcode",
		"url": "file:///sdcard/" + name, "subtask_name": strings.TrimSuffix(strings.TrimSuffix(name, ".3mf"), ".gcode"),
		"md5": "", "timelapse": false, "bed_type": "auto", "bed_levelling": true, "bed_leveling": true,
		"flow_cali": false, "vibration_cali": false, "layer_inspect": false, "use_ams": hasAMS,
		"profile_id": "0", "project_id": "0", "subtask_id": "0", "task_id": "0"}})
}

// DeleteFile removes a file from the printer's storage.
func (b *Bambu) DeleteFile(ctx context.Context, name string) error {
	s, err := b.ftp()
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Delete("/" + name)
}

// HasStream: every Bambu camera can be watched live.
func (b *Bambu) HasStream() bool { return true }

// Stream is the live camera as a stream of JPEGs: the port-6000 feed on
// P1/A1, or the RTSP feed through ffmpeg on X1/H2.
func (b *Bambu) Stream(ctx context.Context) (*http.Response, error) {
	b.mu.Lock()
	rtsp := ""
	if ip, ok := b.state["ipcam"].(map[string]any); ok {
		rtsp = strv(ip["rtsp_url"])
	}
	b.mu.Unlock()
	pr, pw := io.Pipe()
	if strings.HasPrefix(rtsp, "rtsp") {
		ff, err := exec.LookPath("ffmpeg")
		if err != nil {
			return nil, fmt.Errorf("this printer streams RTSP; install ffmpeg to see its camera in PrintHarbor")
		}
		u := strings.Replace(rtsp, "://", "://bblp:"+b.code+"@", 1)
		cmd := exec.CommandContext(ctx, ff, "-loglevel", "error", "-rtsp_transport", "tcp", "-i", u, "-an",
			"-f", "mjpeg", "-q:v", "5", "-r", "10", "pipe:1")
		cmd.Stdout = pw
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		go func() { pw.CloseWithError(cmd.Wait()) }()
	} else {
		cctx, cancel := context.WithCancel(ctx)
		d := &tls.Dialer{Config: b.tls}
		conn, err := d.DialContext(cctx, "tcp", net.JoinHostPort(b.host, strconv.Itoa(b.camPort)))
		if err != nil {
			cancel()
			return nil, err
		}
		auth := make([]byte, 80)
		binary.LittleEndian.PutUint32(auth[0:], 0x40)
		binary.LittleEndian.PutUint32(auth[4:], 0x3000)
		copy(auth[16:48], "bblp")
		copy(auth[48:80], b.code)
		if _, err := conn.Write(auth); err != nil {
			conn.Close()
			cancel()
			return nil, err
		}
		go func() {
			defer cancel()
			defer conn.Close()
			hdr := make([]byte, 16)
			for {
				_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
				if _, err := io.ReadFull(conn, hdr); err != nil {
					pw.CloseWithError(err)
					return
				}
				n := binary.LittleEndian.Uint32(hdr)
				if n == 0 || n > 8<<20 {
					pw.CloseWithError(fmt.Errorf("bad camera frame"))
					return
				}
				img := make([]byte, n)
				if _, err := io.ReadFull(conn, img); err != nil {
					pw.CloseWithError(err)
					return
				}
				if _, err := pw.Write(img); err != nil {
					return
				}
			}
		}()
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"image/jpeg"}}, Body: pr}, nil
}
