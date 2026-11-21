package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/printers"
	"github.com/thefrayedend/printharbor/internal/slicer"
	"github.com/thefrayedend/printharbor/internal/threemf"
)

func TestSpoolDebits(t *testing.T) {
	job := func(total float64, each []float64, presets ...string) slicer.Job {
		return slicer.Job{FilamentG: total, FilamentGs: each, Filaments: presets}
	}
	for name, c := range map[string]struct {
		job   slicer.Job
		locks map[int]int
		nos   []int
		want  map[int]float64
	}{
		"one colour, pinned":                        {job(12.5, []float64{12.5}, "PLA"), map[int]int{1: 42}, []int{1}, map[int]float64{42: 12.5}},
		"one colour, not pinned":                    {job(12.5, []float64{12.5}, "PLA"), nil, []int{1}, map[int]float64{}},
		"three colours, one pinned: only its share": {job(30, []float64{20, 4, 6}, "a", "b", "c"), map[int]int{2: 7}, []int{1, 2, 3}, map[int]float64{7: 4}},
		"three colours, all pinned: each its share": {job(30, []float64{20, 4, 6}, "a", "b", "c"), map[int]int{1: 5, 2: 7, 3: 9}, []int{1, 2, 3}, map[int]float64{5: 20, 7: 4, 9: 6}},
		"two colours on the same spool":             {job(10, []float64{6, 4}, "a", "b"), map[int]int{1: 5, 2: 5}, []int{1, 2}, map[int]float64{5: 10}},
		"a pinned colour this plate didn't use":     {job(8, []float64{8, 0}, "a", "b"), map[int]int{2: 7}, []int{1, 2}, map[int]float64{}},
		"filament numbers that aren't 1,2,3":        {job(9, []float64{5, 4}, "a", "b"), map[int]int{4: 7}, []int{2, 4}, map[int]float64{7: 4}},
		"only a total, one filament, pinned":        {job(12.5, nil, "PLA"), map[int]int{1: 42}, []int{1}, map[int]float64{42: 12.5}},
		"only a total, three colours, one pinned":   {job(30, nil, "a", "b", "c"), map[int]int{1: 42}, []int{1, 2, 3}, map[int]float64{}},
		"only a total, two pinned":                  {job(30, nil), map[int]int{1: 42, 2: 7}, nil, map[int]float64{}},
		"nothing sliced":                            {job(0, nil, "PLA"), map[int]int{1: 42}, []int{1}, map[int]float64{}},
	} {
		got := spoolDebits(c.job, c.locks, c.nos)
		if len(got) != len(c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] < v-0.001 || got[k] > v+0.001 {
				t.Errorf("%s: %v, want %v", name, got, c.want)
			}
		}
	}
}

