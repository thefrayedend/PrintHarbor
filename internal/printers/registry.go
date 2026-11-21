package printers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thefrayedend/printharbor/internal/store"
)

// Printer is a configured printer in the workshop.
type Printer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Driver  string `json:"driver"`
	Address string `json:"address"`
	APIKey  string `json:"api_key,omitempty"`
	Serial  string `json:"serial,omitempty"`
	// MachinePreset is the Orca machine preset used to slice for this printer.
	MachinePreset string `json:"machine_preset"`
	// DefaultProcess is the quality profile first offered for this printer
	// (empty: the one its Orca printer profile names). PrintDefaults are your
	// own values for quality settings on this printer only; they go on top of
	// the general print defaults (Settings → Printing). Never changed in place:
	// a new map replaces it.
	DefaultProcess string            `json:"default_process,omitempty"`
	PrintDefaults  map[string]string `json:"print_defaults,omitempty"`
	// Slots is the filament the user says is loaded. Used when the printer
	// cannot report it; a reporting printer's live slots take priority.
	Slots []Slot `json:"slots"`
	// Hardware the profile doesn't tell us.
	HardenedNozzle bool `json:"hardened_nozzle"`
	Enclosed       bool `json:"enclosed"`
	// LevelGcode is run (as part of the print's start g-code) when the "Level
	// the bed before printing" toggle is on. It's firmware-specific — Klipper
	// uses BED_MESH_CALIBRATE, Marlin G29, Prusa G80 — so it's editable per
	// printer. Empty falls back to PrintHarbor's driver default (set only for
	// Klipper/Moonraker; no guessed command for other drivers).
	LevelGcode string `json:"level_gcode,omitempty"`
	// SnapshotURL overrides camera discovery: any URL that returns a JPEG/PNG
	// (for printers whose camera isn't registered with their firmware).
	SnapshotURL string `json:"snapshot_url,omitempty"`
	// Camera features.
	AutoClear bool `json:"auto_clear"` // mark the bed clear when the camera sees it empty
	// ClearFinished (FlashForge only) clears the printer's "print finished"
	// message by itself a few seconds after each print, like FlashBridge's
	// "Auto clear platform". It does not mark the bed clear.
	ClearFinished bool `json:"clear_finished,omitempty"`
	AIDetect      bool `json:"ai_detect"` // run AI failure detection (needs Obico ML API)
	AIPause       bool `json:"ai_pause"`  // pause the print when AI is confident
	// AISensitivity is low | medium | high (Obico's multiplier); AIInterval is
	// seconds between checks while printing (5-120, default 10).
	AISensitivity string `json:"ai_sensitivity,omitempty"`
	AIInterval    int    `json:"ai_interval,omitempty"`
}

// View is a printer plus its live state, as the UI and fit check see it.
type View struct {
	Printer
	Capabilities Capabilities `json:"capabilities"`
	Status       Status       `json:"status"`
	// BedClear is false after a print finishes until someone confirms the
	// parts were removed. Idle + BedClear = ready for the next job.
	BedClear  bool      `json:"bed_clear"`
	Ready     bool      `json:"ready"`
	UpdatedAt time.Time `json:"updated_at"`
	// Bed leveling before a print, for the UI's "Level the bed before printing"
	// toggle. CanLevel: it's possible on this printer. LevelAlways: it happens
	// every print on its own (Bambu), so the toggle is informational. LevelCmd:
	// the g-code shown next to the toggle (empty for native/always drivers).
	CanLevel    bool   `json:"can_level"`
	LevelAlways bool   `json:"level_always"`
	LevelCmd    string `json:"level_cmd,omitempty"`
}

// LoadedSlots returns live slots if the printer reports them, else the
// user-configured ones.
func (v View) LoadedSlots() []Slot {
	if len(v.Status.Slots) > 0 {
		return v.Status.Slots
	}
	return v.Slots
}

// Sample is one temperature reading for the control page's chart.
type Sample struct {
	T            int64   `json:"t"` // unix ms
	Nozzle       float64 `json:"n"`
	NozzleTarget float64 `json:"nt"`
	Bed          float64 `json:"b"`
	BedTarget    float64 `json:"bt"`
	Chamber      float64 `json:"c,omitempty"`
}

const historyLen = 600 // ~30 minutes at the default 3 s poll

