package printers

import (
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The mock driver pretends to be a printer, including the control page
// (temperatures that heat up, homing, jogging, fans, console, files). Use it
// to try PrintHarbor without hardware, and in tests. Address may be a print
// duration in seconds (default 60).
func init() {
	Register(DriverInfo{Name: "mock", Label: "Simulated printer (for testing)", AddressHint: "print seconds, e.g. 60"},
		func(p Printer) (Driver, error) {
			// "60" = a 60 s print; "60/4" = also four nozzles, like a Snapmaker U1
			a, b, _ := strings.Cut(p.Address, "/")
			secs, _ := strconv.Atoi(a)
			if secs <= 0 {
				secs = 60
			}
			tools, _ := strconv.Atoi(b)
			return &Mock{duration: time.Duration(secs) * time.Second, slots: p.Slots, nozzle: 24, bed: 23, tools: tools,
				pos: []float64{0, 0, 0}, speed: 1, flow: 1, files: map[string]int64{}}, nil
		})
}

type Mock struct {
	mu       sync.Mutex
	duration time.Duration
	slots    []Slot
	job      string
	started  time.Time
	pausedAt time.Time
	paused   time.Duration
	state    State
	Uploads  []string

	nozzle, nozzleT, bed, bedT float64
	lastTick                   time.Time
	pos                        []float64
	homed                      string
	fan, speed, flow, zoff     float64
	light                      bool
	console                    []ConsoleLine
	files                      map[string]int64
	tools                      int
}

func (m *Mock) Capabilities() Capabilities {
	return Capabilities{Status: true, Pause: true, Resume: true, Cancel: true, Upload: true, StartPrint: true, Temperatures: true,
		Gcode: true, Console: true, Files: true, SpeedFactor: true, ZOffset: true, Jog: true, Light: true}
}

func (m *Mock) OutputFormat() string { return "gcode" }

// tick moves temperatures toward their targets.
func (m *Mock) tick() {
	now := time.Now()
	if m.lastTick.IsZero() {
		m.lastTick = now
	}
	dt := now.Sub(m.lastTick).Seconds()
	m.lastTick = now
	approach := func(cur, target, ambient float64) float64 {
		goal := math.Max(target, ambient)
		return goal + (cur-goal)*math.Exp(-dt/12)
	}
	m.nozzle = approach(m.nozzle, m.nozzleT, 24)
	m.bed = approach(m.bed, m.bedT, 23)
}

func (m *Mock) Status(ctx context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tick()
	st := Status{State: StateIdle, Nozzle: round1(m.nozzle), NozzleTarget: m.nozzleT, Bed: round1(m.bed), BedTarget: m.bedT,
		Position: append([]float64(nil), m.pos...), Homed: m.homed, Fan: m.fan, SpeedFactor: m.speed, FlowFactor: m.flow, ZOffset: m.zoff}
	for i := 0; i < m.tools; i++ {
		t := ToolTemp{Temp: 25, Target: 0}
		if i == 0 {
			t = ToolTemp{Temp: round1(m.nozzle), Target: m.nozzleT}
		}
		st.Tools = append(st.Tools, t)
	}
	if m.tools > 0 {
		st.Chamber = 26
	}
	if m.light {
		st.Light = "on"
	} else {
		st.Light = "off"
	}
	switch m.state {
	case StatePrinting, StatePaused:
		el := time.Since(m.started) - m.paused
		if m.state == StatePaused {
			el = m.pausedAt.Sub(m.started) - m.paused
		}
		prog := float64(el) / float64(m.duration)
		if prog >= 1 {
			m.state = StateIdle
			m.nozzleT, m.bedT, m.fan = 0, 0, 0
			st.State, st.JobName, st.Progress = StateFinished, m.job, 1
			return st, nil
		}
		st.State, st.JobName, st.Progress = m.state, m.job, prog
		st.RemainingSec = int((m.duration - el).Seconds())
		st.ElapsedSec = int(el.Seconds())
		st.TotalLayers = 120
		st.Layer = int(prog*120) + 1
		if m.nozzleT == 0 {
			m.nozzleT, m.bedT, m.fan = 215, 60, 1
		}
	}
	return st, nil
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func (m *Mock) Pause(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == StatePrinting {
		m.state, m.pausedAt = StatePaused, time.Now()
	}
	return nil
}

func (m *Mock) Resume(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == StatePaused {
		m.paused += time.Since(m.pausedAt)
		m.state = StatePrinting
	}
	return nil
}

func (m *Mock) Cancel(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = StateIdle
	m.nozzleT, m.bedT, m.fan = 0, 0, 0
	return nil
}

func (m *Mock) SetLight(ctx context.Context, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.light = on
	return nil
}

func (m *Mock) Upload(ctx context.Context, name string, r io.Reader, start bool) error {
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Uploads = append(m.Uploads, name)
	m.files[name] = n
	if start {
		m.startLocked(name)
	}
	return nil
}

func (m *Mock) startLocked(name string) {
	m.job, m.started, m.paused, m.state = name, time.Now(), 0, StatePrinting
}

func (m *Mock) log(typ, msg string) {
	m.console = append(m.console, ConsoleLine{Time: float64(time.Now().UnixMilli()) / 1000, Message: msg, Type: typ})
	if len(m.console) > 200 {
		m.console = m.console[len(m.console)-200:]
	}
}

// SendGcode understands the handful of commands the control page sends.
func (m *Mock) SendGcode(ctx context.Context, script string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rel := false
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m.log("command", line)
		f := strings.Fields(strings.ToUpper(line))
		arg := func(p string) (float64, bool) {
			for _, x := range f[1:] {
				if strings.HasPrefix(x, p) {
					v, err := strconv.ParseFloat(x[len(p):], 64)
					return v, err == nil
				}
			}
			return 0, false
		}
		switch f[0] {
		case "M104", "M109":
			if v, ok := arg("S"); ok {
				m.nozzleT = v
			}
		case "M140", "M190":
			if v, ok := arg("S"); ok {
				m.bedT = v
			}
		case "G28":
			axes := "xyz"
			if len(f) > 1 {
				axes = ""
				for _, x := range f[1:] {
					axes += strings.ToLower(x)
				}
			}
			for i, a := range "xyz" {
				if strings.ContainsRune(axes, a) {
					m.pos[i] = 0
					if !strings.ContainsRune(m.homed, a) {
						m.homed += string(a)
					}
				}
			}
			m.homed = sortAxes(m.homed)
		case "G91":
			rel = true
		case "G90":
			rel = false
		case "G0", "G1":
			for i, a := range []string{"X", "Y", "Z"} {
				if v, ok := arg(a); ok {
					if rel {
						m.pos[i] += v
					} else {
						m.pos[i] = v
					}
					m.pos[i] = math.Max(0, math.Round(m.pos[i]*100)/100)
				}
			}
		case "M84", "M18":
			m.homed = ""
		case "M106":
			if v, ok := arg("S"); ok {
				m.fan = math.Min(1, v/255)
			}
		case "M107":
			m.fan = 0
		case "M220":
			if v, ok := arg("S"); ok {
				m.speed = v / 100
			}
		case "M221":
			if v, ok := arg("S"); ok {
				m.flow = v / 100
			}
		case "SET_GCODE_OFFSET":
			for _, x := range f[1:] {
				if strings.HasPrefix(x, "Z_ADJUST=") {
					v, _ := strconv.ParseFloat(strings.TrimPrefix(x, "Z_ADJUST="), 64)
					m.zoff = math.Round((m.zoff+v)*1000) / 1000
				}
			}
		default:
			m.log("response", fmt.Sprintf("// Unknown command: %s", f[0]))
			continue
		}
		m.log("response", "ok")
	}
	return nil
}

func sortAxes(s string) string {
	b := []byte(s)
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	return string(b)
}

func (m *Mock) Console(ctx context.Context) ([]ConsoleLine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ConsoleLine(nil), m.console...), nil
}

func (m *Mock) Files(ctx context.Context) ([]PrinterFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []PrinterFile{}
	for n, s := range m.files {
		out = append(out, PrinterFile{Name: n, Size: s, Modified: float64(time.Now().Unix())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Mock) StartFile(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[name]; !ok {
		return fmt.Errorf("file not found")
	}
	m.startLocked(name)
	return nil
}

func (m *Mock) DeleteFile(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, name)
	return nil
}
