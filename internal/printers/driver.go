// Package printers holds PrintHarbor's common printer model and the drivers
// that translate each brand's protocol into it.
//
// Adding a brand = writing one Driver and calling Register in an init().
// A driver only implements what its printer supports and says so through
// Capabilities, so the UI can show one consistent control panel that simply
// hides what a machine can't do.
package printers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

type Capabilities struct {
	Status        bool `json:"status"`
	Pause         bool `json:"pause"`
	Resume        bool `json:"resume"`
	Cancel        bool `json:"cancel"`
	Upload        bool `json:"upload"`
	StartPrint    bool `json:"start_print"`
	Temperatures  bool `json:"temperatures"`
	Camera        bool `json:"camera"`
	FilamentSlots bool `json:"filament_slots"` // printer reports loaded filament
	Light         bool `json:"light"`
	// Control page features.
	Gcode       bool `json:"gcode"`        // accepts G-code: temperatures, jog, home, fans
	Console     bool `json:"console"`      // shows printer messages
	Files       bool `json:"files"`        // list / start / delete files on the printer
	SpeedFactor bool `json:"speed_factor"` // M220/M221 speed and flow
	ZOffset     bool `json:"z_offset"`     // live Z adjust while printing
	Jog         bool `json:"jog"`          // safe to jog axes from the UI
	SetSlots    bool `json:"set_slots"`    // loaded filament can be changed from PrintHarbor
}

type State string

const (
	StateOffline  State = "offline"
	StateIdle     State = "idle"
	StatePrinting State = "printing"
	StatePaused   State = "paused"
	StateFinished State = "finished" // job done, part still on the bed
	StateError    State = "error"
)

// Slot is one filament position: an AMS slot, a toolhead, or the single spool.
type Slot struct {
	Index int    `json:"index"` // 1-based
	Type  string `json:"type"`  // PLA, PETG, ...
	Color string `json:"color"` // #RRGGBB
	Name  string `json:"name,omitempty"`
	// SpoolID links the slot to a Spoolman spool (optional).
	SpoolID int `json:"spool_id,omitempty"`
}

type Status struct {
	State   State  `json:"state"`
	Message string `json:"message,omitempty"`
	// Stopped: the printer says its last print was cancelled or failed, not
	// completed. Drivers set it where the printer tells them (the state alone
	// can't say: a cancelled print also ends up idle or "finished, clear the bed").
	Stopped      bool    `json:"stopped,omitempty"`
	JobName      string  `json:"job_name,omitempty"`
	Progress     float64 `json:"progress"` // 0..1
	RemainingSec int     `json:"remaining_sec,omitempty"`
	Nozzle       float64 `json:"nozzle_c"`
	NozzleTarget float64 `json:"nozzle_target_c"`
	Bed          float64 `json:"bed_c"`
	BedTarget    float64 `json:"bed_target_c"`
	Chamber      float64 `json:"chamber_c,omitempty"`
	// ChamberTarget and ChamberHeater are set when the chamber can be heated
	// (Klipper heater_generic).
	ChamberTarget float64 `json:"chamber_target_c,omitempty"`
	ChamberHeater string  `json:"chamber_heater,omitempty"`
	// Tools lists every nozzle on multi-tool printers (Snapmaker U1,
	// Creator 5, toolchangers); Nozzle/NozzleTarget are the first one.
	Tools       []ToolTemp `json:"tools,omitempty"`
	Slots       []Slot     `json:"slots,omitempty"`        // only when the printer reports them
	CurrentSlot int        `json:"current_slot,omitempty"` // slot feeding the nozzle now (0 = unknown)
	CameraURL   string     `json:"camera_url,omitempty"`
	SnapshotURL string     `json:"snapshot_url,omitempty"`
	Light       string     `json:"light,omitempty"` // "on" / "off" when known
	// Extra detail for the control page (zero when the printer doesn't report it).
	Position    []float64 `json:"position,omitempty"` // X, Y, Z
	Homed       string    `json:"homed,omitempty"`    // e.g. "xyz"
	Fan         float64   `json:"fan"`                // part cooling fan 0..1
	SpeedFactor float64   `json:"speed_factor,omitempty"`
	FlowFactor  float64   `json:"flow_factor,omitempty"`
	ZOffset     float64   `json:"z_offset"`
	Layer       int       `json:"layer,omitempty"`
	TotalLayers int       `json:"total_layers,omitempty"`
	ElapsedSec  int       `json:"elapsed_sec,omitempty"`
	// StreamURL is PrintHarbor's live camera address for this printer.
	StreamURL string `json:"stream_url,omitempty"`
	// FF holds FlashForge-only details (filtration, chamber fan, slot palette).
	FF *FFExtra `json:"ff,omitempty"`
}