// How a print ended decides what happens to its run, which event goes out and
// whether Spoolman is charged. A cancelled print used to count as finished and
// was charged in full.
func TestPrintEndRunsEventsSpoolman(t *testing.T) {
	var mu sync.Mutex
	used := map[string]float64{}
	sm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			UseWeight float64 `json:"use_weight"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		mu.Lock()
		used[r.URL.Path] += body.UseWeight
		mu.Unlock()
	}))
	defer sm.Close()
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.OrcaPath = "/none"
	cfg.SpoolmanURL, cfg.SpoolmanWriteback = sm.URL, true
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pr := "pr" + strconv.FormatInt(time.Now().UnixNano(), 36) // its own printer: the recent-events list is shared between tests
	a.projMu.Lock()
	a.projects["p1"] = &Project{ID: "p1", Name: "Dragon", Locks: map[int]int{1: 42, 2: 7},
		Data: &threemf.Project{Filaments: []threemf.Filament{{Index: 1}, {Index: 2}, {Index: 3}}}}
	a.projMu.Unlock()
	// A sliced job in the queue: 20 g of filament 1, 5 g of filament 2, 3 g of filament 3 (not pinned).
	a.Store.Save("jobs", []*slicer.Job{{ID: "aaaaaaaaaaa1", ProjectID: "p1", ProjectName: "Dragon", Plate: 1, PrinterID: pr, State: slicer.Done,
		FilamentG: 28, FilamentGs: []float64{20, 5, 3}, Filaments: []string{"a", "b", "c"}, Created: time.Now(), Finished: time.Now()}})
	q, err := slicer.NewQueue(a.Store, a.Slicer, a.prepare, a.send, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a.Queue = q
	job, _ := a.Queue.Get("aaaaaaaaaaa1")
	charged := func() map[string]float64 {
		time.Sleep(150 * time.Millisecond) // the write-back runs in the background
		mu.Lock()
		defer mu.Unlock()
		out := map[string]float64{}
		for k, v := range used {
			out[k] = v
		}
		return out
	}
	lastRun := func() Run { return a.Runs(pr, "")[0] }

	// Stopped part-way: the run failed, nothing is charged.
	a.startRun(&job)
	a.onPrintEnd(pr, printers.PrintEnd{Outcome: printers.EndStopped, Progress: 0.4})
	if r := lastRun(); r.State != "failed" || !r.Stopped || r.Finished.IsZero() {
		t.Errorf("stopped print: run %+v", r)
	}
	if c := charged(); len(c) != 0 {
		t.Errorf("a stopped print was charged to Spoolman: %v", c)
	}
	// The printer went idle without saying how far it got: check the parts, charge nothing.
	a.startRun(&job)
	a.onPrintEnd(pr, printers.PrintEnd{Outcome: printers.EndUnknown})
	if r := lastRun(); r.State != "printed" || r.Stopped {
		t.Errorf("unknown end: run %+v", r)
	}
	if c := charged(); len(c) != 0 {
		t.Errorf("a print of unknown outcome was charged: %v", c)
	}
	// Completed: each pinned spool is charged its own filament, the unpinned one nothing.
	a.startRun(&job)
	a.onPrintEnd(pr, printers.PrintEnd{Outcome: printers.EndCompleted, Progress: 1})
	if r := lastRun(); r.State != "printed" {
		t.Errorf("completed print: run %+v", r)
	}
	c := charged()
	if len(c) != 2 || c["/api/v1/spool/42/use"] != 20 || c["/api/v1/spool/7/use"] != 5 {
		t.Errorf("completed print charged %v, want 20 g to spool 42 and 5 g to spool 7", c)
	}
	// The events, in order.
	var types []string
	for _, ev := range a.Plugins.RecentEvents() {
		if ev["printer_id"] == pr {
			types = append(types, ev["type"].(string))
		}
	}
	want := []string{"print_started", "print_cancelled", "print_started", "print_finished", "print_started", "print_finished"}
	if len(types) != len(want) {
		t.Fatalf("events %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events %v, want %v", types, want)
		}
	}

	// A run PrintHarbor lost track of (the printer was unreachable when it ended)
	// must not be mistaken for the next print: starting a new one closes it.
	a.startRun(&job) // left "printing"
	a.startRun(&job)
	runs := a.Runs(pr, "")
	states := []string{}
	for _, r := range runs {
		states = append(states, r.State)
	}
	sort.Strings(states)
	n := 0
	for _, s := range states {
		if s == "printing" {
			n++
		}
	}
	if n != 1 || runs[0].State != "printing" || runs[1].State != "printed" {
		t.Errorf("after a lost run: %v (newest first: %s, %s)", states, runs[0].State, runs[1].State)
	}
	a.onPrintEnd(pr, printers.PrintEnd{Outcome: printers.EndStopped})
	if c := charged(); c["/api/v1/spool/42/use"] != 20 {
		t.Errorf("the lost run was charged later: %v", c)
	}
}

// Settings and projects are read and changed from many goroutines at once. Run
// with -race: before the locks this reported data races, and in real use it
// could lose a change or stop the program ("concurrent map writes").
func TestSettingsAndProjectsFromManyGoroutines(t *testing.T) {
	a := guardApp(t)
	p, err := a.NewMeshProject("Busy")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler(nil))
	defer srv.Close()
	var wg sync.WaitGroup
	stop := time.Now().Add(400 * time.Millisecond)
	run := func(f func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; time.Now().Before(stop); i++ {
				f(i)
			}
		}()
	}
	run(func(i int) { a.SetCopies(p.ID, 1+i%3, i%9) })
	run(func(i int) { a.SetLock(p.ID, 1+i%4, i%5) })
	run(func(i int) { a.RenameProject(p.ID, "Busy "+string(rune('a'+i%26))) })
	run(func(i int) { a.SetAuto(p.ID, false, "small") })
	run(func(i int) { // what a page does: read it and turn it into JSON
		if got, ok := a.Project(p.ID); ok {
			json.Marshal(got)
			_ = got.Copies[1] + got.Locks[1]
		}
		json.Marshal(a.Projects())
	})
	run(func(i int) { a.spoolmanUse(slicer.Job{ProjectID: p.ID, FilamentGs: []float64{1}}) })
	run(func(i int) { // Settings being saved...
		a.cfgEdit.Lock()
		c := a.cfg()
		c.JobKeepDays, c.SpoolmanURL = 1+i%30, ""
		c.LibraryFolders = []string{"x", "y"}[:1+i%2]
		a.setCfg(c)
		a.cfgEdit.Unlock()
	})
	run(func(i int) { // ...while everything else reads them
		_ = a.cfg().JobKeepDays + len(a.cfg().ScanFolders())
		a.settings()
		a.cleanupJobs(time.Now())
		http.Get(srv.URL + "/api/settings")
	})
	wg.Wait()
	// A copy handed out is a copy: changing it changes nothing.
	got, _ := a.Project(p.ID)
	got.Name = "scribbled on"
	if got.Copies == nil {
		got.Copies = map[int]int{}
	}
	got.Copies[99] = 99
	again, _ := a.Project(p.ID)
	if again.Name == "scribbled on" || again.Copies[99] == 99 {
		t.Error("changing a project that was handed out changed the real one")
	}
}

// The slicer's list of settings follows the Orca profiles: built again when
// they are reloaded, and never stuck on a list made before they had loaded.
func TestSlicerSchemaFollowsProfiles(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.OrcaPath = "/none"
	cfg.OrcaProfilesDir = filepath.Join(dir, "profiles")
	cfg.OrcaUserPresetsDir = ""
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	has := func(key string) bool {
		for _, s := range a.slicerSchema() {
			if s["key"] == key {
				return true
			}
		}
		return false
	}
	if has("layer_height") {
		t.Fatal("no profiles yet, but layer_height is offered")
	}
	// The profiles arrive (the folder is set in Settings, Orca gets installed...).
	write(t, filepath.Join(dir, "profiles", "Acme", "process", "std.json"),
		`{"type":"process","name":"0.20mm Standard @Acme","instantiation":"true","layer_height":"0.2"}`)
	a.ReloadProfiles()
	if !has("layer_height") {
		t.Fatal("the settings list didn't pick up the profiles after a reload")
	}
	if has("wall_loops") {
		t.Fatal("wall_loops offered though no preset has it")
	}
	// A preset is added: its settings show up after the next reload.
	write(t, filepath.Join(dir, "profiles", "Acme", "process", "strong.json"),
		`{"type":"process","name":"0.20mm Strong @Acme","instantiation":"true","layer_height":"0.2","wall_loops":"6"}`)
	a.ReloadProfiles()
	if !has("wall_loops") {
		t.Error("a setting from a newly loaded preset isn't offered")
	}
	// Unchanged profiles: the same list is reused, not rebuilt on every request.
	s1, s2 := a.slicerSchema(), a.slicerSchema()
	if len(s1) == 0 || &s1[0] != &s2[0] {
		t.Error("the list is rebuilt on every request")
	}
}

// Settings must be able to say which build is running.
func TestInfoHasBuildNumber(t *testing.T) {
	a := guardApp(t)
	srv := httptest.NewServer(a.Handler(nil))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/info")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info struct {
		Version string `json:"version"`
		Build   int    `json:"build_number"`
	}
	json.NewDecoder(resp.Body).Decode(&info)
	if info.Version != Version || info.Build != Build || Build < 56 {
		t.Errorf("info = %+v", info)
	}
	if VersionLabel() != Version+" (build "+strconv.Itoa(Build)+")" {
		t.Error(VersionLabel())
	}
}

// The Queue page lists what is being worked on and what failed. Finished jobs
// are not listed, and "Printing" follows the printer, not the fact that the
// job was once sent.
func TestQueueListsOnlyWhatIsInProgress(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.OrcaPath = "/none"
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	busy, err := a.Printers.Put(printersPrinterSecs("Busy", "Acme 0.4", "600"))
	if err != nil {
		t.Fatal(err)
	}
	idle, _ := a.Printers.Put(printersPrinterSecs("Idle", "Acme 0.4", "600"))
	if err := a.Printers.Send(context.Background(), busy.ID, "x.gcode", strings.NewReader("G28\n"), true); err != nil {
		t.Fatal(err)
	}
	a.Printers.PollOnce(context.Background())
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	job := func(id string, st slicer.JobState, send, start bool, printer string, finished time.Time) *slicer.Job {
		return &slicer.Job{ID: id, ProjectID: "p", ProjectName: "P", Plate: 1, PrinterID: printer, State: st, Send: send, Start: start, Created: ago(3 * time.Hour), Finished: finished}
	}
	a.Store.Save("jobs", []*slicer.Job{
		job("aaaaaaaaaa01", slicer.Failed, true, true, idle.ID, ago(48*time.Hour)),
		job("aaaaaaaaaa02", slicer.Canceled, true, true, idle.ID, ago(time.Minute)),
		job("aaaaaaaaaa03", slicer.Done, false, false, idle.ID, ago(10*time.Minute)), // sliced only, just now
		job("aaaaaaaaaa04", slicer.Done, false, false, idle.ID, ago(2*time.Hour)),    // sliced only, a while ago
		job("aaaaaaaaaa05", slicer.Done, true, false, idle.ID, ago(2*time.Hour)),     // uploaded, not started, a while ago
		job("aaaaaaaaaa06", slicer.Done, true, true, busy.ID, ago(20*time.Minute)),   // printing now
		job("aaaaaaaaaa07", slicer.Done, true, true, idle.ID, ago(20*time.Minute)),   // its end was missed: the printer is idle
		job("aaaaaaaaaa08", slicer.Done, true, true, idle.ID, ago(20*time.Minute)),   // printed
		job("aaaaaaaaaa09", slicer.Done, true, true, idle.ID, ago(20*time.Second)),   // only just sent: the printer hasn't said "printing" yet
	})
	q, err := slicer.NewQueue(a.Store, a.Slicer, a.prepare, a.send, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a.Queue = q
	a.runsMu.Lock()
	a.runs = []*Run{
		{ID: "r6", JobID: "aaaaaaaaaa06", PrinterID: busy.ID, State: "printing", Started: ago(20 * time.Minute)},
		{ID: "r7", JobID: "aaaaaaaaaa07", PrinterID: idle.ID, State: "printing", Started: ago(20 * time.Minute)},
		{ID: "r8", JobID: "aaaaaaaaaa08", PrinterID: idle.ID, State: "printed", Started: ago(20 * time.Minute), Finished: ago(time.Minute)},
		{ID: "r9", JobID: "aaaaaaaaaa09", PrinterID: idle.ID, State: "printing", Started: ago(20 * time.Second)},
	}
	a.runsMu.Unlock()

	want := map[string][2]bool{ // id -> {printing, in the queue}
		"aaaaaaaaaa01": {false, true},  // failed: stays so you can retry
		"aaaaaaaaaa02": {false, false}, // you cancelled it
		"aaaaaaaaaa03": {false, true},  // file ready, still within the hour
		"aaaaaaaaaa04": {false, false},
		"aaaaaaaaaa05": {false, false},
		"aaaaaaaaaa06": {true, true},
		"aaaaaaaaaa07": {false, false},
		"aaaaaaaaaa08": {false, false},
		"aaaaaaaaaa09": {true, true},
	}
	// Through the API, as the page gets it.
	srv := httptest.NewServer(a.Handler(nil))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/jobs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got []struct {
		ID       string `json:"id"`
		State    string `json:"state"`
		Printing bool   `json:"printing"`
		InQueue  bool   `json:"in_queue"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("%d jobs, want %d", len(got), len(want))
	}
	for _, j := range got {
		if w := want[j.ID]; j.Printing != w[0] || j.InQueue != w[1] || j.State == "" {
			t.Errorf("%s (%s): printing=%v in_queue=%v, want %v %v", j.ID, j.State, j.Printing, j.InQueue, w[0], w[1])
		}
	}
}
