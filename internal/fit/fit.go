// Package fit decides which printers can print a plate, and explains why.
//
// Every result says what's stopping a printer (blockers), what the user
// could do to make it work (actions), and whether it's free right now.
package fit

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/thefrayedend/printharbor/internal/printers"
	"github.com/thefrayedend/printharbor/internal/profiles"
	"github.com/thefrayedend/printharbor/internal/threemf"
)

type Level string

const (
	Ready       Level = "ready"        // can start now
	NeedsAction Level = "needs_action" // works after the user does something (load filament...)
	Busy        Level = "busy"         // capable but printing / offline / bed not cleared
	Cannot      Level = "cannot"       // hardware can't do it
)

type ReasonKind string

const (
	Blocker ReasonKind = "blocker"
	Action  ReasonKind = "action"
	Wait    ReasonKind = "wait"
	Info    ReasonKind = "info"
	Good    ReasonKind = "good"
)

type Reason struct {
	Kind ReasonKind `json:"kind"`
	Text string     `json:"text"`
}

// Mapping links one project filament (a color role) to a printer slot.
type Mapping struct {
	Filament int    `json:"filament"` // project filament index
	Slot     int    `json:"slot"`     // printer slot index, 0 = none
	Exact    bool   `json:"exact"`
	Want     string `json:"want"`
	Have     string `json:"have,omitempty"`
}

// Lock pins a project filament (color role) to one specific spool, e.g. so
// every copy of a part comes out of the same batch of filament.
type Lock struct {
	SpoolID int    `json:"spool_id"`
	Label   string `json:"label"`
}

// Options carries the extras a fit check can take into account.
type Options struct {
	// Pending maps printer ID → note about work already headed its way.
	Pending map[string]string
	// Locks maps project filament index → required spool.
	Locks map[int]Lock
}

type Result struct {
	EstimateSec int       `json:"estimate_sec"` // rough, from part volume and nozzle size
	PrinterID   string    `json:"printer_id"`
	PrinterName string    `json:"printer_name"`
	Level       Level     `json:"level"`
	Reasons     []Reason  `json:"reasons"`
	Mapping     []Mapping `json:"mapping"`
	BedArea     float64   `json:"bed_area_mm2"`
}

// MachineLookup returns bed/nozzle data for an Orca machine preset.
type MachineLookup func(preset string) (profiles.MachineInfo, error)

// Plate checks one plate against every printer and returns them best first.
// Printers with work pending (opts.Pending) count as busy.
func Plate(pl threemf.Plate, fils []threemf.Filament, ps []printers.View, lookup MachineLookup, opts Options) []Result {
	out := make([]Result, 0, len(ps))
	for _, p := range ps {
		r := CheckWith(pl, fils, p, lookup, opts.Locks)
		if note, ok := opts.Pending[p.ID]; ok && r.Level != Cannot {
			var keep []Reason
			for _, rs := range r.Reasons {
				if rs.Kind != Good {
					keep = append(keep, rs)
				}
			}
			r.Reasons = append(keep, Reason{Wait, note})
			r.Level = Busy
		}
		out = append(out, r)
	}
	rank := map[Level]int{Ready: 0, NeedsAction: 1, Busy: 2, Cannot: 3}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Level] != rank[out[j].Level] {
			return rank[out[i].Level] < rank[out[j].Level]
		}
		// Smallest printer that fits first: keep big machines free for big parts.
		return out[i].BedArea < out[j].BedArea
	})
	return out
}

func Check(pl threemf.Plate, fils []threemf.Filament, p printers.View, lookup MachineLookup) Result {
	return CheckWith(pl, fils, p, lookup, nil)
}

// Estimate gives a rough print time from part volume and nozzle size. It's
// for comparing printers, not a slicer estimate.
func Estimate(pl threemf.Plate, nozzle float64) int {
	vol := 0.0
	for _, part := range pl.Parts {
		vol += part.SizeX * part.SizeY * part.SizeZ * 0.22 // typical solid fraction with walls + infill
	}
	if nozzle <= 0 {
		nozzle = 0.4
	}
	rate := 9.0 * (nozzle / 0.4) * (nozzle / 0.4) // mm³/s
	return int(vol/rate*1.25) + 240               // travel/overhead + heat-up
}

