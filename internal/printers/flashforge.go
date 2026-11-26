package printers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FlashForge printers with the "LAN mode" HTTP API on port 8898: Adventurer
// 5M / 5M Pro, AD5X, Creator 5 and Creator 5 Pro. PrintHarbor talks to the
// printer directly, the same way FlashBridge's FlashForge client does
// (android-tv-app/source/lib/core/ff/ff_client.dart): every nozzle, the
// material station's slots and colours, light, filtration, fans, speed,
// Z-offset and Clear platform. Address = printer IP, Serial = serial number,
// key = the pairing (check) code the printer shows in LAN-only mode.
func init() {
	Register(DriverInfo{Name: "flashforge", Label: "FlashForge (LAN mode)", AddressHint: "192.168.1.70",
		NeedsKey: true, KeyLabel: "Pairing code (printer → Settings → Network → LAN-only mode)", NeedsSerial: true,
		Help: "Turn on LAN-only mode on the printer; it shows the pairing code. The serial number is under Settings → About."},
		func(p Printer) (Driver, error) { return NewFlashForge(p.Address, p.Serial, p.APIKey) })
}

type FlashForge struct {
	base         *url.URL
	serial, code string
	client       *http.Client
	mu           sync.Mutex
	camURL       string
	last         ffDetail
	lastAt       time.Time
	thumb        string
	creator5     bool
	camOpened    time.Time
}

// FFExtra is what FlashForge printers report beyond the common status, for
// the printer page's FlashForge controls.
type FFExtra struct {
	HasFiltration      bool       `json:"has_filtration"`
	FiltrationInternal bool       `json:"filtration_internal"`
	FiltrationExternal bool       `json:"filtration_external"`
	ChamberFan         float64    `json:"chamber_fan"` // 0..100
	CurrentSlot        int        `json:"current_slot"`
	SlotPalette        [][]string `json:"slot_palette,omitempty"`
	Materials          []string   `json:"materials,omitempty"`
	HasThumb           bool       `json:"has_thumb"`
	Door               string     `json:"door,omitempty"`
}

// The firmware only accepts these exact slot colours (FlashBridge models.dart).
var ffCreator5Palette = [][]string{{"White", "#FFFFFF"}, {"Yellow", "#FFF245"}, {"Light Green", "#DEF578"}, {"Green", "#21CC3D"},
	{"Dark Green", "#167A4B"}, {"Teal", "#156682"}, {"Cyan", "#24E4A0"}, {"Light Blue", "#7BD9F0"},
	{"Blue", "#4CAAF8"}, {"Dark Blue", "#2E54DD"}, {"Purple", "#48358C"}, {"Violet", "#A341F7"},
	{"Magenta", "#F435F6"}, {"Pink", "#D5B4DE"}, {"Coral", "#FA6173"}, {"Red", "#F82D29"},
	{"Brown", "#805003"}, {"Orange", "#F9903B"}, {"Cream", "#FCEBD7"}, {"Tan", "#D5C5A1"},
	{"Dark Brown", "#B17C38"}, {"Grey", "#8C8C89"}, {"Light Grey", "#BEBEBE"}, {"Black", "#1B1B1B"}}
var ffAD5XPalette = [][]string{{"White", "#FFFFFF"}, {"Yellow", "#FEF043"}, {"Light Green", "#DCF478"}, {"Green", "#0ACC38"},
	{"Dark Green", "#067749"}, {"Teal", "#0C6283"}, {"Cyan", "#0DE2A0"}, {"Light Blue", "#75D9F3"},
	{"Blue", "#45A8F9"}, {"Dark Blue", "#2750E0"}, {"Purple", "#46328E"}, {"Violet", "#A03CF7"},
	{"Magenta", "#F330F9"}, {"Pink", "#D4B0DC"}, {"Coral", "#F95D73"}, {"Red", "#F72224"},
	{"Brown", "#7C4B00"}, {"Orange", "#F98D33"}, {"Cream", "#FDEBD5"}, {"Tan", "#D3C4A3"},
	{"Dark Brown", "#AF7836"}, {"Grey", "#898989"}, {"Light Grey", "#BCBCBC"}, {"Black", "#161616"}}
var ffMaterials = []string{"PLA", "PLA-CF", "PETG", "PETG-CF", "ABS", "ASA", "TPU", "SILK", "PLA+", "PA", "PA-CF", "PC", "HIPS", "PVA"}

const (
	ffNoChange  = -200 // temperatureCtl_cmd: leave unchanged
	ffOff       = -100 // scalar heater off
	ffNozzleOff = 0    // nozzle array: off
)

// ffNum, ffStr and ffBool read a field whatever type the firmware sends it
// as (0, 0.0, "0", null...), like FlashBridge's _d/_i/_s helpers in models.dart.
type ffNum float64

func (n *ffNum) UnmarshalJSON(b []byte) error {
	var v any
	if json.Unmarshal(b, &v) != nil {
		*n = 0
		return nil
	}
	switch t := v.(type) {
	case float64:
		*n = ffNum(t)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		*n = ffNum(f)
	case bool:
		if t {
			*n = 1
		}
	default:
		*n = 0
	}
	return nil
}

