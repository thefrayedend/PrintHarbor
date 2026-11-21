package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/printers"
	"github.com/thefrayedend/printharbor/internal/slicer"
)

func TestWorkerSlicing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	dir := t.TempDir()
	orca := filepath.Join(dir, "orca-slicer")
	write(t, orca, "#!/bin/sh\noutdir=\"\"; prev=\"\"; for a in \"$@\"; do [ \"$prev\" = \"--outputdir\" ] && outdir=\"$a\"; prev=\"$a\"; done\necho 'G28 ; from worker' > \"$outdir/plate_1.gcode\"\n")
	wcfg := config.Defaults()
	wcfg.DataDir, wcfg.OrcaPath, wcfg.WorkerToken = filepath.Join(dir, "worker"), orca, "s3cret"
	worker, err := New(wcfg)
	if err != nil {
		t.Fatal(err)
	}
	wsrv := httptest.NewServer(worker.Handler(nil))
	defer wsrv.Close()

	hcfg := config.Defaults()
	hcfg.DataDir, hcfg.OrcaPath, hcfg.Workers, hcfg.WorkerToken = filepath.Join(dir, "hub"), "/no/orca/here", []string{wsrv.URL}, "s3cret"
	hub, err := New(hcfg)
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(dir, "part.stl")
	os.WriteFile(in, []byte("solid x\nendsolid x\n"), 0o644)
	req := slicer.Request{Inputs: []string{in}, Plate: 1, Machine: map[string]any{"name": "m"}, Process: map[string]any{"name": "p"},
		Filaments: []map[string]any{{"name": "f"}}, WorkDir: filepath.Join(dir, "job")}
	res, err := hub.Slicer.Slice(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	g, _ := os.ReadFile(res.GCode)
	if !strings.Contains(string(g), "from worker") || !strings.Contains(res.Log, "sliced on worker") {
		t.Fatalf("gcode %q log %q", g, res.Log)
	}
	// Wrong key: the worker refuses, the hub falls back to local Orca (missing here).
	hc := hub.cfg()
	hc.WorkerToken = "wrong"
	hub.setCfg(hc)
	if _, err := hub.Slicer.Slice(context.Background(), req); err == nil || !strings.Contains(err.Error(), "orca_path") {
		t.Fatalf("want local fallback error, got %v", err)
	}
	resp, _ := http.Get(wsrv.URL + "/api/worker/ping")
	if resp.StatusCode != 403 {
		t.Fatal("ping without key must be refused")
	}
}

func TestAssistant(t *testing.T) {
	var mu sync.Mutex
	step := 0
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Messages []chatMsg `json:"messages"`
			Tools    []any     `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		step++
		s := step
		mu.Unlock()
		last := in.Messages[len(in.Messages)-1]
		switch {
		case s == 1:
			io.WriteString(w, `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"get_printers","arguments":{}}}]}}`)
		case s == 2:
			if last.Role != "tool" || !strings.Contains(last.Content, `"name":"Mini"`) {
				io.WriteString(w, `{"error":"tool result missing"}`)
				return
			}
			io.WriteString(w, `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"propose_printer_action","arguments":{"printer":"mini","action":"pause"}}}]}}`)
		default:
			io.WriteString(w, `{"message":{"role":"assistant","content":"Mini is printing. I can pause it once you confirm."}}`)
		}
	}))
	defer ollama.Close()
	cfg := config.Defaults()
	cfg.DataDir, cfg.OllamaURL = filepath.Join(t.TempDir(), "d"), ollama.URL
	a, _ := New(cfg)
	p, _ := a.Printers.Put(printers.Printer{Name: "Mini", Driver: "mock", Address: "60"})
	ctx := context.Background()
	a.Printers.Send(ctx, p.ID, "x.gcode", strings.NewReader("G28"), true)
	a.Printers.PollOnce(ctx)
	reply, pending, err := a.Ask(ctx, []chatMsg{{Role: "user", Content: "is anything printing? pause the mini"}})
	if err != nil || !strings.Contains(reply, "confirm") || len(pending) != 1 || pending[0].Action != "pause" {
		t.Fatalf("reply %q pending %+v err %v", reply, pending, err)
	}
	// Nothing happened until confirmed.
	if v, _ := a.Printers.Get(p.ID); v.Status.State != printers.StatePrinting {
		t.Fatal("assistant must not act without confirmation")
	}
	srv := httptest.NewServer(a.Handler(nil))
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/api/assistant/confirm/"+pending[0].ID, "application/json", nil)
	if resp.StatusCode != 200 {
		t.Fatal("confirm failed", resp.Status)
	}
	a.Printers.PollOnce(ctx)
	if v, _ := a.Printers.Get(p.ID); v.Status.State != printers.StatePaused {
		t.Fatal("confirmed pause not applied")
	}
}