type entry struct {
	history     []Sample
	p           Printer
	d           Driver
	st          Status
	bedClear    bool
	autoCleared time.Time // when PrintHarbor last sent "clear platform" by itself (see ClearPlatformAuto)
	updated     time.Time
	rawState    State // what the printer itself last said, before bed-clear rules
	// Following one print from start to end (see update):
	active      bool      // a print is believed to be in progress
	progress    float64   // the furthest it was seen to get (0..1)
	cancelAsked bool      // Stop was pressed in PrintHarbor during this print
	errored     bool      // the printer reported an error during this print
	seen        bool      // the printer itself has reported this print as printing
	sentAt      time.Time // when PrintHarbor started it (zero: started elsewhere)
	relay       *camRelay
}

type Registry struct {
	// OnTransition, if set, is called (in its own goroutine) when a
	// printer's state changes, and with ready=true when it becomes ready
	// for the next job.
	OnTransition func(id string, prev, cur State, ready bool)
	// OnPrintEnd, if set, is called once when a print ends, saying how. It runs
	// in the same goroutine as, and before, the OnTransition for that change.
	OnPrintEnd func(id string, end PrintEnd)

	mu    sync.RWMutex
	items map[string]*entry
	store *store.Store
}

// startGrace is how long a printer may take to report a print PrintHarbor just
// sent it as printing, before PrintHarbor concludes it never started.
var startGrace = 2 * time.Minute

// How a print ended.
const (
	EndCompleted = "completed" // ran to the end
	EndStopped   = "stopped"   // cancelled, on the printer or from PrintHarbor
	EndFailed    = "failed"    // the printer reported an error
	EndUnknown   = "unknown"   // the printer just went idle and never said how far it got
)

// PrintEnd says how a print ended. "The printer is idle again" is not "the
// print finished": a cancelled print looks the same. Anything that acts on a
// completed print (the Spoolman write-back, a "finished" notification) must
// look at Outcome.
type PrintEnd struct {
	Outcome  string  `json:"outcome"`
	Progress float64 `json:"progress"` // the furthest the print was seen to get
}

// outcomeOf decides how a print ended from what was seen: raw is the state
// the printer went to, stopped what the driver or the user said, progress the
// furthest it got.
func outcomeOf(raw State, stopped, errored bool, progress float64) string {
	switch {
	case stopped:
		return EndStopped
	case errored:
		return EndFailed
	case raw == StateFinished:
		return EndCompleted // the printer itself says it completed
	case progress >= 0.99:
		return EndCompleted
	case progress > 0:
		return EndStopped // went idle part-way without saying "complete"
	}
	return EndUnknown
}

type saved struct {
	Printers []Printer       `json:"printers"`
	BedClear map[string]bool `json:"bed_clear"`
}

func NewRegistry(s *store.Store) (*Registry, error) {
	r := &Registry{items: map[string]*entry{}, store: s}
	var sv saved
	if err := s.Load("printers", &sv); err != nil {
		return nil, err
	}
	for _, p := range sv.Printers {
		d, err := newDriver(p)
		e := &entry{p: p, d: d, bedClear: true, st: Status{State: StateOffline}}
		if err != nil {
			e.st.Message = err.Error()
		}
		if v, ok := sv.BedClear[p.ID]; ok {
			e.bedClear = v
		}
		r.items[p.ID] = e
	}
	return r, nil
}

func (r *Registry) persist() error {
	sv := saved{BedClear: map[string]bool{}}
	for _, e := range r.items {
		sv.Printers = append(sv.Printers, e.p)
		sv.BedClear[e.p.ID] = e.bedClear
	}
	sort.Slice(sv.Printers, func(i, j int) bool { return sv.Printers[i].Name < sv.Printers[j].Name })
	return r.store.Save("printers", sv)
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Put adds or updates a printer.
func (r *Registry) Put(p Printer) (Printer, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return p, fmt.Errorf("name is required")
	}
	d, err := newDriver(p)
	if err != nil {
		return p, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.ID == "" {
		p.ID = newID()
	}
	e, ok := r.items[p.ID]
	if !ok {
		e = &entry{bedClear: true, st: Status{State: StateOffline}}
		r.items[p.ID] = e
	}
	if e.relay != nil && e.relay.running {
		e.relay.mu.Lock()
		e.relay.running = false
		e.relay.cancel()
		e.relay.mu.Unlock()
	}
	e.p, e.d, e.relay = p, d, nil
	return p, r.persist()
}

// Tweak changes settings that don't affect the connection (auto clear, AI)
// without reconnecting to the printer.
func (r *Registry) Tweak(id string, fn func(*Printer)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.items[id]
	if !ok {
		return fmt.Errorf("printer not found")
	}
	fn(&e.p)
	return r.persist()
}

func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, id)
	return r.persist()
}

