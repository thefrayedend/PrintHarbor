package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"github.com/thefrayedend/printharbor/internal/fit"
	"github.com/thefrayedend/printharbor/internal/printers"
	"github.com/thefrayedend/printharbor/internal/slicer"
	"github.com/thefrayedend/printharbor/internal/threemf"
)

// Run is one physical print of a plate: sent → printed → accepted / failed.
// "Printer finished" and "you accepted the parts" are separate on purpose.
type Run struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	ProjectName string    `json:"project_name"`
	Plate       int       `json:"plate"`
	PrinterID   string    `json:"printer_id"`
	PrinterName string    `json:"printer_name"`
	JobID       string    `json:"job_id"`
	State       string    `json:"state"`             // printing | printed | accepted | failed
	Manual      bool      `json:"manual,omitempty"`  // marked printed by hand, not an actual print
	Stopped     bool      `json:"stopped,omitempty"` // failed because the print was cancelled part-way
	Started     time.Time `json:"started"`
	Finished    time.Time `json:"finished,omitempty"`
	// From the slice, kept here because the job itself is cleaned up after a while.
	PrintTimeSec int     `json:"print_time_sec,omitempty"`
	FilamentG    float64 `json:"filament_g,omitempty"`
}

// PlateProgress summarizes a plate's runs against how many copies are needed.
type PlateProgress struct {
	Plate     int   `json:"plate"`
	Needed    int   `json:"needed"`
	Accepted  int   `json:"accepted"`
	Manual    int   `json:"manual"` // of Accepted, how many were marked printed by hand
	Printing  int   `json:"printing"`
	Review    int   `json:"review"` // printed, waiting for you to check the parts
	Failed    int   `json:"failed"`
	Remaining int   `json:"remaining"`
	Runs      []Run `json:"runs"`
}

func runID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *App) loadRuns() {
	var runs []*Run
	_ = a.Store.Load("runs", &runs)
	a.runs = runs
}

func (a *App) saveRunsLocked() { _ = a.Store.Save("runs", a.runs) }

// startRun records a plate that started printing.
func (a *App) startRun(j *slicer.Job) {
	a.runsMu.Lock()
	defer a.runsMu.Unlock()
	// A run still marked "printing" on this printer can't be: the printer just
	// took a new job. PrintHarbor missed its end (the printer was unreachable
	// then). Hand it to "check the parts" so it never gets mistaken for the
	// print that ends next.
	for _, r := range a.runs {
		if r.PrinterID == j.PrinterID && r.State == "printing" {
			r.State, r.Finished = "printed", time.Now()
		}
	}
	a.runs = append(a.runs, &Run{ID: runID(), ProjectID: j.ProjectID, ProjectName: j.ProjectName, Plate: j.Plate,
		PrinterID: j.PrinterID, PrinterName: j.PrinterName, JobID: j.ID, State: "printing", Started: time.Now(),
		PrintTimeSec: j.PrintTimeSec, FilamentG: j.FilamentG})
	a.saveRunsLocked()
	a.emitEvent("print_started", j.PrinterID, map[string]any{
		"project_id": j.ProjectID, "project_name": j.ProjectName, "plate": j.Plate, "job_id": j.ID})
}

// printEnded is what the last print on a printer came to, kept briefly so the
// "finished" alert that follows can word itself.
type printEnded struct {
	run     *Run // nil when PrintHarbor didn't start that print
	outcome string
	at      time.Time
}