// ConsoleLine is one message from (or to) the printer.
// ToolTemp is one nozzle's temperature.
type ToolTemp struct {
	Temp   float64 `json:"temp_c"`
	Target float64 `json:"target_c"`
}

// PlatformClearer is a printer that waits for "platform cleared" after a
// print (FlashForge).
type PlatformClearer interface {
	ClearPlatform(ctx context.Context) error
}

// Controller is a printer with its own control actions instead of G-code
// (FlashBridge). op is temp | speed | zoffset | fan | chamber_fan | filtration.
type Controller interface {
	Control(ctx context.Context, op string, args map[string]any) error
}

// Streamer opens a live MJPEG camera stream. HasStream is false for
// cameras that only take snapshots (Snapmaker U1).
type Streamer interface {
	Stream(ctx context.Context) (*http.Response, error)
	HasStream() bool
}

// Thumber returns the picture of the job being printed.
type Thumber interface {
	Thumb(ctx context.Context) ([]byte, string, error)
}

// SlotSetter changes what the printer thinks is loaded in a slot.
type SlotSetter interface {
	SetSlot(ctx context.Context, slot int, material, color string) error
}

type ConsoleLine struct {
	Time    float64 `json:"time"`
	Message string  `json:"message"`
	Type    string  `json:"type"` // command | response
}

// PrinterFile is a file stored on the printer.
type PrinterFile struct {
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Modified float64 `json:"modified"`
}

// GcodeSender runs G-code / macros on the printer.
type GcodeSender interface {
	SendGcode(ctx context.Context, script string) error
}

// Consoler returns recent printer console messages.
type Consoler interface {
	Console(ctx context.Context) ([]ConsoleLine, error)
}

// FileManager manages files stored on the printer.
type FileManager interface {
	Files(ctx context.Context) ([]PrinterFile, error)
	StartFile(ctx context.Context, name string) error
	DeleteFile(ctx context.Context, name string) error
}

// Driver talks to one physical printer.
type Driver interface {
	Capabilities() Capabilities
	Status(ctx context.Context) (Status, error)
	Pause(ctx context.Context) error
	Resume(ctx context.Context) error
	Cancel(ctx context.Context) error
	// Upload sends a sliced file. If start is true the print begins.
	Upload(ctx context.Context, filename string, r io.Reader, start bool) error
	// OutputFormat is the file the printer wants: "gcode" or "gcode.3mf".
	OutputFormat() string
}

// Factory builds a driver for a configured printer.
type Factory func(p Printer) (Driver, error)

// DriverInfo describes a driver for the "add printer" screen.
type DriverInfo struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	AddressHint string `json:"address_hint"`
	NeedsKey    bool   `json:"needs_key"`
	KeyLabel    string `json:"key_label,omitempty"`
	NeedsSerial bool   `json:"needs_serial,omitempty"`
	Help        string `json:"help,omitempty"`
}

var (
	regMu     sync.Mutex
	factories = map[string]Factory{}
	infos     = map[string]DriverInfo{}
)

func Register(info DriverInfo, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	factories[info.Name] = f
	infos[info.Name] = info
}

func Drivers() []DriverInfo {
	regMu.Lock()
	defer regMu.Unlock()
	out := []DriverInfo{}
	for _, i := range infos {
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Label < out[b].Label })
	return out
}

func newDriver(p Printer) (Driver, error) {
	regMu.Lock()
	f := factories[p.Driver]
	regMu.Unlock()
	if f == nil {
		return nil, fmt.Errorf("unknown driver %q", p.Driver)
	}
	return f(p)
}