func (n ffNum) F() float64 { return float64(n) }
func (n ffNum) I() int     { return int(math.Round(float64(n))) }

type ffStr string

func (s *ffStr) UnmarshalJSON(b []byte) error {
	var v any
	if json.Unmarshal(b, &v) != nil || v == nil {
		*s = ""
		return nil
	}
	switch t := v.(type) {
	case string:
		*s = ffStr(t)
	case float64:
		*s = ffStr(strconv.FormatFloat(t, 'f', -1, 64))
	default:
		*s = ffStr(fmt.Sprint(t))
	}
	return nil
}

type ffBool bool

func (x *ffBool) UnmarshalJSON(b []byte) error {
	var v any
	_ = json.Unmarshal(b, &v)
	switch t := v.(type) {
	case bool:
		*x = ffBool(t)
	case float64:
		*x = t != 0
	case string:
		*x = t == "true" || t == "1"
	default:
		*x = false
	}
	return nil
}

type ffNums []ffNum

func (l *ffNums) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		*l = nil
		return nil
	}
	out := make(ffNums, len(raw))
	for i, r := range raw {
		_ = out[i].UnmarshalJSON(r)
	}
	*l = out
	return nil
}

// ffDetail is the part of /detail PrintHarbor uses (FlashBridge PrinterDetail).
type ffDetail struct {
	Pid               ffNum  `json:"pid"`
	Name              ffStr  `json:"name"`
	Model             ffStr  `json:"model"`
	Status            ffStr  `json:"status"`
	PrintProgress     ffNum  `json:"printProgress"`
	PrintFileName     ffStr  `json:"printFileName"`
	PrintFileThumbURL ffStr  `json:"printFileThumbUrl"`
	PrintLayer        ffNum  `json:"printLayer"`
	TargetPrintLayer  ffNum  `json:"targetPrintLayer"`
	PrintDuration     ffNum  `json:"printDuration"`
	EstimatedTime     ffNum  `json:"estimatedTime"`
	RightTemp         ffNum  `json:"rightTemp"`
	RightTargetTemp   ffNum  `json:"rightTargetTemp"`
	NozzleTemps       ffNums `json:"nozzleTemps"`
	NozzleTargetTemps ffNums `json:"nozzleTargetTemps"`
	PlatTemp          ffNum  `json:"platTemp"`
	PlatTargetTemp    ffNum  `json:"platTargetTemp"`
	ChamberTemp       *ffNum `json:"chamberTemp"`
	ChamberTargetTemp ffNum  `json:"chamberTargetTemp"`
	CameraStreamURL   ffStr  `json:"cameraStreamUrl"`
	Camera            ffNum  `json:"camera"`
	LightStatus       ffStr  `json:"lightStatus"`
	InternalFanStatus ffStr  `json:"internalFanStatus"`
	ExternalFanStatus ffStr  `json:"externalFanStatus"`
	DoorStatus        ffStr  `json:"doorStatus"`
	CoolingFanSpeed   ffNum  `json:"coolingFanSpeed"`
	ChamberFanSpeed   ffNum  `json:"chamberFanSpeed"`
	PrintSpeedAdjust  ffNum  `json:"printSpeedAdjust"`
	ZAxisCompensation ffNum  `json:"zAxisCompensation"`
	ErrorCode         ffStr  `json:"errorCode"`
	HasMatlStation    ffBool `json:"hasMatlStation"`
	MatlStationInfo   struct {
		CurrentSlot ffNum `json:"currentSlot"`
		SlotCnt     ffNum `json:"slotCnt"`
		SlotInfos   []struct {
			SlotID        ffNum  `json:"slotId"`
			MaterialName  ffStr  `json:"materialName"`
			MaterialColor ffStr  `json:"materialColor"`
			HasFilament   ffBool `json:"hasFilament"`
		} `json:"slotInfos"`
	} `json:"matlStationInfo"`
}

func (d ffDetail) isCreator5() bool {
	return d.Pid.I() == 40 || d.Pid.I() == 41 || (d.Pid.I() == 0 && strings.Contains(string(d.Name+d.Model), "Creator 5"))
}
func (d ffDetail) isCreator5Pro() bool {
	return d.Pid.I() == 41 || (d.Pid.I() == 0 && strings.Contains(strings.ToLower(string(d.Name+d.Model)), "creator 5 pro"))
}

// ffHex normalizes the firmware's colours (#RRGGBB, RRGGBB or AARRGGBB).
func ffHex(c string) string {
	h := strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(c), "#"))
	if len(h) == 8 {
		h = h[2:]
	}
	if len(h) != 6 {
		return ""
	}
	if _, err := strconv.ParseUint(h, 16, 32); err != nil {
		return ""
	}
	return "#" + h
}