// onPrintEnd closes the run of a print that ended, according to HOW it ended:
// a completed print waits for you to check the parts and (if write-back is on)
// is deducted from its spools; a stopped or failed one counts as not printed
// and nothing is deducted.
func (a *App) onPrintEnd(id string, end printers.PrintEnd) {
	failed := end.Outcome == printers.EndStopped || end.Outcome == printers.EndFailed
	a.runsMu.Lock()
	var done *Run
	for i := len(a.runs) - 1; i >= 0; i-- {
		r := a.runs[i]
		if r.PrinterID == id && r.State == "printing" {
			r.State, r.Finished = "printed", time.Now()
			if failed {
				r.State, r.Stopped = "failed", end.Outcome == printers.EndStopped
			}
			cp := *r
			done = &cp
			break
		}
	}
	if a.ended == nil {
		a.ended = map[string]printEnded{}
	}
	a.ended[id] = printEnded{run: done, outcome: end.Outcome, at: time.Now()}
	a.saveRunsLocked()
	a.runsMu.Unlock()

	ev := map[string]any{"outcome": end.Outcome, "progress": end.Progress}
	if done != nil {
		ev["project_id"], ev["project_name"], ev["plate"] = done.ProjectID, done.ProjectName, done.Plate
	}
	switch end.Outcome {
	case printers.EndStopped:
		a.emitEvent("print_cancelled", id, ev)
	case printers.EndFailed:
		// print_failed went out the moment the printer reported the error.
	default:
		a.emitEvent("print_finished", id, ev)
	}
	// Only a print known to have completed is deducted from Spoolman.
	if end.Outcome == printers.EndCompleted && done != nil {
		if job, ok := a.Queue.Get(done.JobID); ok {
			go a.spoolmanUse(job)
		}
	}
}

// onPrinterTransition reacts to a printer changing state: tells plugins, raises
// the "remove the parts" alert, and feeds auto-dispatch when a printer becomes
// ready. Closing the print's run is onPrintEnd, which runs just before this.
func (a *App) onPrinterTransition(id string, prev, cur printers.State, ready bool) {
	if prev != cur {
		a.emitEvent("printer_state", id, map[string]any{"from": string(prev), "to": string(cur)})
	}
	if (prev == printers.StatePrinting || prev == printers.StatePaused) && cur == printers.StateError {
		a.emitEvent("print_failed", id, map[string]any{"reason": "the printer reported an error"})
	}
	if prev != printers.StateFinished && cur == printers.StateFinished {
		a.runsMu.Lock()
		e, ok := a.ended[id]
		delete(a.ended, id)
		a.runsMu.Unlock()
		if ok && time.Since(e.at) < time.Minute {
			what := "Print finished."
			if e.run != nil {
				what = fmt.Sprintf("Finished %s, plate %d.", e.run.ProjectName, e.run.Plate)
			}
			if e.outcome == printers.EndStopped || e.outcome == printers.EndFailed {
				what = "The print was stopped before it finished."
				if e.run != nil {
					what = fmt.Sprintf("%s, plate %d was stopped before it finished.", e.run.ProjectName, e.run.Plate)
				}
			}
			frame, _, _ := a.Printers.Snapshot(context.Background(), id)
			a.Alert(id, "info", what+" Remove the parts to free the printer.", frame)
			if p, ok := a.Printers.Raw(id); ok && p.ClearFinished && a.Printers.CanClearPlatform(id) {
				go func() {
					time.Sleep(8 * time.Second) // like FlashBridge: after the finish alert
					if v, _ := a.Printers.Get(id); v.Status.State != printers.StateFinished {
						return // already cleared, or a new print started
					}
					// The printer's message only. The bed is not marked clear: nobody has
					// said the part is off it (see Registry.ClearPlatformAuto).
					if err := a.Printers.ClearPlatformAuto(context.Background(), id); err != nil {
						log.Printf("auto clear platform %s: %v", p.Name, err)
					}
				}()
			}
		}
	}
	if ready {
		for _, p := range a.Projects() {
			if p.AutoDispatch {
				if as, err := a.Dispatch(p.ID); err == nil && len(as) > 0 {
					log.Printf("auto-dispatch %s: sent %d plate(s)", p.Name, len(as))
				}
			}
		}
	}
}

// ReviewRun records whether a finished print's parts were good.
func (a *App) ReviewRun(id string, good bool) (Run, error) {
	a.runsMu.Lock()
	defer a.runsMu.Unlock()
	for _, r := range a.runs {
		if r.ID == id {
			if good {
				r.State = "accepted"
			} else {
				r.State = "failed"
			}
			if r.Finished.IsZero() {
				r.Finished = time.Now()
			}
			a.saveRunsLocked()
			return *r, nil
		}
	}
	return Run{}, fmt.Errorf("print not found")
}

// Runs lists runs, newest first, optionally for one printer or project.
func (a *App) Runs(printerID, projectID string) []Run {
	a.runsMu.Lock()
	defer a.runsMu.Unlock()
	out := []Run{}
	for i := len(a.runs) - 1; i >= 0; i-- {
		r := a.runs[i]
		if (printerID == "" || r.PrinterID == printerID) && (projectID == "" || r.ProjectID == projectID) {
			out = append(out, *r)
		}
	}
	return out
}

