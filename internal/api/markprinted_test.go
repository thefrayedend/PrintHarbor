package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A plate that finished printing and waits for "how did it come out?" counts
// as printed once you press Mark printed: the real print is accepted, no
// extra hand-marked copy is added, and the plate shows done.
func TestMarkPrintedAcceptsAFinishedPrint(t *testing.T) {
	a := libApp(t, t.TempDir())
	tmp := filepath.Join(t.TempDir(), "kit.3mf")
	os.WriteFile(tmp, project3mf(t), 0o644)
	p, err := a.AddProject([]Upload{{Name: "Kit.3mf", Tmp: tmp}})
	if err != nil {
		t.Fatal(err)
	}
	a.runsMu.Lock()
	a.runs = append(a.runs, &Run{ID: runID(), ProjectID: p.ID, ProjectName: p.Name, Plate: 1, PrinterName: "U1", State: "printed", Finished: time.Now()})
	a.runsMu.Unlock()
	pr := func() PlateProgress {
		for _, x := range a.Progress(p) {
			if x.Plate == 1 {
				return x
			}
		}
		t.Fatal("no plate 1")
		return PlateProgress{}
	}
	if x := pr(); x.Accepted != 0 || x.Review != 1 {
		t.Fatalf("before: %+v", x)
	}
	if err := a.MarkPrinted(p.ID, 1, true); err != nil {
		t.Fatal(err)
	}
	if x := pr(); x.Accepted != 1 || x.Review != 0 || x.Manual != 0 || x.Remaining != 0 {
		t.Fatalf("after marking: %+v", x)
	}
	// Plate 2 was never printed: marking adds one copy marked by hand, unmark takes it away.
	if err := a.MarkPrinted(p.ID, 2, true); err != nil {
		t.Fatal(err)
	}
	for _, x := range a.Progress(p) {
		if x.Plate == 2 && (x.Accepted != 1 || x.Manual != 1) {
			t.Fatalf("plate 2 marked: %+v", x)
		}
	}
	a.MarkPrinted(p.ID, 2, false)
	for _, x := range a.Progress(p) {
		if x.Plate == 2 && x.Accepted != 0 {
			t.Fatalf("plate 2 unmarked: %+v", x)
		}
		if x.Plate == 1 && x.Accepted != 1 {
			t.Fatalf("unmarking plate 2 touched plate 1: %+v", x)
		}
	}
}