func NewFlashForge(addr, serial, code string) (*FlashForge, error) {
	a := strings.TrimSpace(addr)
	if a == "" {
		return nil, fmt.Errorf("FlashForge printers need the printer's IP address")
	}
	if !strings.Contains(a, "://") {
		a = "http://" + a
	}
	u, err := url.Parse(a)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad address %q", addr)
	}
	if u.Port() == "" {
		u.Host += ":8898"
	}
	return &FlashForge{base: u, serial: strings.TrimSpace(serial), code: strings.TrimSpace(code), client: &http.Client{Timeout: 10 * time.Minute}}, nil
}

func (f *FlashForge) Capabilities() Capabilities {
	return Capabilities{Status: true, Pause: true, Resume: true, Cancel: true, Upload: true, StartPrint: true,
		Temperatures: true, Camera: true, FilamentSlots: true, Light: true, Files: true, SpeedFactor: true, ZOffset: true, SetSlots: true}
}

// OutputFormat is the sliced-file format FlashForge printers take: OrcaSlicer's
// native FlashForge .gcode.3mf (Creator 5 / AD5X / Adventurer 5M). It carries the
// embedded preview thumbnail and the flashforge metadata Orca writes for its
// FlashForge machine profiles, and is what OrcaSlicer's own "send to printer"
// uploads — so PrintHarbor sends exactly what the printer expects.
func (f *FlashForge) OutputFormat() string { return "gcode.3mf" }

func (f *FlashForge) post(ctx context.Context, path string, body map[string]any, out any) error {
	return f.request(ctx, path, body, out, false)
}

// postRaw is post for endpoints that answer at the top level, not in "detail".
func (f *FlashForge) postRaw(ctx context.Context, path string, body map[string]any, out any) error {
	return f.request(ctx, path, body, out, true)
}

func (f *FlashForge) request(ctx context.Context, path string, body map[string]any, out any, raw bool) error {
	body["serialNumber"], body["checkCode"] = f.serial, f.code
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", f.base.String()+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	all, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(all, &env); err != nil {
		return fmt.Errorf("flashforge %s: %s", path, resp.Status)
	}
	if env.Code != 0 || (env.Message != "" && env.Message != "Success") {
		if strings.Contains(strings.ToLower(env.Message), "check") || strings.Contains(strings.ToLower(env.Message), "serial") {
			return fmt.Errorf("the printer refused the serial number or pairing code")
		}
		return fmt.Errorf("printer said: %s (code %d)", env.Message, env.Code)
	}
	if out == nil {
		return nil
	}
	if raw {
		return json.Unmarshal(all, out)
	}
	if len(env.Detail) > 0 {
		return json.Unmarshal(env.Detail, out)
	}
	return nil
}

func (f *FlashForge) detail(ctx context.Context) (ffDetail, error) {
	var d ffDetail
	if f.serial == "" || f.code == "" {
		return d, fmt.Errorf("enter the printer's serial number and pairing code (Edit printer): turn on LAN-only mode on the printer to see the pairing code")
	}
	if err := f.post(ctx, "/detail", map[string]any{}, &d); err != nil {
		return d, err
	}
	f.mu.Lock()
	f.last, f.lastAt, f.creator5 = d, time.Now(), d.isCreator5()
	f.mu.Unlock()
	return d, nil
}