func copiesFor(p *Project, plate int) int {
	if n, ok := p.Copies[plate]; ok && n >= 0 {
		return n
	}
	return 1
}

// Progress returns per-plate progress for a project.
func (a *App) Progress(p *Project) []PlateProgress {
	runs := a.Runs("", p.ID)
	// Jobs still slicing/sending with "start" count as printing too.
	inflight := map[int]int{}
	for _, j := range a.Queue.List() {
		if j.ProjectID == p.ID && j.Start && (j.State == slicer.Queued || j.State == slicer.Slicing || j.State == slicer.Sending) {
			inflight[j.Plate]++
		}
	}
	out := []PlateProgress{}
	for _, pl := range p.Data.Plates {
		pp := PlateProgress{Plate: pl.Index, Needed: copiesFor(p, pl.Index), Runs: []Run{}, Printing: inflight[pl.Index]}
		for _, r := range runs {
			if r.Plate != pl.Index {
				continue
			}
			pp.Runs = append(pp.Runs, r)
			switch r.State {
			case "accepted":
				pp.Accepted++
				if r.Manual {
					pp.Manual++
				}
			case "printing":
				pp.Printing++
			case "printed":
				pp.Review++
			case "failed":
				pp.Failed++
			}
		}
		pp.Remaining = pp.Needed - pp.Accepted - pp.Printing - pp.Review
		if pp.Remaining < 0 {
			pp.Remaining = 0
		}
		out = append(out, pp)
	}
	return out
}

// MarkPrinted marks a plate as printed by hand (done=true), or undoes that
// (done=false). Marking adds "accepted" runs tagged Manual to fill whatever is
// still remaining, so the plate counts as done without going to a printer;
// unmarking removes only the hand-marked runs, leaving real print history.
func (a *App) MarkPrinted(projectID string, plate int, done bool) error {
	p, ok := a.Project(projectID)
	if !ok {
		return fmt.Errorf("project not found")
	}
	a.runsMu.Lock()
	defer a.runsMu.Unlock()
	if !done {
		kept := a.runs[:0]
		for _, r := range a.runs {
			if r.ProjectID == projectID && r.Plate == plate && r.Manual {
				continue
			}
			kept = append(kept, r)
		}
		a.runs = kept
		a.saveRunsLocked()
		return nil
	}
	acc, pr, rev := 0, 0, 0
	for _, r := range a.runs {
		if r.ProjectID != projectID || r.Plate != plate {
			continue
		}
		switch r.State {
		case "accepted":
			acc++
		case "printing":
			pr++
		case "printed":
			rev++
		}
	}
	for _, j := range a.Queue.List() {
		if j.ProjectID == projectID && j.Plate == plate && j.Start && (j.State == slicer.Queued || j.State == slicer.Slicing || j.State == slicer.Sending) {
			pr++
		}
	}
	add := copiesFor(p, plate) - acc - pr - rev
	now := time.Now()
	for i := 0; i < add; i++ {
		a.runs = append(a.runs, &Run{ID: runID(), ProjectID: projectID, ProjectName: p.Name, Plate: plate,
			PrinterName: "Marked done", State: "accepted", Manual: true, Started: now, Finished: now})
	}
	a.saveRunsLocked()
	return nil
}

// SetCopies sets how many times a plate must be printed (0 = skip it).
func (a *App) SetCopies(id string, plate, copies int) (*Project, error) {
	if copies < 0 || copies > 999 {
		return nil, fmt.Errorf("copies must be between 0 and 999")
	}
	a.projMu.Lock()
	defer a.projMu.Unlock()
	p, ok := a.projects[id]
	if !ok {
		return nil, fmt.Errorf("project not found")
	}
	if p.Copies == nil {
		p.Copies = map[int]int{}
	}
	p.Copies[plate] = copies
	return p.clone(), a.saveProjectsLocked()
}