func CheckWith(pl threemf.Plate, fils []threemf.Filament, p printers.View, lookup MachineLookup, locks map[int]Lock) Result {
	r := Result{PrinterID: p.ID, PrinterName: p.Name, Reasons: []Reason{}, Mapping: []Mapping{}}
	add := func(k ReasonKind, f string, a ...any) { r.Reasons = append(r.Reasons, Reason{k, fmt.Sprintf(f, a...)}) }

	// --- capable ---
	if p.MachinePreset == "" {
		add(Blocker, "Choose an Orca printer profile for this printer")
		r.Level = Cannot
		return r
	}
	m, err := lookup(p.MachinePreset)
	if err != nil {
		add(Blocker, "Printer profile problem: %v", err)
		r.Level = Cannot
		return r
	}
	r.BedArea = m.BedX * m.BedY
	r.EstimateSec = Estimate(pl, m.Nozzle)
	for _, part := range pl.Parts {
		fits := (part.SizeX <= m.BedX && part.SizeY <= m.BedY) || (part.SizeY <= m.BedX && part.SizeX <= m.BedY)
		if !fits {
			add(Blocker, "%q is %s × %s mm, bed is %s × %s mm", part.Name, mm(part.SizeX), mm(part.SizeY), mm(m.BedX), mm(m.BedY))
		}
		if m.MaxZ > 0 && part.SizeZ > m.MaxZ {
			add(Blocker, "%q is %s mm tall, max height is %s mm", part.Name, mm(part.SizeZ), mm(m.MaxZ))
		}
	}
	if r.BedArea > 0 && pl.FootprintMM2 > 0.8*r.BedArea {
		add(Info, "Tight fit: parts cover %d%% of the bed; arranging may need to split this plate", int(100*pl.FootprintMM2/r.BedArea))
	}

	// --- configured: filament roles -> slots ---
	slots := p.LoadedSlots()
	capacity := len(slots)
	if capacity == 0 {
		capacity = m.Extruders
	}
	if capacity < 1 {
		capacity = 1
	}
	need := pl.Filaments
	if len(need) == 0 {
		need = []int{1}
	}
	if len(need) > capacity {
		add(Blocker, "Needs %d filaments, this printer holds %d", len(need), capacity)
	}
	if pl.Painted && len(fils) > capacity {
		add(Info, "Has painted colors; the project defines %d filaments", len(fils))
	}
	used := map[int]bool{}
	for _, idx := range need {
		want := filamentByIndex(fils, idx)
		mp := Mapping{Filament: idx, Want: describe(want.Type, want.Color)}
		if lk, ok := locks[idx]; ok && lk.SpoolID > 0 {
			mp.Want = lk.Label
			found := 0
			for _, s := range slots {
				if s.SpoolID == lk.SpoolID && !used[s.Index] {
					found = s.Index
					break
				}
			}
			if found > 0 {
				used[found] = true
				mp.Slot, mp.Exact, mp.Have = found, true, lk.Label
			} else {
				add(Action, "Load spool #%d (%s) for filament %d", lk.SpoolID, lk.Label, idx)
			}
			r.Mapping = append(r.Mapping, mp)
			continue
		}
		best, bestD := 0, math.MaxFloat64
		for _, s := range slots {
			if used[s.Index] || !sameType(s.Type, want.Type) {
				continue
			}
			if d := colorDist(s.Color, want.Color); d < bestD {
				best, bestD = s.Index, d
			}
		}
		if best > 0 {
			used[best] = true
			mp.Slot = best
			s := slotByIndex(slots, best)
			mp.Have = describe(s.Type, s.Color)
			mp.Exact = bestD < 60 // close enough to call it the same color
			if !mp.Exact {
				add(Action, "Load %s for filament %d (closest loaded: %s in slot %d)", mp.Want, idx, mp.Have, best)
			}
		} else if len(slots) == 0 {
			add(Info, "Loaded filament unknown: make sure %s is loaded", mp.Want)
		} else {
			add(Action, "Load %s for filament %d", mp.Want, idx)
		}
		r.Mapping = append(r.Mapping, mp)

		t := strings.ToUpper(want.Type)
		if (strings.Contains(t, "CF") || strings.Contains(t, "GF")) && !p.HardenedNozzle {
			add(Action, "%s needs a hardened nozzle", want.Type)
		}
		if needsEnclosure(t) && !p.Enclosed {
			add(Info, "%s prints best in an enclosed printer", want.Type)
		}
	}

	// --- available ---
	st := p.Status
	switch st.State {
	case printers.StateOffline:
		add(Wait, "Offline")
	case printers.StatePrinting:
		msg := fmt.Sprintf("Printing, %d%%", int(st.Progress*100))
		if st.RemainingSec > 0 {
			msg += ", " + dur(st.RemainingSec) + " left"
		}
		add(Wait, "%s", msg)
	case printers.StatePaused:
		add(Wait, "Paused")
	case printers.StateFinished:
		add(Wait, "Finished: remove the parts, then mark the bed clear")
	case printers.StateError:
		add(Wait, "Printer error: %s", st.Message)
	case printers.StateIdle:
		if !p.BedClear {
			add(Wait, "Bed not marked clear")
		}
	}

	// Precedence: can't > busy (not free now anyway) > needs action > ready.
	r.Level = Ready
	sev := map[Level]int{Ready: 0, NeedsAction: 1, Busy: 2, Cannot: 3}
	for _, rs := range r.Reasons {
		l := Ready
		switch rs.Kind {
		case Blocker:
			l = Cannot
		case Wait:
			l = Busy
		case Action:
			l = NeedsAction
		}
		if sev[l] > sev[r.Level] {
			r.Level = l
		}
	}
	if r.Level == Cannot {
		// Only the deal-breakers matter; skip "load filament" noise.
		var bl []Reason
		for _, rs := range r.Reasons {
			if rs.Kind == Blocker {
				bl = append(bl, rs)
			}
		}
		r.Reasons = bl
	}
	if r.Level == Ready {
		add(Good, "Ready now")
	}
	return r
}