func (r *Registry) view(e *entry) View {
	v := View{Printer: e.p, Status: e.st, BedClear: e.bedClear, UpdatedAt: e.updated}
	if e.d != nil {
		v.Capabilities = e.d.Capabilities()
	}
	v.Ready = e.st.State == StateIdle && e.bedClear
	switch {
	case AlwaysLevels(e.p.Driver):
		v.LevelAlways, v.CanLevel = true, true
	case NativeLevel(e.p.Driver):
		v.CanLevel = true
	default:
		v.LevelCmd = LevelGcodeFor(e.p)
		v.CanLevel = v.LevelCmd != ""
	}
	if e.p.SnapshotURL != "" {
		v.Status.SnapshotURL = "/api/printers/" + e.p.ID + "/snapshot"
	} else if _, ok := e.d.(Snapshotter); ok && v.Status.SnapshotURL != "" {
		v.Status.SnapshotURL = "/api/printers/" + e.p.ID + "/snapshot"
	} else {
		v.Status.SnapshotURL = ""
	}
	if v.Status.SnapshotURL != "" {
		v.Status.StreamURL = "/api/printers/" + e.p.ID + "/stream"
	}
	if v.APIKey != "" {
		v.APIKey = "••••"
	}
	return v
}

// Driver returns the printer's driver, for features only some drivers have.
func (r *Registry) Driver(id string) (Driver, error) { return r.driver(id) }