// SetAuto turns auto-dispatch on/off and sets the dispatch goal.
func (a *App) SetAuto(id string, on bool, goal string) (*Project, error) {
	switch goal {
	case "", "small", "fast", "loaded":
	default:
		return nil, fmt.Errorf("unknown goal %q", goal)
	}
	a.projMu.Lock()
	p, ok := a.projects[id]
	if !ok {
		a.projMu.Unlock()
		return nil, fmt.Errorf("project not found")
	}
	p.AutoDispatch, p.Goal = on, goal
	err := a.saveProjectsLocked()
	out := p.clone()
	a.projMu.Unlock()
	if on && err == nil {
		go a.Dispatch(id)
	}
	return out, err
}

// Assignment is one plate sent to one printer by Dispatch.
type Assignment struct {
	Plate       int    `json:"plate"`
	PrinterID   string `json:"printer_id"`
	PrinterName string `json:"printer_name"`
	JobID       string `json:"job_id"`
	EstimateSec int    `json:"estimate_sec"`
}

// Dispatch sends every plate that still needs printing to a printer that's
// ready right now (idle, bed clear, right filament loaded). Big plates are
// placed first; each printer gets at most one plate per call.
func (a *App) Dispatch(id string) ([]Assignment, error) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	p, ok := a.Project(id)
	if !ok {
		return nil, fmt.Errorf("project not found")
	}
	busy := map[string]bool{}
	for _, j := range a.Queue.List() {
		if j.Start && (j.State == slicer.Queued || j.State == slicer.Slicing || j.State == slicer.Sending) {
			busy[j.PrinterID] = true
		}
	}
	prog := a.Progress(p)
	remaining := map[int]int{}
	for _, pp := range prog {
		remaining[pp.Plate] = pp.Remaining
	}
	plates := append([]threemf.Plate(nil), p.Data.Plates...)
	sort.SliceStable(plates, func(i, j int) bool { return plates[i].MaxX*plates[i].MaxY > plates[j].MaxX*plates[j].MaxY })

	var out []Assignment
	views := a.Printers.List()
	for progress := true; progress; {
		progress = false
		for _, pl := range plates {
			if remaining[pl.Index] <= 0 || len(pl.Parts) == 0 {
				continue
			}
			var free []printers.View
			for _, v := range views {
				if v.Ready && !busy[v.ID] {
					free = append(free, v)
				}
			}
			if len(free) == 0 {
				return out, nil
			}
			res := a.fitPlate(p, pl, free, fit.Options{Locks: a.locksFor(p)})
			cands := []fit.Result{}
			for _, r := range res {
				if r.Level == fit.Ready {
					cands = append(cands, r)
				}
			}
			if len(cands) == 0 {
				continue
			}
			if p.Goal == "fast" {
				sort.SliceStable(cands, func(i, j int) bool { return cands[i].EstimateSec < cands[j].EstimateSec })
			}
			pick := cands[0]
			sug, err := a.Suggest(p, pick.PrinterID)
			if err != nil || sug.Process == "" {
				continue
			}
			slotMap := make([]int, len(p.Data.Filaments))
			for _, m := range pick.Mapping {
				if m.Filament-1 < len(slotMap) && m.Filament >= 1 {
					slotMap[m.Filament-1] = m.Slot
				}
			}
			pv, _ := a.Printers.Get(pick.PrinterID)
			job := slicer.Job{ProjectID: p.ID, ProjectName: p.Name, Plate: pl.Index, PrinterID: pick.PrinterID,
				PrinterName: pv.Name, Machine: sug.Machine, Process: sug.Process, Filaments: sug.Filaments, SlotMap: slotMap,
				Send: true, Start: true}
			if err := a.applySaved(p, &job); err != nil {
				continue
			}
			j, err := a.Queue.Add(job)
			if err != nil {
				return out, err
			}
			busy[pick.PrinterID] = true
			remaining[pl.Index]--
			out = append(out, Assignment{Plate: pl.Index, PrinterID: pick.PrinterID, PrinterName: pv.Name, JobID: j.ID, EstimateSec: pick.EstimateSec})
			progress = true
		}
	}
	return out, nil
}

