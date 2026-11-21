package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWantsEvent(t *testing.T) {
	man := func(fns, events []string, off bool, errStr string) Manifest {
		return Manifest{Functions: fns, Events: events, Off: off, Error: errStr}
	}
	cases := []struct {
		name string
		m    Manifest
		typ  string
		want bool
	}{
		{"no on_event", man([]string{"run"}, nil, false, ""), "print_finished", false},
		{"empty events = all", man([]string{"on_event"}, nil, false, ""), "print_finished", true},
		{"wildcard", man([]string{"on_event"}, []string{"*"}, false, ""), "anything", true},
		{"listed type", man([]string{"on_event"}, []string{"print_finished", "bed_cleared"}, false, ""), "bed_cleared", true},
		{"unlisted type", man([]string{"on_event"}, []string{"print_finished"}, false, ""), "print_started", false},
		{"turned off", man([]string{"on_event"}, nil, true, ""), "print_finished", false},
		{"failed to load", man([]string{"on_event"}, nil, false, "boom"), "print_finished", false},
	}
	for _, c := range cases {
		if got := wantsEvent(c.m, c.typ); got != c.want {
			t.Errorf("%s: wantsEvent = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestEventDispatch runs a real plugin host with a plugin that records every
// event it is handed, and checks that dispatch delivers the types the plugin
// asked for and withholds the ones it didn't. Also checks the recent-events ring.
func TestEventDispatch(t *testing.T) {
	data := t.TempDir()
	m := New(data)
	dir := filepath.Join(data, "plugins")
	log := filepath.Join(data, "events.log")
	mk := func(name, body string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A watcher that wants only print_finished and bed_cleared, and logs each one.
	mk("watcher/plugin.json", `{"id":"watcher","name":"Watcher","kind":"tool","events":["print_finished","bed_cleared"],
		"settings":[{"key":"logfile","label":"Log file","type":"text","default":""}]}`)
	mk("watcher/plugin.py", `import json
def on_event(ctx, event=None):
    with open(ctx["settings"]["logfile"], "a") as f:
        f.write(json.dumps(event) + "\n")
def run(ctx, params=None):
    return {"ok": True}
`)

	m.Start(context.Background())
	defer m.Stop()
	if why := m.Off(); why != "" {
		t.Skipf("plugin host unavailable: %s", why)
	}
	m.SetSettings("watcher", map[string]any{"logfile": log})

	// dispatch is synchronous: the host has written the file by the time it returns.
	m.dispatch(map[string]any{"type": "print_finished", "printer_id": "p1", "plate": 2})
	m.dispatch(map[string]any{"type": "print_started", "printer_id": "p1"}) // not wanted
	m.dispatch(map[string]any{"type": "bed_cleared", "printer_id": "p1"})

	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		got = append(got, ev["type"].(string))
	}
	want := []string{"print_finished", "bed_cleared"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("delivered %v, want %v (print_started must be withheld)", got, want)
	}

	// Emit (the public path) records into the recent ring even with no listener.
	// (The list keeps the last 50 and is shared, so look at the newest entry, not the length.)
	m.Emit(map[string]any{"type": "printer_state", "from": "idle", "to": "printing", "mark": "dispatch-test"})
	rec := m.RecentEvents()
	if len(rec) == 0 || rec[len(rec)-1]["mark"] != "dispatch-test" || rec[len(rec)-1]["time"] == nil {
		t.Errorf("recent ring: %+v", rec)
	}
}