func (r *Registry) List() []View {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]View, 0, len(r.items))
	for _, e := range r.items {
		out = append(out, r.view(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) Get(id string) (View, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.items[id]
	if !ok {
		return View{}, false
	}
	return r.view(e), true
}

// Raw returns the stored printer, including its secret key.
func (r *Registry) Raw(id string) (Printer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.items[id]
	if !ok {
		return Printer{}, false
	}
	return e.p, true
}

func (r *Registry) driver(id string) (Driver, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.items[id]
	if !ok {
		return nil, fmt.Errorf("printer not found")
	}
	if e.d == nil {
		return nil, fmt.Errorf("printer has no working driver")
	}
	return e.d, nil
}

// SetBedClear records that the user removed the parts (or not).
func (r *Registry) SetBedClear(id string, clear bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.items[id]
	if !ok {
		return fmt.Errorf("printer not found")
	}
	e.bedClear = clear
	if clear && e.st.State == StateFinished {
		e.st.State = StateIdle
		if r.OnTransition != nil {
			go r.OnTransition(id, StateFinished, StateIdle, true)
		}
	}
	return r.persist()
}

// ClearPlatformAuto is ClearPlatform sent by PrintHarbor on its own ("Auto
// clear platform"). It clears the printer's "print finished" message so the
// printer will take another job, and nothing more: nobody has said the part is
// off the bed, so the bed stays "not cleared" until you, or the camera, say so.
// (FlashBridge, where this comes from, has no "bed clear" of its own. Counting
// an automatic clear as a free bed would let "send plates to ready printers"
// start the next plate on top of the finished part.)
func (r *Registry) ClearPlatformAuto(ctx context.Context, id string) error {
	r.mu.Lock()
	if e, ok := r.items[id]; ok {
		e.autoCleared = time.Now()
	}
	r.mu.Unlock()
	err := r.ClearPlatform(ctx, id)
	if err != nil {
		r.mu.Lock()
		if e, ok := r.items[id]; ok {
			e.autoCleared = time.Time{}
		}
		r.mu.Unlock()
	}
	return err
}

// ClearPlatform tells printers that wait for it (FlashForge) that the bed
// was cleared. Other printers ignore it.
func (r *Registry) ClearPlatform(ctx context.Context, id string) error {
	d, err := r.driver(id)
	if err != nil {
		return err
	}
	if c, ok := d.(PlatformClearer); ok {
		return c.ClearPlatform(ctx)
	}
	return nil
}

// CanClearPlatform reports whether the printer has a "clear platform" step.
func (r *Registry) CanClearPlatform(id string) bool {
	d, err := r.driver(id)
	if err != nil {
		return false
	}
	_, ok := d.(PlatformClearer)
	return ok
}

func (r *Registry) Action(ctx context.Context, id, action string) error {
	d, err := r.driver(id)
	if err != nil {
		return err
	}
	switch action {
	case "pause":
		return d.Pause(ctx)
	case "resume":
		return d.Resume(ctx)
	case "cancel":
		err := d.Cancel(ctx)
		if err == nil {
			r.mu.Lock()
			if e, ok := r.items[id]; ok && e.active {
				e.cancelAsked = true
			}
			r.mu.Unlock()
		}
		return err
	case "light_on", "light_off":
		l, ok := d.(Lighter)
		if !ok {
			return ErrUnsupported
		}
		return l.SetLight(ctx, action == "light_on")
	}
	return fmt.Errorf("unknown action %q", action)
}

// Send uploads a sliced file and optionally starts it. Starting a print marks
// the bed as occupied.
func (r *Registry) Send(ctx context.Context, id, filename string, f io.Reader, start bool) error {
	d, err := r.driver(id)
	if err != nil {
		return err
	}
	if err := d.Upload(ctx, filename, f, start); err != nil {
		return err
	}
	if start {
		r.mu.Lock()
		if e, ok := r.items[id]; ok {
			e.bedClear = false
			e.st.State = StatePrinting
			e.st.JobName = filename
			// A print is now in progress, even before the printer says so.
			e.active, e.seen, e.sentAt = true, false, time.Now()
			e.progress, e.cancelAsked, e.errored = 0, false, false
			_ = r.persist()
		}
		r.mu.Unlock()
	}
	return nil
}

// Snapshot returns a camera frame from the printer.
// relayFor returns the shared camera connection for printers with a live
// stream (nil for snapshot-only cameras or a custom snapshot address).
func (r *Registry) relayFor(id string) *camRelay {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.items[id]
	if !ok || e.d == nil || e.p.SnapshotURL != "" {
		return nil
	}
	s, ok := e.d.(Streamer)
	if !ok || !s.HasStream() {
		return nil
	}
	if e.relay == nil {
		e.relay = newCamRelay(func(ctx context.Context) (io.ReadCloser, error) {
			resp, err := s.Stream(ctx)
			if err != nil {
				return nil, err
			}
			return resp.Body, nil
		})
	}
	return e.relay
}

// Watch subscribes to a printer's live camera. ok is false when the camera
// has no stream (use snapshots instead).
func (r *Registry) Watch(id string) (frames chan []byte, leave func(), lastErr func() string, ok bool) {
	rl := r.relayFor(id)
	if rl == nil {
		return nil, nil, nil, false
	}
	c, leave := rl.subscribe()
	return c, leave, func() string { rl.mu.Lock(); defer rl.mu.Unlock(); return rl.lastErr }, true
}

func (r *Registry) Snapshot(ctx context.Context, id string) ([]byte, string, error) {
	if p, ok := r.Raw(id); ok && p.SnapshotURL != "" {
		return fetchImage(ctx, http.DefaultClient, p.SnapshotURL)
	}
	if rl := r.relayFor(id); rl != nil {
		// Share the live connection (FlashForge cameras allow only one).
		if b, msg := rl.snapshot(ctx); b != nil {
			return b, "image/jpeg", nil
		} else {
			return nil, "", fmt.Errorf("%s", msg)
		}
	}
	d, err := r.driver(id)
	if err != nil {
		return nil, "", err
	}
	s, ok := d.(Snapshotter)
	if !ok {
		return nil, "", ErrUnsupported
	}
	return s.Snapshot(ctx)
}

func (r *Registry) OutputFormat(id string) string {
	d, err := r.driver(id)
	if err != nil {
		return "gcode"
	}
	return d.OutputFormat()
}

// PollOnce refreshes every printer's status in parallel.
func (r *Registry) PollOnce(ctx context.Context) {
	r.mu.RLock()
	type job struct {
		id string
		d  Driver
	}
	var jobs []job
	for id, e := range r.items {
		if e.d != nil {
			jobs = append(jobs, job{id, e.d})
		}
	}
	r.mu.RUnlock()

	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			st, err := j.d.Status(cctx)
			if err != nil {
				st = Status{State: StateOffline, Message: err.Error()}
			}
			r.update(j.id, st)
		}(j)
	}
	wg.Wait()
}