// Repack re-arranges the parts of an STL project into as few plates as
// fit the chosen printer's bed (shelf packing, parts may turn 90°).
func (a *App) Repack(id, printerID string) (*Project, error) {
	pv, ok := a.Printers.Get(printerID)
	if !ok {
		return nil, fmt.Errorf("printer not found")
	}
	m, err := a.Lib().Machine(pv.MachinePreset)
	if err != nil {
		return nil, fmt.Errorf("printer profile: %w", err)
	}
	return a.editMesh(id, func(p *Project) error {
		bins, err := packShelves(p.MeshParts, m.BedX-10, m.BedY-10, m.MaxZ, 5)
		if err != nil {
			return err
		}
		for i := range p.MeshParts {
			p.MeshParts[i].Plate = bins[p.MeshParts[i].ID]
		}
		max := 1
		for _, b := range bins {
			if b > max {
				max = b
			}
		}
		p.PlateCount = max
		p.Copies = nil
		return nil
	})
}

// packShelves assigns each part to a plate number using a simple shelf
// packer. It's an estimate: Orca does the final arrangement when slicing.
func packShelves(parts []MeshPart, w, d, maxZ, gap float64) (map[int]int, error) {
	type box struct {
		id   int
		x, y float64
	}
	var boxes []box
	for _, mp := range parts {
		x, y := mp.SizeX, mp.SizeY
		if x < y { // lay long side along X
			x, y = y, x
		}
		fits := (x <= w && y <= d) || (y <= w && x <= d)
		if !fits {
			return nil, fmt.Errorf("%q (%.0f×%.0f mm) doesn't fit this printer's bed (%.0f×%.0f mm)", mp.Name, mp.SizeX, mp.SizeY, w+10, d+10)
		}
		if maxZ > 0 && mp.SizeZ > maxZ {
			return nil, fmt.Errorf("%q is %.0f mm tall; this printer prints up to %.0f mm", mp.Name, mp.SizeZ, maxZ)
		}
		if x > w { // only fits rotated
			x, y = y, x
		}
		boxes = append(boxes, box{mp.ID, x, y})
	}
	sort.SliceStable(boxes, func(i, j int) bool { return boxes[i].y > boxes[j].y })
	out := map[int]int{}
	plate, cx, cy, shelfH := 1, 0.0, 0.0, 0.0
	for _, b := range boxes {
		if cx > 0 && cx+b.x > w { // next shelf
			cy += shelfH + gap
			cx, shelfH = 0, 0
		}
		if cy+b.y > d { // next plate
			plate++
			cx, cy, shelfH = 0, 0, 0
		}
		out[b.id] = plate
		cx += b.x + gap
		shelfH = math.Max(shelfH, b.y)
	}
	return out, nil
}

// readyFor is how long a job that was only sliced, or sent without starting,
// stays on the Queue page after it is ready, so its file can still be
// previewed or downloaded. (A job that prints leaves when the print ends.)
const readyFor = time.Hour

// jobView is a job as the Queue page gets it.
type jobView struct {
	slicer.Job
	// Printing: the printer is working on this job right now.
	Printing bool `json:"printing"`
	// InQueue: it belongs on the Queue page. The Queue is what is being worked
	// on (waiting, slicing, sending, printing) and what went wrong. Finished
	// jobs are not listed.
	InQueue bool `json:"in_queue"`
}

// jobViews lists every job with whether it is printing now and whether the
// Queue page should show it.
func (a *App) jobViews(now time.Time) []jobView {
	// The newest run of each job that is still marked "printing".
	started := map[string]time.Time{}
	a.runsMu.Lock()
	for _, r := range a.runs {
		if r.State == "printing" && r.JobID != "" {
			started[r.JobID] = r.Started
		}
	}
	a.runsMu.Unlock()
	out := []jobView{}
	for _, j := range a.Queue.List() {
		v := jobView{Job: j}
		if at, ok := started[j.ID]; ok && j.State == slicer.Done {
			// Believe the run only while the printer agrees (or has barely had
			// time to say so): a run whose end was missed must not show as
			// printing for ever.
			if pv, ok := a.Printers.Get(j.PrinterID); ok && (pv.Status.State == printers.StatePrinting || pv.Status.State == printers.StatePaused) {
				v.Printing = true
			} else if now.Sub(at) < 3*time.Minute {
				v.Printing = true
			}
		}
		switch j.State {
		case slicer.Queued, slicer.Slicing, slicer.Sending, slicer.Failed:
			v.InQueue = true
		case slicer.Done:
			if j.Send && j.Start {
				v.InQueue = v.Printing
			} else {
				v.InQueue = now.Sub(j.Finished) < readyFor
			}
		}
		out = append(out, v)
	}
	return out
}