func filamentByIndex(fs []threemf.Filament, i int) threemf.Filament {
	for _, f := range fs {
		if f.Index == i {
			return f
		}
	}
	return threemf.Filament{Index: i, Type: "PLA"}
}

func slotByIndex(ss []printers.Slot, i int) printers.Slot {
	for _, s := range ss {
		if s.Index == i {
			return s
		}
	}
	return printers.Slot{}
}

func sameType(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToUpper(strings.TrimSpace(s))
		s = strings.ReplaceAll(s, "-", "")
		s = strings.ReplaceAll(s, " ", "")
		// "PLA+" / "PLA BASIC" / "PLA MATTE" all print as PLA for planning.
		for _, base := range []string{"PLA", "PETG", "ABS", "ASA", "TPU", "PC", "PA", "PET", "PVA", "HIPS"} {
			if strings.HasPrefix(s, base) && !strings.Contains(s, "CF") && !strings.Contains(s, "GF") {
				return base
			}
		}
		return s
	}
	if a == "" || b == "" {
		return true
	}
	return norm(a) == norm(b)
}

func needsEnclosure(t string) bool {
	for _, x := range []string{"ABS", "ASA", "PC", "PA", "NYLON"} {
		if strings.HasPrefix(t, x) {
			return true
		}
	}
	return false
}

func rgb(hex string) (float64, float64, float64, bool) {
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) == 8 { // #RRGGBBAA
		h = h[:6]
	}
	if len(h) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return float64(v >> 16 & 255), float64(v >> 8 & 255), float64(v & 255), true
}

// colorDist is a cheap perceptual distance ("redmean").
func colorDist(a, b string) float64 {
	r1, g1, b1, ok1 := rgb(a)
	r2, g2, b2, ok2 := rgb(b)
	if !ok1 || !ok2 {
		return 0 // unknown color: don't block on it
	}
	rm := (r1 + r2) / 2
	dr, dg, db := r1-r2, g1-g2, b1-b2
	return math.Sqrt((2+rm/256)*dr*dr + 4*dg*dg + (2+(255-rm)/256)*db*db)
}

var named = []struct {
	name string
	hex  string
}{
	{"black", "#000000"}, {"white", "#FFFFFF"}, {"grey", "#808080"}, {"silver", "#C0C0C0"},
	{"red", "#E02020"}, {"orange", "#FF8C00"}, {"yellow", "#FFE000"}, {"gold", "#D4AF37"},
	{"green", "#20A040"}, {"dark green", "#0B5D1E"}, {"blue", "#1E5BD8"}, {"navy", "#0A1F5C"},
	{"cyan", "#20C0E0"}, {"purple", "#7B2FBE"}, {"pink", "#FF7FB0"}, {"brown", "#7B4A26"},
	{"beige", "#E8D8B0"},
}

// ColorName gives a friendly name for a hex color.
func ColorName(hex string) string {
	if _, _, _, ok := rgb(hex); !ok {
		return ""
	}
	best, bd := "", math.MaxFloat64
	for _, n := range named {
		if d := colorDist(hex, n.hex); d < bd {
			best, bd = n.name, d
		}
	}
	return best
}

func describe(typ, color string) string {
	n := ColorName(color)
	switch {
	case n != "" && typ != "":
		return n + " " + typ
	case typ != "":
		return typ
	case n != "":
		return n
	}
	return "filament"
}

func mm(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) }

func dur(sec int) string {
	h, m := sec/3600, (sec%3600)/60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}
