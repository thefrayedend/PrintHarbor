package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/slicer"
)

func touch(t *testing.T, path string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	os.Chtimes(path, at, at)
	os.Chtimes(filepath.Dir(path), at, at)
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// Old finished jobs go, with their G-code; everything else stays.
func TestCleanupJobs(t *testing.T) {
	a := guardApp(t)
	data := a.Store.Dir()
	day := 24 * time.Hour
	// The queue as a previous run left it.
	jobs := []*slicer.Job{
		{ID: "aaaaaaaaaaa1", State: slicer.Done, Created: time.Now().Add(-40 * day), Finished: time.Now().Add(-40 * day)},
		{ID: "aaaaaaaaaaa2", State: slicer.Failed, Created: time.Now().Add(-31 * day), Finished: time.Now().Add(-31 * day)},
		{ID: "aaaaaaaaaaa3", State: slicer.Canceled, Created: time.Now().Add(-35 * day)}, // canceled while waiting
		{ID: "aaaaaaaaaaa4", State: slicer.Done, Created: time.Now().Add(-2 * day), Finished: time.Now().Add(-2 * day)},
		{ID: "aaaaaaaaaaa5", State: slicer.Done, Created: time.Now().Add(-90 * day), Finished: time.Now().Add(-1 * day)}, // old project, printed yesterday
	}
	if err := a.Store.Save("jobs", jobs); err != nil {
		t.Fatal(err)
	}
	q, err := slicer.NewQueue(a.Store, a.Slicer, a.prepare, a.send, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a.Queue = q
	for _, j := range jobs {
		touch(t, filepath.Join(data, "jobs", j.ID, "out", "plate_1.gcode"), 40*day)
	}
	touch(t, filepath.Join(data, "jobs", "bbbbbbbbbbb1", "out", "plate_1.gcode"), 60*day) // folder of a job no longer listed
	touch(t, filepath.Join(data, "jobs", "bbbbbbbbbbb2", "out", "plate_1.gcode"), time.Hour)
	touch(t, filepath.Join(data, "jobs", "not-a-job", "keep.txt"), 400*day)
	touch(t, filepath.Join(data, "projects", "aaaaaaaaaaa1", "original.3mf"), 400*day) // same name, different folder

	c := a.cfg()
	c.JobKeepDays = 0 // keep forever: nothing happens
	a.setCfg(c)
	if n, _ := a.cleanupJobs(time.Now()); n != 0 || !exists(filepath.Join(data, "jobs", "aaaaaaaaaaa1")) {
		t.Fatal("cleanup ran although it is turned off")
	}
	c.JobKeepDays = 30
	a.setCfg(c)
	n, freed := a.cleanupJobs(time.Now())
	if n != 3 || freed != 40 {
		t.Errorf("removed %d jobs, %d bytes; want 3 jobs, 40 bytes (3 jobs + 1 orphan folder)", n, freed)
	}
	for id, want := range map[string]bool{"aaaaaaaaaaa1": false, "aaaaaaaaaaa2": false, "aaaaaaaaaaa3": false, "aaaaaaaaaaa4": true,
		"aaaaaaaaaaa5": true, "bbbbbbbbbbb1": false, "bbbbbbbbbbb2": true, "not-a-job": true} {
		if got := exists(filepath.Join(data, "jobs", id)); got != want {
			t.Errorf("jobs/%s: exists=%v, want %v", id, got, want)
		}
	}
	if !exists(filepath.Join(data, "projects", "aaaaaaaaaaa1", "original.3mf")) {
		t.Error("a project file was removed")
	}
	left := map[string]bool{}
	for _, j := range a.Queue.List() {
		left[j.ID] = true
	}
	if len(left) != 2 || !left["aaaaaaaaaaa4"] || !left["aaaaaaaaaaa5"] {
		t.Errorf("queue now has %v", left)
	}
	// And it is saved: a restart doesn't bring them back.
	var saved []*slicer.Job
	a.Store.Load("jobs", &saved)
	if len(saved) != 2 {
		t.Errorf("jobs.json has %d jobs, want 2", len(saved))
	}
}

func TestCleanupLeftovers(t *testing.T) {
	a := guardApp(t)
	data := a.Store.Dir()
	touch(t, filepath.Join(data, "slicer", "inbox", "abc.3mf"), 3*time.Hour)
	touch(t, filepath.Join(data, "slicer", "inbox", "new.3mf"), time.Minute) // just handed over: in use
	touch(t, filepath.Join(data, "slicer", "job-1", "plate.3mf"), 300*time.Hour)
	touch(t, filepath.Join(data, "upload-123456"), 3*time.Hour)
	touch(t, filepath.Join(data, "lib-99"), 3*time.Hour)
	touch(t, filepath.Join(data, "copy-5"), time.Minute)
	touch(t, filepath.Join(data, "worker-777", "in-a.stl"), 3*time.Hour)
	touch(t, filepath.Join(data, "upload-notes.txt"), 300*time.Hour) // looks similar, isn't ours
	touch(t, filepath.Join(data, "printers.json"), 300*time.Hour)
	n, _ := a.cleanupLeftovers(time.Now())
	if n != 4 {
		t.Errorf("removed %d, want 4", n)
	}
	for p, want := range map[string]bool{"slicer/inbox/abc.3mf": false, "slicer/inbox/new.3mf": true, "slicer/job-1/plate.3mf": true,
		"upload-123456": false, "lib-99": false, "copy-5": true, "worker-777": false, "upload-notes.txt": true, "printers.json": true} {
		if got := exists(filepath.Join(data, filepath.FromSlash(p))); got != want {
			t.Errorf("%s: exists=%v, want %v", p, got, want)
		}
	}
}
