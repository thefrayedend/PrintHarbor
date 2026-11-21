package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/slicer"
)

// After a print, PrintHarbor deducts the filament it used from the pinned
// Spoolman spool — but only when write-back is on and the print maps to exactly
// one spool, so it can't deduct from the wrong roll.
func TestSpoolmanWriteback(t *testing.T) {
	var mu sync.Mutex
	var gotID string
	var gotWeight float64
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Path == "/api/v1/spool/42/use" {
			var body struct {
				UseWeight float64 `json:"use_weight"`
			}
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &body)
			mu.Lock()
			calls++
			gotID, gotWeight = "42", body.UseWeight
			mu.Unlock()
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	newApp := func(writeback bool) *App {
		cfg := config.Defaults()
		cfg.DataDir = filepath.Join(t.TempDir(), "data")
		cfg.OrcaPath = "/none"
		cfg.SpoolmanURL = srv.URL
		cfg.SpoolmanWriteback = writeback
		a, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	project := func(a *App, locks map[int]int) {
		a.projMu.Lock()
		a.projects["p1"] = &Project{ID: "p1", Name: "Dragon", Locks: locks}
		a.projMu.Unlock()
	}
	job := slicer.Job{ProjectID: "p1", ProjectName: "Dragon", FilamentG: 12.5}

	// On, one locked spool: it deducts the used grams.
	a := newApp(true)
	project(a, map[int]int{1: 42})
	a.spoolmanUse(job)
	mu.Lock()
	if calls != 1 || gotID != "42" || gotWeight < 12.4 || gotWeight > 12.6 {
		t.Errorf("writeback: calls=%d id=%q weight=%v, want 1 / 42 / 12.5", calls, gotID, gotWeight)
	}
	mu.Unlock()

	// Two locked spools: ambiguous, so it must NOT guess.
	calls = 0
	project(a, map[int]int{1: 42, 2: 7})
	a.spoolmanUse(job)
	mu.Lock()
	if calls != 0 {
		t.Errorf("multi-spool print should not write back, but made %d call(s)", calls)
	}
	mu.Unlock()

	// Write-back off: nothing happens.
	calls = 0
	b := newApp(false)
	project(b, map[int]int{1: 42})
	b.spoolmanUse(job)
	mu.Lock()
	if calls != 0 {
		t.Errorf("write-back off should send nothing, but made %d call(s)", calls)
	}
	mu.Unlock()
}
