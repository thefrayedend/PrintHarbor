package slicer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thefrayedend/printharbor/internal/store"
)

type JobState string

const (
	Queued   JobState = "queued"
	Slicing  JobState = "slicing"
	Sending  JobState = "sending"
	Done     JobState = "done"
	Failed   JobState = "failed"
	Canceled JobState = "canceled"
)

// Job is one plate sliced for one printer, optionally sent to it.
type Job struct {
	ID          string            `json:"id"`
	ProjectID   string            `json:"project_id"`
	ProjectName string            `json:"project_name"`
	Plate       int               `json:"plate"`
	PrinterID   string            `json:"printer_id"`
	PrinterName string            `json:"printer_name"`
	Machine     string            `json:"machine_preset"`
	Process     string            `json:"process_preset"`
	Filaments   []string          `json:"filament_presets"`
	Overrides   map[string]string `json:"overrides,omitempty"`
	// SlotMap: element i = printer slot (1-based) for project filament i+1.
	SlotMap []int `json:"slot_map,omitempty"`
	// From the sliced G-code (Orca's summary).
	PrintTimeSec int     `json:"print_time_sec,omitempty"`
	FilamentG    float64 `json:"filament_g,omitempty"`
	// FilamentGs: grams used of each filament, in the order of Filaments (nil
	// when the G-code didn't say). The Spoolman write-back uses it.
	FilamentGs []float64 `json:"filament_gs,omitempty"`
	// SlicedSec: how long slicing took, so the next slice of the same plate can show how far along it is.
	SlicedSec int  `json:"sliced_sec,omitempty"`
	Send      bool `json:"send"`
	Start     bool `json:"start"`
	// Level: run the printer's bed-leveling command as part of this print's
	// start g-code (the "Level the bed before printing" toggle).
	Level bool     `json:"level,omitempty"`
	State JobState `json:"state"`
	Error string   `json:"error,omitempty"`
	// Note: something to know about how it was sliced (Result.Notes).
	Note      string    `json:"note,omitempty"`
	GCode     string    `json:"gcode,omitempty"`
	Sliced3MF string    `json:"sliced_3mf,omitempty"`
	Created   time.Time `json:"created"`
	Started   time.Time `json:"started,omitempty"`
	Finished  time.Time `json:"finished,omitempty"`
}

// Preparer turns a Job into a slice Request (resolving presets, applying
// overrides). Sender uploads the result to the printer. Both are provided by
// the app so this package stays independent of profiles and printers.
type Preparer func(j *Job, workDir string) (Request, error)
type Sender func(ctx context.Context, j *Job) error
type Slicer interface {
	Slice(ctx context.Context, req Request) (Result, error)
}

type Queue struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	cancels map[string]context.CancelFunc
	pending chan string
	store   *store.Store
	slicer  Slicer
	prepare Preparer
	send    Sender
	timeout time.Duration
	notify  func(*Job)
}

func NewQueue(s *store.Store, sl Slicer, prep Preparer, send Sender, workers int, timeout time.Duration) (*Queue, error) {
	q := &Queue{jobs: map[string]*Job{}, cancels: map[string]context.CancelFunc{}, pending: make(chan string, 1024),
		store: s, slicer: sl, prepare: prep, send: send, timeout: timeout}
	var saved []*Job
	if err := s.Load("jobs", &saved); err != nil {
		return nil, err
	}
	for _, j := range saved {
		// Anything interrupted by a restart is re-queued.
		if j.State == Slicing || j.State == Sending {
			j.State = Queued
		}
		q.jobs[j.ID] = j
	}
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		go q.worker()
	}
	for _, j := range q.sorted() {
		if j.State == Queued {
			q.pending <- j.ID
		}
	}
	return q, nil
}

// OnChange registers a callback fired whenever a job changes.
func (q *Queue) OnChange(f func(*Job)) { q.notify = f }

func (q *Queue) sorted() []*Job {
	out := make([]*Job, 0, len(q.jobs))
	for _, j := range q.jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Created.Before(out[b].Created) })
	return out
}

func (q *Queue) saveLocked() {
	_ = q.store.Save("jobs", q.sorted())
}

func (q *Queue) Add(j Job) (*Job, error) {
	if j.Machine == "" || j.Process == "" {
		return nil, fmt.Errorf("machine and process presets are required")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	j.ID = hex.EncodeToString(b)
	j.State = Queued
	j.Created = time.Now()
	stored := j // the queue owns this copy; callers get their own
	q.mu.Lock()
	q.jobs[j.ID] = &stored
	q.saveLocked()
	q.mu.Unlock()
	q.pending <- j.ID
	return &j, nil
}

func (q *Queue) List() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []Job{}
	for _, j := range q.sorted() {
		out = append(out, *j)
	}
	return out
}

func (q *Queue) Get(id string) (Job, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

func (q *Queue) Cancel(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[id]
	if !ok {
		return fmt.Errorf("job not found")
	}
	switch j.State {
	case Queued:
		j.State = Canceled
	case Slicing, Sending:
		if c := q.cancels[id]; c != nil {
			c()
		}
	default:
		return fmt.Errorf("job already %s", j.State)
	}
	q.saveLocked()
	return nil
}

// Prune forgets jobs that finished (done, failed or canceled) before the given
// time and returns their IDs, so the caller can remove their files. Jobs that
// are waiting or running are never touched.
func (q *Queue) Prune(before time.Time) []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var gone []string
	for id, j := range q.jobs {
		if j.State != Done && j.State != Failed && j.State != Canceled {
			continue
		}
		at := j.Finished
		if at.IsZero() {
			at = j.Started // canceled while waiting: never finished
		}
		if at.IsZero() {
			at = j.Created
		}
		if at.Before(before) {
			delete(q.jobs, id)
			gone = append(gone, id)
		}
	}
	if len(gone) > 0 {
		q.saveLocked()
	}
	return gone
}