func (f *FlashForge) Status(ctx context.Context) (Status, error) {
	d, err := f.detail(ctx)
	if err != nil {
		return Status{State: StateOffline}, err
	}
	st := Status{JobName: string(d.PrintFileName), Progress: d.PrintProgress.F(), Bed: d.PlatTemp.F(), BedTarget: d.PlatTargetTemp.F(),
		Layer: d.PrintLayer.I(), TotalLayers: d.TargetPrintLayer.I(), ElapsedSec: d.PrintDuration.I(), RemainingSec: d.EstimatedTime.I(),
		Fan: d.CoolingFanSpeed.F() / 100, ZOffset: d.ZAxisCompensation.F()}
	if st.Progress > 1 {
		st.Progress /= 100
	}
	sp := d.PrintSpeedAdjust.I()
	if sp == 0 {
		sp = 100
	}
	st.SpeedFactor = float64(sp) / 100
	// Every nozzle (Creator 5 has four), like FlashBridge's toolTemps.
	if len(d.NozzleTemps) > 0 {
		for i, tn := range d.NozzleTemps {
			t := tn.F()
			tt := 0.0
			if i < len(d.NozzleTargetTemps) {
				tt = d.NozzleTargetTemps[i].F()
			}
			if i == 0 {
				st.Nozzle, st.NozzleTarget = t, tt
			}
			if len(d.NozzleTemps) > 1 {
				st.Tools = append(st.Tools, ToolTemp{Temp: t, Target: tt})
			}
		}
	} else {
		st.Nozzle, st.NozzleTarget = d.RightTemp.F(), d.RightTargetTemp.F()
	}
	if d.ChamberTemp != nil {
		st.Chamber, st.ChamberTarget = d.ChamberTemp.F(), d.ChamberTargetTemp.F()
		if d.isCreator5() {
			st.ChamberHeater = "chamber"
		}
	}
	// FlashBridge's job states: a finished or cancelled print waits for Clear platform.
	fname := string(d.PrintFileName)
	switch strings.ToLower(string(d.Status)) {
	case "printing", "pausing":
		st.State = StatePrinting
	case "pause", "paused":
		st.State = StatePaused
	case "heating", "calibrate_doing", "busy", "downloading":
		if fname != "" && d.PrintProgress.F() < 1 {
			st.State = StatePrinting
		} else {
			st.State = StateIdle
		}
	case "completed":
		st.State = StateFinished
	case "cancel": // still waits for "platform cleared", like a completed print
		st.State, st.Stopped = StateFinished, true
	case "error":
		st.State, st.Message = StateError, "Printer error "+string(d.ErrorCode)
	default:
		st.State = StateIdle
	}
	if d.LightStatus == "open" {
		st.Light = "on"
	} else {
		st.Light = "off"
	}
	for _, s := range d.MatlStationInfo.SlotInfos {
		sl := Slot{Index: s.SlotID.I(), Color: ffHex(string(s.MaterialColor))}
		if s.HasFilament {
			sl.Type = string(s.MaterialName)
		}
		st.Slots = append(st.Slots, sl)
	}
	st.CurrentSlot = d.MatlStationInfo.CurrentSlot.I()
	cam := string(d.CameraStreamURL)
	if cam == "" {
		cam = "http://" + f.base.Hostname() + ":8080/?action=stream"
	}
	f.mu.Lock()
	f.camURL = cam
	f.thumb = string(d.PrintFileThumbURL)
	f.mu.Unlock()
	st.SnapshotURL = cam
	pal := ffAD5XPalette
	if d.isCreator5() {
		pal = ffCreator5Palette
	}
	st.FF = &FFExtra{HasFiltration: d.isCreator5Pro() || (d.InternalFanStatus != "" && d.ExternalFanStatus != ""),
		FiltrationInternal: d.InternalFanStatus == "open", FiltrationExternal: d.ExternalFanStatus == "open",
		ChamberFan: d.ChamberFanSpeed.F(), CurrentSlot: d.MatlStationInfo.CurrentSlot.I(), SlotPalette: pal, Materials: ffMaterials,
		HasThumb: d.PrintFileThumbURL != "" || fname != "", Door: string(d.DoorStatus)}
	return st, nil
}

func (f *FlashForge) control(ctx context.Context, cmd string, args any) error {
	return f.post(ctx, "/control", map[string]any{"payload": map[string]any{"cmd": cmd, "args": args}}, nil)
}

func (f *FlashForge) cached(ctx context.Context) (ffDetail, error) {
	f.mu.Lock()
	d, at := f.last, f.lastAt
	f.mu.Unlock()
	if time.Since(at) < 5*time.Second {
		return d, nil
	}
	return f.detail(ctx)
}