func (r *Registry) update(id string, st Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.items[id]
	if !ok {
		return
	}
	prev := e.st.State
	rawPrev := e.rawState
	e.rawState = st.State
	// Follow the print: note how far it gets, and when it ends work out how.
	var end *PrintEnd
	switch st.State {
	case StatePrinting, StatePaused:
		if !e.active {
			e.active, e.progress, e.cancelAsked, e.errored = true, 0, false, false
		}
		e.seen = true
		if st.Progress > e.progress {
			e.progress = st.Progress
		}
	case StateError:
		if e.active {
			e.errored = true // it may still recover; the end is decided when it settles
		}
	case StateIdle, StateFinished:
		switch {
		case !e.active:
		case !e.seen && time.Since(e.sentAt) < startGrace && !e.cancelAsked:
			// Just sent, and the printer hasn't picked it up yet: what it shows
			// is still its LAST job ("complete", "cancelled", idle). That is not
			// this print ending. Keep showing it as starting.
			st.State, st.Stopped = StatePrinting, false
			if st.JobName == "" {
				st.JobName = e.st.JobName
			}
			e.rawState = StatePrinting
		case !e.seen:
			// It never started (or was stopped before it did).
			end = &PrintEnd{Outcome: EndStopped}
			e.active = false
		default:
			end = &PrintEnd{Outcome: outcomeOf(st.State, st.Stopped || e.cancelAsked, e.errored, e.progress), Progress: e.progress}
			e.active = false
		}
	}
	// Printers that wait for "platform cleared" (FlashForge) going from
	// finished to idle were cleared by a person (at the printer, or with
	// PrintHarbor's Clear platform button): the bed is free.
	if _, ok := e.d.(PlatformClearer); ok && rawPrev == StateFinished && st.State == StateIdle {
		if time.Since(e.autoCleared) < 2*time.Minute {
			e.autoCleared = time.Time{} // PrintHarbor's own automatic clear: the part may still be there (ClearPlatformAuto)
		} else {
			e.bedClear = true
			_ = r.persist()
		}
	}
	// A print that ends leaves parts on the bed.
	if end != nil || (prev == StatePrinting || prev == StatePaused) && (st.State == StateIdle || st.State == StateFinished) {
		e.bedClear = false
		_ = r.persist()
	}
	if st.State == StateFinished && e.bedClear {
		st.State = StateIdle
	}
	if st.State == StateIdle && !e.bedClear {
		st.State = StateFinished
	}
	e.st = st
	e.updated = time.Now()
	if st.State != StateOffline {
		e.history = append(e.history, Sample{T: e.updated.UnixMilli(), Nozzle: st.Nozzle, NozzleTarget: st.NozzleTarget,
			Bed: st.Bed, BedTarget: st.BedTarget, Chamber: st.Chamber})
		if len(e.history) > historyLen {
			e.history = e.history[len(e.history)-historyLen:]
		}
	}
	changed, cur, ready := prev != st.State, st.State, st.State == StateIdle && e.bedClear
	if end != nil || changed {
		onEnd, onChange := r.OnPrintEnd, r.OnTransition
		go func() {
			if end != nil && onEnd != nil {
				onEnd(id, *end)
			}
			if changed && onChange != nil {
				onChange(id, prev, cur, ready)
			}
		}()
	}
}

// Run polls until ctx is cancelled.
func (r *Registry) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	r.PollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.PollOnce(ctx)
		}
	}
}

// History returns recent temperature samples.
func (r *Registry) History(id string) []Sample {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.items[id]; ok {
		return append([]Sample{}, e.history...)
	}
	return []Sample{}
}

// Gcode sends G-code to a printer that accepts it.
func (r *Registry) Gcode(ctx context.Context, id, script string) error {
	d, err := r.driver(id)
	if err != nil {
		return err
	}
	g, ok := d.(GcodeSender)
	if !ok {
		return ErrUnsupported
	}
	return g.SendGcode(ctx, script)
}

func (r *Registry) Console(ctx context.Context, id string) ([]ConsoleLine, error) {
	d, err := r.driver(id)
	if err != nil {
		return nil, err
	}
	c, ok := d.(Consoler)
	if !ok {
		return nil, ErrUnsupported
	}
	return c.Console(ctx)
}

// FileManager returns the printer's file manager, if it has one.
func (r *Registry) FileManager(id string) (FileManager, error) {
	d, err := r.driver(id)
	if err != nil {
		return nil, err
	}
	f, ok := d.(FileManager)
	if !ok {
		return nil, ErrUnsupported
	}
	return f, nil
}
