package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/slicer"
)

// Your print defaults (Settings → Printing) take the place of what a stock
// profile says. What you set yourself still wins: a setting saved in one of
// your own presets, and anything changed for one print.
func TestPrintDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.OrcaPath = "/none"
	cfg.OrcaProfilesDir = filepath.Join(dir, "profiles")
	cfg.OrcaUserPresetsDir = ""
	write(t, filepath.Join(dir, "profiles", "Acme", "process", "common.json"),
		`{"type":"process","name":"fdm_common","instantiation":"false","support_type":"normal(auto)","wall_loops":"2","sparse_infill_density":"15%","enable_support":"0"}`)
	write(t, filepath.Join(dir, "profiles", "Acme", "process", "std.json"),
		`{"type":"process","name":"0.20 Standard @Acme","inherits":"fdm_common","instantiation":"true","layer_height":"0.2"}`)
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.CfgPath = filepath.Join(dir, "config.json")
	if err := a.SavePreset("process", "Mine", "0.20 Standard @Acme", map[string]string{"wall_loops": "5"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler(nil))
	defer srv.Close()
	post := func(defs map[string]string) (int, string) {
		b, _ := json.Marshal(map[string]any{"defaults": defs})
		resp, err := http.Post(srv.URL+"/api/print-defaults", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out bytes.Buffer
		out.ReadFrom(resp.Body)
		return resp.StatusCode, out.String()
	}
	val := func(preset, key string) string {
		p, err := a.resolveProcess(a.Lib(), preset, "")
		if err != nil {
			t.Fatal(err)
		}
		s, _ := p[key].(string)
		return s
	}
	const std = "0.20 Standard @Acme"

	// Nothing set: the profile as it comes.
	if val(std, "support_type") != "normal(auto)" || val(std, "wall_loops") != "2" {
		t.Fatal("the stock profile isn't what the test expects")
	}
	if c, body := post(map[string]string{"support_type": "tree(auto)", "wall_loops": "3", "sparse_infill_density": "20", "brim_type": ""}); c != 200 {
		t.Fatal(c, body)
	}
	// A stock profile now gives your values...
	if val(std, "support_type") != "tree(auto)" || val(std, "wall_loops") != "3" || val(std, "sparse_infill_density") != "20%" {
		t.Errorf("stock profile with defaults: %s, %s walls, %s", val(std, "support_type"), val(std, "wall_loops"), val(std, "sparse_infill_density"))
	}
	if val(std, "enable_support") != "0" || val(std, "layer_height") != "0.2" {
		t.Error("a setting with no default was changed")
	}
	// ...and so does what the send screen and the slicer are shown.
	resp, _ := http.Get(srv.URL + "/api/profiles/resolve?type=process&name=" + strings.ReplaceAll(std, " ", "%20"))
	var shown map[string]any
	json.NewDecoder(resp.Body).Decode(&shown)
	resp.Body.Close()
	if shown["support_type"] != "tree(auto)" {
		t.Errorf("the send screen is shown %v", shown["support_type"])
	}
	// Your own preset: what you saved in it stays, the rest follows your defaults.
	if val("Mine", "wall_loops") != "5" || val("Mine", "support_type") != "tree(auto)" {
		t.Errorf("own preset: %s walls, %s", val("Mine", "wall_loops"), val("Mine", "support_type"))
	}
	// They are kept in config.json, and listed back.
	saved, _ := config.Load(a.CfgPath)
	if saved.PrintDefaults["support_type"] != "tree(auto)" || len(saved.PrintDefaults) != 3 {
		t.Errorf("config.json: %v", saved.PrintDefaults)
	}
	resp, _ = http.Get(srv.URL + "/api/print-defaults")
	var listed struct {
		Defaults map[string]string `json:"defaults"`
		Settings []struct {
			Key string `json:"key"`
			For string `json:"for"`
		} `json:"settings"`
	}
	json.NewDecoder(resp.Body).Decode(&listed)
	resp.Body.Close()
	if listed.Defaults["wall_loops"] != "3" || len(listed.Settings) < 10 {
		t.Errorf("listed: %v, %d settings", listed.Defaults, len(listed.Settings))
	}
	for _, s := range listed.Settings {
		if s.For != "process" {
			t.Errorf("%s is a %s setting; only quality settings can have a default", s.Key, s.For)
		}
	}
	// Nonsense is refused and changes nothing.
	for name, bad := range map[string]map[string]string{
		"not a choice":       {"support_type": "banana"},
		"not a setting":      {"gcode_start": "M112"},
		"a filament setting": {"nozzle_temperature": "300"},
		"not a number":       {"wall_loops": "lots"},
		"over 100 %":         {"sparse_infill_density": "250"},
		"not on or off":      {"enable_support": "maybe"},
	} {
		if c, _ := post(bad); c != 400 {
			t.Errorf("%s: accepted (%d)", name, c)
		}
	}
	if val(std, "support_type") != "tree(auto)" || val(std, "wall_loops") != "3" {
		t.Error("a refused save changed the defaults")
	}
	// Saving the other settings doesn't lose them.
	b, _ := json.Marshal(a.settings())
	if resp, err := http.Post(srv.URL+"/api/settings", "application/json", bytes.NewReader(b)); err != nil || resp.StatusCode != 200 {
		t.Fatal(err, resp.Status)
	}
	if val(std, "support_type") != "tree(auto)" {
		t.Error("saving Settings dropped the print defaults")
	}
	// Cleared: the profile as it comes again.
	if c, _ := post(map[string]string{}); c != 200 || val(std, "support_type") != "normal(auto)" || val("Mine", "wall_loops") != "5" {
		t.Errorf("after clearing: %s", val(std, "support_type"))
	}
}

type sendRecorder struct {
	calls chan slicer.Job
	fail  error
}

// Slice, look at the preview, then send: a job that was only sliced can be
// sent to its printer as it is.
func TestSendAfterPreview(t *testing.T) {
	a := guardApp(t)
	out := filepath.Join(t.TempDir(), "plate_1.gcode")
	os.WriteFile(out, []byte("G28\n"), 0o644)
	now := time.Now()
	a.Store.Save("jobs", []*slicer.Job{
		{ID: "bbbbbbbbbb01", ProjectName: "P", Plate: 1, PrinterID: "x", Machine: "m", Process: "p", State: slicer.Done, GCode: out, Created: now, Finished: now},
		{ID: "bbbbbbbbbb02", ProjectName: "P", Plate: 1, PrinterID: "x", Machine: "m", Process: "p", State: slicer.Done, Send: true, Start: true, GCode: out, Created: now, Finished: now},
		{ID: "bbbbbbbbbb03", ProjectName: "P", Plate: 1, PrinterID: "x", Machine: "m", Process: "p", State: slicer.Failed, Created: now, Finished: now},
		{ID: "bbbbbbbbbb04", ProjectName: "P", Plate: 1, PrinterID: "x", Machine: "m", Process: "p", State: slicer.Done, Created: now, Finished: now}, // its file is gone
		{ID: "bbbbbbbbbb05", ProjectName: "P", Plate: 1, PrinterID: "x", Machine: "m", Process: "p", State: slicer.Done, GCode: out, Created: now, Finished: now},
	})
	rec := &sendRecorder{calls: make(chan slicer.Job, 4)}
	q, err := slicer.NewQueue(a.Store, a.Slicer, a.prepare, func(ctx context.Context, j *slicer.Job) error {
		rec.calls <- *j
		return rec.fail
	}, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a.Queue = q
	srv := httptest.NewServer(a.Handler(nil))
	defer srv.Close()
	send := func(id string, start bool) int {
		b, _ := json.Marshal(map[string]bool{"start": start})
		resp, err := http.Post(srv.URL+"/api/jobs/"+id+"/send", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	wait := func(id string, ok func(slicer.Job) bool) slicer.Job {
		for i := 0; i < 100; i++ {
			if j, _ := a.Queue.Get(id); ok(j) {
				return j
			}
			time.Sleep(20 * time.Millisecond)
		}
		j, _ := a.Queue.Get(id)
		t.Fatalf("%s never got there: %+v", id, j)
		return j
	}
	// The sliced job is sent and started.
	if c := send("bbbbbbbbbb01", true); c != 200 {
		t.Fatal(c)
	}
	got := <-rec.calls
	if got.ID != "bbbbbbbbbb01" || !got.Send || !got.Start || got.GCode != out {
		t.Errorf("sent %+v", got)
	}
	j := wait("bbbbbbbbbb01", func(j slicer.Job) bool { return j.State == slicer.Done && j.Send })
	if !j.Start || j.Error != "" {
		t.Errorf("after sending: %+v", j)
	}
	// Not twice, and not a job that was already sent, failed, or lost its file.
	for _, id := range []string{"bbbbbbbbbb01", "bbbbbbbbbb02", "bbbbbbbbbb03", "bbbbbbbbbb04", "nope"} {
		if c := send(id, true); c != 400 {
			t.Errorf("%s: sent again (%d)", id, c)
		}
	}
	select {
	case j := <-rec.calls:
		t.Errorf("something else was sent: %s", j.ID)
	default:
	}
	// The printer refuses it: the job goes back to "sliced" with the reason, and can be sent again.
	rec.fail = context.DeadlineExceeded
	if c := send("bbbbbbbbbb05", false); c != 200 {
		t.Fatal(c)
	}
	<-rec.calls
	j = wait("bbbbbbbbbb05", func(j slicer.Job) bool { return j.State == slicer.Done && j.Error != "" })
	if j.Send || j.Start || !strings.Contains(j.Error, "Sending failed") {
		t.Errorf("after a failed send: %+v", j)
	}
	rec.fail = nil
	if c := send("bbbbbbbbbb05", false); c != 200 {
		t.Fatal(c)
	}
	<-rec.calls
	if j = wait("bbbbbbbbbb05", func(j slicer.Job) bool { return j.Send && j.State == slicer.Done }); j.Error != "" || j.Start {
		t.Errorf("after sending again: %+v", j)
	}
}