// Control handles the printer page's buttons with FlashForge's own commands.
func (f *FlashForge) Control(ctx context.Context, op string, args map[string]any) error {
	num := func(k string) float64 { v, _ := args[k].(float64); return v }
	d, err := f.cached(ctx)
	if err != nil {
		return err
	}
	switch op {
	case "temp":
		heater, _ := args["heater"].(string)
		t := num("value")
		temp := map[string]any{"rightNozzle": ffNoChange, "leftNozzle": ffNoChange, "platform": ffNoChange, "chamber": ffNoChange}
		var n int
		switch {
		case heater == "bed":
			if t <= 0 {
				temp["platform"] = ffOff
			} else {
				temp["platform"] = int(t + .5)
			}
		case heater == "chamber":
			switch {
			case t <= 0:
				temp["chamber"] = ffOff
			case t > 80:
				temp["chamber"] = 80
			default:
				temp["chamber"] = int(t + .5)
			}
		case heater == "nozzle" || strings.HasPrefix(heater, "tool"):
			if heater != "nozzle" {
				fmt.Sscanf(heater, "tool%d", &n)
			}
			if d.isCreator5() {
				noz := []int{ffNoChange, ffNoChange, ffNoChange, ffNoChange}
				if n < 0 || n > 3 {
					return fmt.Errorf("nozzle %d out of range", n+1)
				}
				noz[n] = ffNozzleOff
				if t > 0 {
					noz[n] = int(t + .5)
				}
				temp["nozzles"] = noz
			} else if t <= 0 {
				temp["rightNozzle"] = ffOff
			} else {
				temp["rightNozzle"] = int(t + .5)
			}
		default:
			return fmt.Errorf("unknown heater %q", heater)
		}
		if d.isCreator5() {
			if _, ok := temp["nozzles"]; !ok {
				temp["nozzles"] = []int{ffNoChange, ffNoChange, ffNoChange, ffNoChange}
			}
		}
		return f.control(ctx, "temperatureCtl_cmd", temp)
	case "speed", "zoffset", "fan", "chamber_fan":
		// printerCtl_cmd sets all four at once; unchanged values are sent as they are.
		sp := d.PrintSpeedAdjust.I()
		if sp == 0 {
			sp = 100
		}
		a := map[string]any{"zAxisCompensation": d.ZAxisCompensation.F(), "speed": sp,
			"chamberFan": d.ChamberFanSpeed.I(), "coolingFan": d.CoolingFanSpeed.I(), "coolingLeftFan": 0}
		switch op {
		case "speed":
			a["speed"] = int(num("value"))
		case "zoffset":
			z := d.ZAxisCompensation.F() + num("distance")
			a["zAxisCompensation"] = float64(int(z*100+sign(z)*0.5)) / 100
		case "fan":
			a["coolingFan"] = int(num("value"))
		case "chamber_fan":
			a["chamberFan"] = int(num("value"))
		}
		err := f.control(ctx, "printerCtl_cmd", a)
		if err == nil {
			f.mu.Lock()
			f.lastAt = time.Time{}
			f.mu.Unlock()
		}
		return err
	case "filtration":
		in, ex := d.InternalFanStatus == "open", d.ExternalFanStatus == "open"
		if v, ok := args["internal"].(bool); ok {
			in = v
		}
		if v, ok := args["external"].(bool); ok {
			ex = v
		}
		oc := func(b bool) string {
			if b {
				return "open"
			}
			return "close"
		}
		return f.control(ctx, "circulateCtl_cmd", map[string]any{"internal": oc(in), "external": oc(ex)})
	}
	return ErrUnsupported
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// SetSlot tells the material station what a slot holds. The colour is
// snapped to the firmware's palette, which is all it accepts.
func (f *FlashForge) SetSlot(ctx context.Context, slot int, material, color string) error {
	d, err := f.cached(ctx)
	if err != nil {
		return err
	}
	pal := ffAD5XPalette
	if d.isCreator5() {
		pal = ffCreator5Palette
	}
	rgb := strings.TrimPrefix(ffSnap(color, pal), "#")
	return f.control(ctx, "msConfig_cmd", map[string]any{"slot": slot, "mt": material, "rgb": rgb})
}

// ffSnap picks the nearest palette colour (CIE76 in Lab, like FlashBridge).
func ffSnap(hex string, pal [][]string) string {
	lab := func(h string) [3]float64 {
		h = ffHex(h)
		if h == "" {
			return [3]float64{100, 0, 0}
		}
		lin := func(s string) float64 {
			v, _ := strconv.ParseUint(s, 16, 8)
			c := float64(v) / 255
			if c <= 0.04045 {
				return c / 12.92
			}
			return math.Pow((c+0.055)/1.055, 2.4)
		}
		r, g, b := lin(h[1:3]), lin(h[3:5]), lin(h[5:7])
		x := (r*0.4124564 + g*0.3575761 + b*0.1804375) / 0.95047
		y := r*0.2126729 + g*0.7151522 + b*0.072175
		z := (r*0.0193339 + g*0.119192 + b*0.9503041) / 1.08883
		ff := func(t float64) float64 {
			if t > 0.008856 {
				return math.Cbrt(t)
			}
			return 7.787*t + 16.0/116
		}
		return [3]float64{116*ff(y) - 16, 500 * (ff(x) - ff(y)), 200 * (ff(y) - ff(z))}
	}
	l := lab(hex)
	best, bd := pal[0][1], math.Inf(1)
	for _, p := range pal {
		m := lab(p[1])
		d := (l[0]-m[0])*(l[0]-m[0]) + (l[1]-m[1])*(l[1]-m[1]) + (l[2]-m[2])*(l[2]-m[2])
		if d < bd {
			bd, best = d, p[1]
		}
	}
	return best
}

func (f *FlashForge) HasStream() bool { return true }

// Snapshot is only used when no relay exists; the relay normally serves frames.
// Stream opens the printer's live camera (mjpg-streamer on port 8080).
func (f *FlashForge) Stream(ctx context.Context) (*http.Response, error) {
	f.mu.Lock()
	u := f.camURL
	f.mu.Unlock()
	if u == "" {
		u = "http://" + f.base.Hostname() + ":8080/?action=stream"
	}
	try := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("camera: %s", resp.Status)
		}
		return resp, nil
	}
	resp, err := try()
	if err == nil {
		return resp, nil
	}
	// The camera may be switched off: turn it on (streamCtrl_cmd, as in
	// FlashBridge's ff_client) and try once more, at most once a minute.
	f.mu.Lock()
	again := time.Since(f.camOpened) > time.Minute
	if again {
		f.camOpened = time.Now()
	}
	f.mu.Unlock()
	if again && f.control(ctx, "streamCtrl_cmd", map[string]any{"action": "open"}) == nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
		if resp, err2 := try(); err2 == nil {
			return resp, nil
		}
	}
	return nil, fmt.Errorf("the printer's camera is busy: FlashForge cameras take one connection at a time, and another app (for example FlashBridge or FlashPrint) is using it")
}