// Remove forgets a single finished job (done, failed or canceled). A waiting or
// running job can't be removed — cancel it first. The caller removes its files.
func (q *Queue) Remove(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[id]
	if !ok {
		return fmt.Errorf("job not found")
	}
	if j.State != Done && j.State != Failed && j.State != Canceled {
		return fmt.Errorf("the job is still %s — cancel it first", j.State)
	}
	delete(q.jobs, id)
	q.saveLocked()
	return nil
}

// Has reports whether the queue still knows a job.
func (q *Queue) Has(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.jobs[id]
	return ok
}

func (q *Queue) set(j *Job, f func()) {
	q.mu.Lock()
	f()
	q.saveLocked()
	cp := *j
	q.mu.Unlock()
	if q.notify != nil {
		q.notify(&cp)
	}
}

// SendNow sends a job that was sliced but not sent to its printer, exactly as
// it was sliced (start: also start printing). It is for looking at the preview
// first: slice, look, then send. The sending runs in the background like any
// other job's; the job shows as "sending", then done or failed.
func (q *Queue) SendNow(id string, start bool) (Job, error) {
	if q.send == nil {
		return Job{}, fmt.Errorf("sending isn't available")
	}
	q.mu.Lock()
	j, ok := q.jobs[id]
	switch {
	case !ok:
		q.mu.Unlock()
		return Job{}, fmt.Errorf("job not found")
	case j.State != Done || j.Send:
		q.mu.Unlock()
		return Job{}, fmt.Errorf("only a job that was sliced and not sent yet can be sent")
	case j.GCode == "" && j.Sliced3MF == "":
		q.mu.Unlock()
		return Job{}, fmt.Errorf("the sliced file is gone; slice it again")
	}
	ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
	q.cancels[id] = cancel
	j.Send, j.Start, j.State, j.Error = true, start, Sending, ""
	q.saveLocked()
	cp := *j
	q.mu.Unlock()
	if q.notify != nil {
		q.notify(&cp)
	}
	go func() {
		defer func() {
			cancel()
			q.mu.Lock()
			delete(q.cancels, id)
			q.mu.Unlock()
		}()
		if err := q.send(ctx, j); err != nil {
			q.set(j, func() {
				// Back to "sliced": the file is fine, so it can be sent again.
				j.State, j.Send, j.Start, j.Finished = Done, false, false, time.Now()
				j.Error = "Sending failed: " + err.Error()
				if ctx.Err() == context.Canceled {
					j.Error = ""
				}
			})
			return
		}
		q.set(j, func() { j.State, j.Finished = Done, time.Now() })
	}()
	return cp, nil
}

// Retry sends a failed or canceled job around again with the same settings.
func (q *Queue) Retry(id string) (*Job, error) {
	j, ok := q.Get(id)
	if !ok {
		return nil, fmt.Errorf("job not found")
	}
	j.Error, j.GCode, j.Sliced3MF = "", "", ""
	j.Started, j.Finished = time.Time{}, time.Time{}
	return q.Add(j)
}

func (q *Queue) worker() {
	for id := range q.pending {
		q.mu.Lock()
		j, ok := q.jobs[id]
		if !ok || j.State != Queued {
			q.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
		q.cancels[id] = cancel
		q.mu.Unlock()

		q.run(ctx, j)

		cancel()
		q.mu.Lock()
		delete(q.cancels, id)
		q.mu.Unlock()
	}
}

func (q *Queue) run(ctx context.Context, j *Job) {
	fail := func(err error) {
		q.set(j, func() {
			j.State, j.Error, j.Finished = Failed, err.Error(), time.Now()
			if ctx.Err() == context.Canceled {
				j.State, j.Error = Canceled, ""
			}
		})
	}
	q.set(j, func() { j.State, j.Started = Slicing, time.Now() })

	work := q.store.Path("jobs", j.ID, "x")
	work = filepath.Dir(work)
	_ = os.RemoveAll(filepath.Join(work, "out"))
	req, err := q.prepare(j, work)
	if err != nil {
		fail(err)
		return
	}
	res, err := q.slicer.Slice(ctx, req)
	if err != nil {
		fail(err)
		return
	}
	sec, grams := 0, 0.0
	var each []float64
	if res.GCode != "" {
		sec, grams, each = GCodeUsage(res.GCode)
	}
	q.set(j, func() {
		j.GCode, j.Sliced3MF, j.PrintTimeSec, j.FilamentG, j.FilamentGs = res.GCode, res.Sliced3MF, sec, grams, each
		j.SlicedSec = int(time.Since(j.Started).Seconds()) + 1
		j.Note = strings.Join(res.Notes, " ")
	})

	if j.Send && q.send != nil {
		q.set(j, func() { j.State = Sending })
		if err := q.send(ctx, j); err != nil {
			fail(fmt.Errorf("sliced OK, but sending failed: %w", err))
			return
		}
	}
	q.set(j, func() { j.State, j.Finished = Done, time.Now() })
}
