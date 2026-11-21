package printers

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/store"
)

// scripted is a printer that says whatever the test tells it to.
type scripted struct {
	mu sync.Mutex
	st Status
}

func (s *scripted) set(st Status) { s.mu.Lock(); s.st = st; s.mu.Unlock() }
func (s *scripted) Capabilities() Capabilities {
	return Capabilities{Status: true, Cancel: true, Upload: true, StartPrint: true}
}
func (s *scripted) Status(context.Context) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st, nil
}
func (s *scripted) Pause(context.Context) error                           { return nil }
func (s *scripted) Resume(context.Context) error                          { return nil }
func (s *scripted) Cancel(context.Context) error                          { return nil }
func (s *scripted) OutputFormat() string                                  { return "gcode" }
func (s *scripted) Upload(context.Context, string, io.Reader, bool) error { return nil }

// A print that ends is not always a print that finished. The registry must say
// which, because the Spoolman write-back and "print finished" depend on it.
func TestHowAPrintEnded(t *testing.T) {
	oldGrace := startGrace
	defer func() { startGrace = oldGrace }()
	dev := &scripted{}
	Register(DriverInfo{Name: "scripted", Label: "Scripted"}, func(Printer) (Driver, error) { return dev, nil })
	st, _ := store.New(t.TempDir())
	r, err := NewRegistry(st)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Put(Printer{Name: "P", Driver: "scripted", Address: "x"})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var ends []PrintEnd
	var order []string
	r.OnPrintEnd = func(id string, e PrintEnd) {
		mu.Lock()
		ends = append(ends, e)
		order = append(order, "end")
		mu.Unlock()
	}
	r.OnTransition = func(id string, prev, cur State, ready bool) {
		mu.Lock()
		order = append(order, string(prev)+">"+string(cur))
		mu.Unlock()
	}
	poll := func(s Status) { dev.set(s); r.PollOnce(context.Background()); time.Sleep(30 * time.Millisecond) }
	last := func() (PrintEnd, int) {
		mu.Lock()
		defer mu.Unlock()
		if len(ends) == 0 {
			return PrintEnd{}, 0
		}
		return ends[len(ends)-1], len(ends)
	}
	printing := func(prog float64) Status { return Status{State: StatePrinting, Progress: prog} }
	expect := func(what, outcome string, n int) {
		t.Helper()
		e, got := last()
		if got != n || (n > 0 && e.Outcome != outcome) {
			t.Fatalf("%s: %d end(s), last %q; want %d, %q", what, got, e.Outcome, n, outcome)
		}
	}

	poll(Status{State: StateIdle})
	expect("idle printer", "", 0)

	// 1. Runs to the end, the printer says "complete".
	poll(printing(0.2))
	poll(printing(0.9))
	poll(Status{State: StateFinished, Progress: 1})
	expect("completed", EndCompleted, 1)
	mu.Lock()
	if n := len(order); n < 2 || order[n-2] != "end" || order[n-1] != "printing>finished" {
		t.Errorf("the end must be reported just before the state change: %v", order)
	}
	mu.Unlock()
	poll(Status{State: StateIdle}) // still "finished, clear the bed": no second end
	expect("after completion", EndCompleted, 1)
	r.SetBedClear(p.ID, true)

	// 2. Stop pressed in PrintHarbor: the printer just goes idle.
	poll(printing(0.4))
	if err := r.Action(context.Background(), p.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	poll(Status{State: StateIdle})
	expect("stopped from PrintHarbor", EndStopped, 2)
	r.SetBedClear(p.ID, true)

	// 3. Cancelled on the printer; the driver says so (FlashForge even calls
	// the state "finished", because the bed still has to be cleared).
	poll(printing(0.6))
	poll(Status{State: StateFinished, Stopped: true})
	expect("cancelled on the printer", EndStopped, 3)
	if e, _ := last(); e.Progress != 0.6 {
		t.Errorf("progress at the stop: %v", e.Progress)
	}
	r.SetBedClear(p.ID, true)

	// 4. Went idle part-way without a word: not a finished print.
	poll(printing(0.5))
	poll(Status{State: StateIdle})
	expect("idle part-way", EndStopped, 4)
	r.SetBedClear(p.ID, true)

	// 5. Idle at 100% (a printer with no "complete" state): completed.
	poll(printing(0.995))
	poll(Status{State: StateIdle})
	expect("idle at the end", EndCompleted, 5)
	r.SetBedClear(p.ID, true)

	// 6. A printer that reports no progress at all: unknown, never "completed".
	poll(printing(0))
	poll(Status{State: StateIdle})
	expect("no progress reported", EndUnknown, 6)
	r.SetBedClear(p.ID, true)

	// 7. An error that it recovers from is not the end; an error it doesn't is a failure.
	poll(printing(0.3))
	poll(Status{State: StateError})
	expect("error, not settled yet", EndUnknown, 6)
	poll(printing(0.5))
	poll(Status{State: StateError})
	poll(Status{State: StateIdle})
	expect("error then idle", EndFailed, 7)
	r.SetBedClear(p.ID, true)

	// 8. Unreachable for a while, then finished: still one end, completed.
	poll(printing(0.8))
	poll(Status{State: StateOffline})
	poll(Status{State: StateFinished, Progress: 1})
	expect("offline in between", EndCompleted, 8)
	r.SetBedClear(p.ID, true)

	// 9. Just sent: the printer still shows its LAST job as complete. That is
	// not this print finishing, and nothing may be charged for it.
	poll(Status{State: StateFinished, Progress: 1}) // the old job, lingering
	r.SetBedClear(p.ID, true)
	if err := r.Send(context.Background(), p.ID, "new.gcode", nil, true); err != nil {
		t.Fatal(err)
	}
	poll(Status{State: StateFinished, Progress: 1})
	poll(Status{State: StateIdle, Stopped: true})
	expect("stale state right after sending", EndCompleted, 8)
	if v, _ := r.Get(p.ID); v.Status.State != StatePrinting {
		t.Errorf("a print that was just sent shows as %s", v.Status.State)
	}
	poll(printing(0.1))
	poll(Status{State: StateFinished, Progress: 1})
	expect("the sent print completing", EndCompleted, 9)
	r.SetBedClear(p.ID, true)

	// 10. Sent, but the printer never starts it.
	if err := r.Send(context.Background(), p.ID, "never.gcode", nil, true); err != nil {
		t.Fatal(err)
	}
	poll(Status{State: StateIdle})
	expect("not started yet", EndCompleted, 9)
	startGrace = 0
	poll(Status{State: StateIdle})
	expect("never started", EndStopped, 10)
}

func TestOutcomeOf(t *testing.T) {
	for _, c := range []struct {
		raw              State
		stopped, errored bool
		progress         float64
		want             string
	}{
		{StateFinished, false, false, 1, EndCompleted},
		{StateFinished, false, false, 0, EndCompleted}, // the printer says complete; trust it
		{StateFinished, true, false, 1, EndStopped},    // "stopped" always wins
		{StateIdle, true, false, 0.999, EndStopped},
		{StateIdle, false, true, 0.5, EndFailed},
		{StateIdle, false, false, 0.99, EndCompleted},
		{StateIdle, false, false, 0.98, EndStopped},
		{StateIdle, false, false, 0, EndUnknown},
	} {
		if got := outcomeOf(c.raw, c.stopped, c.errored, c.progress); got != c.want {
			t.Errorf("outcomeOf(%s, stopped=%v, errored=%v, %.3f) = %s, want %s", c.raw, c.stopped, c.errored, c.progress, got, c.want)
		}
	}
}