// Thumb is the picture of the file being printed.
func (f *FlashForge) Thumb(ctx context.Context) ([]byte, string, error) {
	f.mu.Lock()
	tu, name := f.thumb, string(f.last.PrintFileName)
	f.mu.Unlock()
	if tu != "" {
		if u, err := f.base.Parse(tu); err == nil {
			if b, ct, err := fetchImage(ctx, f.client, u.String()); err == nil {
				return b, ct, nil
			}
		}
	}
	if name == "" {
		return nil, "", fmt.Errorf("no picture")
	}
	var r struct {
		ImageData string `json:"imageData"`
	}
	if err := f.postRaw(ctx, "/gcodeThumb", map[string]any{"fileName": name}, &r); err != nil || r.ImageData == "" {
		return nil, "", fmt.Errorf("no picture")
	}
	b, err := base64.StdEncoding.DecodeString(r.ImageData)
	return b, "image/png", err
}

// Files lists the files stored on the printer.
func (f *FlashForge) Files(ctx context.Context) ([]PrinterFile, error) {
	var r struct {
		GcodeList       []any `json:"gcodeList"`
		GcodeListDetail []struct {
			Name string `json:"gcodeFileName"`
		} `json:"gcodeListDetail"`
	}
	if err := f.postRaw(ctx, "/gcodeList", map[string]any{}, &r); err != nil {
		return nil, err
	}
	out := []PrinterFile{}
	for _, e := range r.GcodeListDetail {
		out = append(out, PrinterFile{Name: e.Name})
	}
	if len(out) == 0 {
		for _, e := range r.GcodeList {
			switch v := e.(type) {
			case string:
				out = append(out, PrinterFile{Name: v})
			case map[string]any:
				if n, _ := v["gcodeFileName"].(string); n != "" {
					out = append(out, PrinterFile{Name: n})
				}
			}
		}
	}
	return out, nil
}

// StartFile prints a file already on the printer (/printGcode).
// ffClearWait is how long the printer is given after "clear platform" before
// the next job is sent (FlashBridge waits the same 2 s).
var ffClearWait = 2 * time.Second

// clearFinishedJob does what FlashBridge does before every print it starts: a
// job that has finished or been cancelled must be acknowledged ("clear
// platform") or the printer refuses the next one. So when the printer still
// shows one, clear it and give the printer a moment. This is about the
// printer's own message only; whether the bed is really free is PrintHarbor's
// "bed clear" (you, or the camera).
func (f *FlashForge) clearFinishedJob(ctx context.Context) {
	d, err := f.detail(ctx)
	if err != nil {
		return
	}
	switch strings.ToLower(string(d.Status)) {
	case "completed", "cancel":
		if f.ClearPlatform(ctx) == nil {
			select {
			case <-ctx.Done():
			case <-time.After(ffClearWait):
			}
		}
	}
}

func (f *FlashForge) StartFile(ctx context.Context, name string) error {
	f.clearFinishedJob(ctx)
	d, _ := f.cached(ctx)
	body := map[string]any{"fileName": name, "levelingBeforePrint": Level(ctx), "flowCalibration": false,
		"useMatlStation": false, "gcodeToolCnt": 0, "materialMappings": []any{}}
	if d.isCreator5() {
		body["firstLayerInspection"] = false
		body["timeLapseVideo"] = false
	}
	return f.postRaw(ctx, "/printGcode", body, nil)
}

// DeleteFile isn't offered by FlashForge's LAN API.
func (f *FlashForge) DeleteFile(ctx context.Context, name string) error { return ErrUnsupported }

func (f *FlashForge) job(ctx context.Context, action string) error {
	return f.post(ctx, "/control", map[string]any{"payload": map[string]any{"cmd": "jobCtl_cmd", "args": map[string]any{"jobID": "", "action": action}}}, nil)
}

func (f *FlashForge) Pause(ctx context.Context) error  { return f.job(ctx, "pause") }
func (f *FlashForge) Resume(ctx context.Context) error { return f.job(ctx, "continue") }
func (f *FlashForge) Cancel(ctx context.Context) error { return f.job(ctx, "cancel") }

func (f *FlashForge) SetLight(ctx context.Context, on bool) error {
	s := "close"
	if on {
		s = "open"
	}
	return f.post(ctx, "/control", map[string]any{"payload": map[string]any{"cmd": "lightControl_cmd", "args": map[string]any{"status": s}}}, nil)
}

// ClearPlatform tells the printer the finished print was taken off (clears
// its "print finished" screen so it accepts the next job).
func (f *FlashForge) ClearPlatform(ctx context.Context) error {
	return f.post(ctx, "/control", map[string]any{"payload": map[string]any{"cmd": "stateCtrl_cmd", "args": map[string]any{"action": "setClearPlatform"}}}, nil)
}

func (f *FlashForge) Snapshot(ctx context.Context) ([]byte, string, error) {
	f.mu.Lock()
	u := f.camURL
	f.mu.Unlock()
	if u == "" {
		u = "http://" + f.base.Hostname() + ":8080/?action=stream"
	}
	// mjpg-streamer serves single frames at ?action=snapshot.
	if strings.Contains(u, "action=stream") {
		if b, err := mjpegFrame(ctx, f.client, strings.Replace(u, "action=stream", "action=snapshot", 1)); err == nil {
			return b, "image/jpeg", nil
		}
	}
	b, err := mjpegFrame(ctx, f.client, u)
	return b, "image/jpeg", err
}