// Prober is a driver that can run a one-shot connection check (dial, auth,
// clean up) without starting its background polling. Used by "Test
// connection" so the user finds out the IP / code / serial are right, and
// whether Developer Mode is on, before saving.
type Prober interface {
	Probe(ctx context.Context) error
}

// Probe tries to reach a printer from an unsaved config and returns nil on
// success or a friendly error. It never leaves a background connection open.
func Probe(ctx context.Context, p Printer) error {
	d, err := newDriver(p)
	if err != nil {
		return err
	}
	if pr, ok := d.(Prober); ok {
		return pr.Probe(ctx)
	}
	// Generic drivers (HTTP request/response): one status read is enough.
	if _, err := d.Status(ctx); err != nil {
		return err
	}
	if c, ok := d.(io.Closer); ok {
		_ = c.Close()
	}
	return nil
}

// Snapshotter is implemented by drivers that can grab a camera frame.
// PrintHarbor serves it at /api/printers/{id}/snapshot so the browser never
// talks to printers directly (no CORS / mixed-content / port guessing).
type Snapshotter interface {
	Snapshot(ctx context.Context) (data []byte, contentType string, err error)
}

// Lighter is implemented by drivers that can switch the chamber/work light.
type Lighter interface {
	SetLight(ctx context.Context, on bool) error
}

type ctxKey int

const slotMapKey ctxKey = 1

// WithSlotMap attaches the filament mapping for an upload: element i is the
// printer slot (1-based) for project filament i+1 (0 = printer default).
func WithSlotMap(ctx context.Context, m []int) context.Context {
	return context.WithValue(ctx, slotMapKey, m)
}

// SlotMap returns the mapping set with WithSlotMap.
func SlotMap(ctx context.Context) []int {
	m, _ := ctx.Value(slotMapKey).([]int)
	return m
}

// ErrUnsupported is returned for actions a driver does not implement.
var ErrUnsupported = fmt.Errorf("not supported by this printer")

// Bed leveling before a print works one of three ways, depending on the printer:
//
//   - "native": the driver's own print API carries a leveling flag — FlashForge's
//     levelingBeforePrint. The driver reads Level(ctx) and sets it; no g-code.
//   - "always": the printer levels every print on its own and there's nothing to
//     toggle — Bambu (its print command always sends bed_leveling).
//   - "gcode": the leveling is a firmware g-code command injected into the print's
//     start g-code (Klipper BED_MESH_CALIBRATE, Marlin G29, Prusa G80).

// NativeLevel reports that the driver levels through its own print API, so Level(ctx)
// is honoured at send time and no g-code is injected.
func NativeLevel(driver string) bool { return driver == "flashforge" }

// AlwaysLevels reports that the printer levels every print regardless, so there's
// no choice to make.
func AlwaysLevels(driver string) bool { return driver == "bambu" }

// DefaultLevelGcode is the start-g-code bed-leveling command for a g-code driver
// when the printer has none set. "" means there's no safe default to guess.
func DefaultLevelGcode(driver string) string {
	switch driver {
	case "moonraker", "elegoo": // Klipper (incl. Elegoo Centauri)
		return "BED_MESH_CALIBRATE"
	case "prusalink": // Prusa mesh bed leveling
		return "G80"
	default:
		return ""
	}
}

// LevelGcodeFor is the effective leveling command for a g-code printer: its own,
// or the driver default. Empty means none is known. (Not used for native/always
// drivers, which level through their print API.)
func LevelGcodeFor(p Printer) string {
	if g := strings.TrimSpace(p.LevelGcode); g != "" {
		return g
	}
	return DefaultLevelGcode(p.Driver)
}

const levelKey ctxKey = 2

// WithLevel marks an upload to level the bed before the print. Native-leveling
// drivers (FlashForge) read it; others level via injected start g-code instead.
func WithLevel(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, levelKey, on)
}

// Level reports whether this upload should level the bed first.
func Level(ctx context.Context) bool { b, _ := ctx.Value(levelKey).(bool); return b }
