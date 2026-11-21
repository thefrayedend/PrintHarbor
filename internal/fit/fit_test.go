package fit

import (
	"fmt"
	"strings"
	"testing"

	"github.com/thefrayedend/printharbor/internal/printers"
	"github.com/thefrayedend/printharbor/internal/profiles"
	"github.com/thefrayedend/printharbor/internal/threemf"
)

func lookup(preset string) (profiles.MachineInfo, error) {
	switch preset {
	case "big":
		return profiles.MachineInfo{BedX: 350, BedY: 320, MaxZ: 325, Extruders: 1}, nil
	case "small":
		return profiles.MachineInfo{BedX: 220, BedY: 220, MaxZ: 220, Extruders: 1}, nil
	}
	return profiles.MachineInfo{}, fmt.Errorf("no preset")
}

func view(name, preset string, state printers.State, clear bool, slots ...printers.Slot) printers.View {
	return printers.View{Printer: printers.Printer{ID: name, Name: name, MachinePreset: preset, Slots: slots},
		Status: printers.Status{State: state}, BedClear: clear}
}

func has(r Result, kind ReasonKind, substr string) bool {
	for _, x := range r.Reasons {
		if x.Kind == kind && strings.Contains(x.Text, substr) {
			return true
		}
	}
	return false
}

func TestFit(t *testing.T) {
	fils := []threemf.Filament{{Index: 1, Type: "PLA", Color: "#000000"}, {Index: 2, Type: "PLA", Color: "#D4AF37"}}
	plate := threemf.Plate{Parts: []threemf.Part{{Name: "Arm", SizeX: 240, SizeY: 100, SizeZ: 230}}, Filaments: []int{1, 2}, FootprintMM2: 24000}

	ams := []printers.Slot{{Index: 1, Type: "PLA", Color: "#050505"}, {Index: 2, Type: "PLA Basic", Color: "#FFFFFF"}, {Index: 3, Type: "PETG", Color: "#D4AF37"}}
	ps := []printers.View{
		view("small", "small", printers.StateIdle, true),
		view("bigAMS", "big", printers.StateIdle, true, ams...),
		view("busyBig", "big", printers.StatePrinting, true, printers.Slot{Index: 1, Type: "PLA", Color: "#000000"}, printers.Slot{Index: 2, Type: "PLA", Color: "#D4AF37"}),
		view("noProfile", "", printers.StateIdle, true),
	}
	res := Plate(plate, fils, ps, lookup, Options{})
	by := map[string]Result{}
	for _, r := range res {
		by[r.PrinterID] = r
	}
	if by["small"].Level != Cannot || !has(by["small"], Blocker, "bed is 220") || !has(by["small"], Blocker, "max height") {
		t.Fatalf("small: %+v", by["small"])
	}
	if by["bigAMS"].Level != NeedsAction || !has(by["bigAMS"], Action, "Load gold PLA") {
		t.Fatalf("bigAMS: %+v", by["bigAMS"])
	}
	if by["bigAMS"].Mapping[0].Slot != 1 || !by["bigAMS"].Mapping[0].Exact {
		t.Fatalf("black should map to slot 1: %+v", by["bigAMS"].Mapping)
	}
	if by["busyBig"].Level != Busy || !has(by["busyBig"], Wait, "Printing") {
		t.Fatalf("busyBig: %+v", by["busyBig"])
	}
	if by["noProfile"].Level != Cannot {
		t.Fatal("no profile must block")
	}
	if res[0].PrinterID != "bigAMS" || res[len(res)-1].Level != Cannot {
		t.Fatalf("bad order: %v", res)
	}
}

func TestReadyAndSmallestFirst(t *testing.T) {
	plate := threemf.Plate{Parts: []threemf.Part{{Name: "Clip", SizeX: 20, SizeY: 20, SizeZ: 5}}, Filaments: []int{1}}
	ps := []printers.View{view("big", "big", printers.StateIdle, true), view("small", "small", printers.StateIdle, true),
		view("dirty", "small", printers.StateFinished, false)}
	res := Plate(plate, nil, ps, lookup, Options{})
	if p := Plate(plate, nil, ps[:1], lookup, Options{Pending: map[string]string{"big": "Slicing a job for this printer"}}); p[0].Level != Busy {
		t.Fatalf("pending job should make printer busy: %+v", p[0])
	}
	if res[0].PrinterID != "small" || res[1].PrinterID != "big" {
		t.Fatalf("smallest ready printer should come first: %v", res)
	}
	// No slots configured: ready, with a reminder.
	if res[0].Level != Ready || !has(res[0], Info, "make sure") {
		t.Fatalf("%+v", res[0])
	}
	if res[2].PrinterID != "dirty" || res[2].Level != Busy || !has(res[2], Wait, "remove the parts") {
		t.Fatalf("%+v", res[2])
	}
}

func TestColorName(t *testing.T) {
	if ColorName("#D4AF37") != "gold" || ColorName("#FEFEFE") != "white" || ColorName("bad") != "" {
		t.Fatal(ColorName("#D4AF37"), ColorName("#FEFEFE"))
	}
}

func TestLocksAndEstimate(t *testing.T) {
	fils := []threemf.Filament{{Index: 1, Type: "PLA", Color: "#FF0000"}}
	plate := threemf.Plate{Parts: []threemf.Part{{Name: "Cube", SizeX: 20, SizeY: 20, SizeZ: 20}}, Filaments: []int{1}}
	red := printers.Slot{Index: 2, Type: "PLA", Color: "#FF0000", SpoolID: 7}
	other := printers.Slot{Index: 1, Type: "PLA", Color: "#FF0000", SpoolID: 3}
	locks := Options{Locks: map[int]Lock{1: {SpoolID: 7, Label: "Polymaker Red"}}}
	res := Plate(plate, fils, []printers.View{view("a", "small", printers.StateIdle, true, other, red)}, lookup, locks)
	if res[0].Level != Ready || res[0].Mapping[0].Slot != 2 {
		t.Fatalf("locked spool should map to slot 2: %+v", res[0])
	}
	res = Plate(plate, fils, []printers.View{view("b", "small", printers.StateIdle, true, other)}, lookup, locks)
	if res[0].Level != NeedsAction || !has(res[0], Action, "spool #7") {
		t.Fatalf("missing locked spool must ask to load it: %+v", res[0])
	}
	if e := Estimate(plate, 0.4); e < 300 || e > 2000 {
		t.Fatal("estimate", e)
	}
	if Estimate(plate, 0.8) >= Estimate(plate, 0.4) {
		t.Fatal("bigger nozzle should be faster")
	}
}