// Upload sends a sliced file to the printer's /uploadGcode; with start=true the
// printer starts it right away. A filament mapping (AD5X / Creator 5 material
// station) is sent when one was chosen.
//
// This is written with a raw socket, exactly like FlashBridge's FlashForge
// client (android-tv-app/source/lib/core/ff/ff_client.dart upload()), which is
// how OrcaSlicer's own FlashForge sender does it. It matters: FlashForge
// firmware rejects the upload with a 500 unless the request is sent its way —
// a fixed Content-Length (NOT Go's chunked streaming), the header names in
// their exact spelling (serialNumber, printNow…, which net/http would rewrite
// to Serialnumber, Printnow), Expect: 100-continue honoured, and the body sent
// after the printer answers 100. Going through net/http instead gave the 500.
func (f *FlashForge) Upload(ctx context.Context, name string, r io.Reader, start bool) error {
	if start {
		f.clearFinishedJob(ctx)
	}
	tmp, err := os.CreateTemp("", "ff-*.gcode")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	size, err := io.Copy(tmp, r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}

	boundary := fmt.Sprintf("----printharbor%d", time.Now().UnixNano())
	pre := []byte("--" + boundary + "\r\nContent-Disposition: form-data; name=\"gcodeFile\"; filename=\"" +
		ffFileName(name) + "\"\r\nContent-Type: application/octet-stream\r\n\r\n")
	post := []byte("\r\n--" + boundary + "--\r\n")

	// materialMappings: OrcaSlicer's JSON, base64-encoded, like FlashBridge.
	use := false
	mapJSON := "[]"
	if m := SlotMap(ctx); len(m) > 0 && start {
		type orcaMap struct {
			MaterialName      string `json:"materialName"`
			SlotID            int    `json:"slotId"`
			SlotMaterialColor string `json:"slotMaterialColor"`
			ToolID            int    `json:"toolId"`
			ToolMaterialColor string `json:"toolMaterialColor"`
		}
		d, _ := f.cached(ctx)
		var maps []orcaMap
		for tool, slot := range m {
			if slot <= 0 {
				continue
			}
			om := orcaMap{SlotID: slot, ToolID: tool}
			for _, s := range d.MatlStationInfo.SlotInfos {
				if s.SlotID.I() == slot {
					om.MaterialName = string(s.MaterialName)
					om.SlotMaterialColor = ffHex(string(s.MaterialColor))
					om.ToolMaterialColor = om.SlotMaterialColor
					break
				}
			}
			maps = append(maps, om)
		}
		if len(maps) > 0 {
			use = true
			b, _ := json.Marshal(maps)
			mapJSON = string(b)
		}
	}
	toolCnt := "0"
	if use {
		toolCnt = strconv.Itoa(len(SlotMap(ctx)))
	}

	// Headers in the exact order and spelling OrcaSlicer's sender uses.
	host := f.base.Host
	hdr := [][2]string{
		{"Host", host},
		{"Accept", "*/*"},
		{"serialNumber", f.serial},
		{"checkCode", f.code},
		{"fileSize", strconv.FormatInt(size, 10)},
		{"printNow", strconv.FormatBool(start)},
		{"levelingBeforePrint", strconv.FormatBool(start && Level(ctx))},
		{"flowCalibration", "false"},
		{"firstLayerInspection", "false"},
		{"timeLapseVideo", "false"},
		{"useMatlStation", strconv.FormatBool(use)},
		{"gcodeToolCnt", toolCnt},
		{"materialMappings", base64.StdEncoding.EncodeToString([]byte(mapJSON))},
		{"Content-Type", "multipart/form-data; boundary=" + boundary},
		{"Content-Length", strconv.FormatInt(int64(len(pre))+size+int64(len(post)), 10)},
		{"Expect", "100-continue"},
		{"Connection", "close"},
	}
	body, err := os.Open(tmp.Name())
	if err != nil {
		return err
	}
	defer body.Close()
	t0 := time.Now()
	log.Printf("FlashForge: uploading %s (%.1f MB) to %s (printNow=%v)", ffFileName(name), float64(size)/(1<<20), f.base.Hostname(), start)
	err = f.rawUpload(ctx, hdr, size, pre, body, post)
	if err != nil {
		log.Printf("FlashForge: upload of %s failed after %s: %v", ffFileName(name), time.Since(t0).Round(time.Second), err)
	} else {
		log.Printf("FlashForge: uploaded %s in %s", ffFileName(name), time.Since(t0).Round(time.Second))
	}
	return err
}

// rawUpload sends the file to the printer over a raw socket, a faithful port of
// FlashBridge's FlashForge client (ff_client.dart upload()): it drains the
// socket in the background the whole time so the printer is never blocked from
// answering, waits for the "100 Continue", streams the body, and then waits for
// the printer to close the connection ("Connection: close") and reads the
// answer out of everything it sent.
func (f *FlashForge) rawUpload(ctx context.Context, hdr [][2]string, size int64, pre []byte, file io.Reader, post []byte) error {
	host := f.base.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host += ":8898"
	}
	d := net.Dialer{Timeout: 8 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return fmt.Errorf("could not reach the printer at %s: %w", f.base.Hostname(), err)
	}
	defer conn.Close()
	deadline := time.Now().Add(6 * time.Minute)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	conn.SetDeadline(deadline)
	stop := make(chan struct{})
	defer close(stop)
	go func() { // cancelling the job closes the socket, unblocking everything
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stop:
		}
	}()

	// Drain the socket continuously into a buffer (like FlashBridge's
	// sock.listen). firstData fires on the first bytes, closed when the printer
	// hangs up.
	var mu sync.Mutex
	var buf []byte
	firstData := make(chan struct{}, 1)
	closed := make(chan struct{})
	go func() {
		tmp := make([]byte, 32<<10)
		for {
			n, err := conn.Read(tmp)
			if n > 0 {
				mu.Lock()
				buf = append(buf, tmp[:n]...)
				mu.Unlock()
				select {
				case firstData <- struct{}{}:
				default:
				}
			}
			if err != nil {
				close(closed)
				return
			}
		}
	}()
	snapshot := func() string { mu.Lock(); defer mu.Unlock(); return string(buf) }
	reset := func() { mu.Lock(); buf = buf[:0]; mu.Unlock() }

	var head bytes.Buffer
	head.WriteString("POST /uploadGcode HTTP/1.1\r\n")
	for _, h := range hdr {
		head.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	head.WriteString("\r\n")
	if _, err := conn.Write(head.Bytes()); err != nil {
		return fmt.Errorf("could not send to the printer: %w", err)
	}

	// Wait briefly for "100 Continue" (proceed anyway after 2s, like curl).
	select {
	case <-firstData:
	case <-closed:
	case <-time.After(2 * time.Second):
	}
	early := snapshot()
	if strings.HasPrefix(early, "HTTP/") && !strings.HasPrefix(early, "HTTP/1.1 100") {
		log.Printf("FlashForge upload: printer rejected it before the body: %s", ffFirstLine(early))
		return ffUploadErr(early)
	}
	reset() // discard the 100 Continue

	// Stream the body. The background reader keeps draining, so if the printer
	// answers (and stops reading) mid-upload, the write just errors here.
	tb := time.Now()
	var werr error
	if _, werr = conn.Write(pre); werr == nil {
		if _, werr = io.Copy(conn, file); werr == nil {
			_, werr = conn.Write(post)
		}
	}
	log.Printf("FlashForge upload: body sent in %s (write err %v); waiting for the printer's answer", time.Since(tb).Round(time.Millisecond), werr)

	// Wait for the printer to close the connection, then read its answer.
	select {
	case <-closed:
	case <-time.After(3 * time.Minute):
	}
	text := snapshot()
	for strings.HasPrefix(text, "HTTP/1.1 100") { // strip any interim 100
		i := strings.Index(text, "\r\n\r\n")
		if i < 0 {
			break
		}
		text = text[i+4:]
	}
	status := ffFirstLine(text)
	if !strings.Contains(status, " 200") {
		if strings.TrimSpace(text) == "" {
			if werr != nil {
				return fmt.Errorf("flashforge upload: the printer stopped accepting the file (%v)", werr)
			}
			return fmt.Errorf("flashforge upload: the printer did not answer")
		}
		log.Printf("FlashForge upload: printer said: %s", status)
		return ffUploadErr(text)
	}
	if i := strings.IndexByte(text, '{'); i >= 0 {
		var env struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(ffBody(text[i:])), &env) == nil && env.Code != 0 {
			if env.Message == "" {
				env.Message = fmt.Sprintf("code %d", env.Code)
			}
			return fmt.Errorf("flashforge upload: %s", env.Message)
		}
	}
	return nil
}

func ffFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// ffBody trims a body down to its JSON object.
func ffBody(s string) string {
	if i := strings.LastIndex(s, "}"); i >= 0 {
		return s[:i+1]
	}
	return s
}

func ffUploadErr(text string) error {
	if i := strings.Index(text, "{"); i >= 0 {
		var env struct {
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(ffBody(text[i:])), &env) == nil && env.Message != "" {
			return fmt.Errorf("flashforge upload: %s", env.Message)
		}
	}
	line := strings.TrimSpace(text)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "HTTP/1.1"), "HTTP/1.0"))
	if line == "" {
		line = "the printer rejected the upload"
	}
	return fmt.Errorf("flashforge upload: %s", line)
}

// ffFileName is the name OrcaSlicer's FlashForge sender uses: only ASCII
// letters, digits, '.', '_' and '-'. The printer fails to unpack others.
func ffFileName(name string) string {
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		name = "print"
	}
	b := []byte(name)
	for i, c := range b {
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '.' || c == '_' || c == '-') {
			b[i] = '_'
		}
	}
	return string(b)
}
