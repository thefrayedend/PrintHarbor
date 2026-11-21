package plugins

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/printers"
)

func testPlugins(t *testing.T, files map[string]string) (*Manager, string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		if _, err := exec.LookPath("python"); err != nil {
			t.Skip("Python 3 isn't installed")
		}
	}
	data := t.TempDir()
	m := New(data)
	for rel, body := range files {
		p := filepath.Join(data, "plugins", filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m.Start(context.Background())
	t.Cleanup(m.Stop)
	if why := m.Off(); why != "" {
		t.Skipf("plugin host unavailable: %s", why)
	}
	return m, data
}

// Events reach a plugin in the order they happened, even when the plugin is
// slow over some of them, and Emit never waits for the plugin.
func TestEventsArriveInOrder(t *testing.T) {
	m, data := testPlugins(t, map[string]string{
		"slow/plugin.json": `{"id":"slow","name":"Slow","kind":"tool","settings":[{"key":"logfile","label":"Log","type":"text","default":""}]}`,
		"slow/plugin.py": `import time
def on_event(ctx, event=None):
    if event["n"] % 5 == 0:
        time.sleep(0.15)      # every fifth one is slow: the next ones must still wait their turn
    with open(ctx["settings"]["logfile"], "a") as f:
        f.write(str(event["n"]) + "\n")
def run(ctx, params=None):
    return {"ok": True}
`})
	logf := filepath.Join(data, "order.log")
	m.SetSettings("slow", map[string]any{"logfile": logf})
	const n = 30
	began := time.Now()
	var want []string
	for i := 0; i < n; i++ {
		m.Emit(map[string]any{"type": "printer_state", "n": i})
		want = append(want, fmt.Sprint(i))
	}
	if d := time.Since(began); d > 500*time.Millisecond {
		t.Errorf("Emit waited for the plugin (%v)", d)
	}
	var got []string
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		b, _ := os.ReadFile(logf)
		got = strings.Fields(string(b))
		if len(got) >= n {
			break
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("events arrived as %v", got)
	}
}

// A plugin that hangs on one event is given up on after eventLimit, and the
// events behind it are still delivered.
func TestStuckPluginDoesNotBlockEvents(t *testing.T) {
	old := eventLimit
	eventLimit = 400 * time.Millisecond
	defer func() { eventLimit = old }()
	m, data := testPlugins(t, map[string]string{
		"stuck/plugin.json": `{"id":"stuck","name":"Stuck","kind":"tool","settings":[{"key":"logfile","label":"Log","type":"text","default":""}]}`,
		"stuck/plugin.py": `import time
def on_event(ctx, event=None):
    if event["type"] == "print_started":
        time.sleep(3)
    with open(ctx["settings"]["logfile"], "a") as f:
        f.write(event["type"] + "\n")
def run(ctx, params=None):
    return {"ok": True}
`})
	logf := filepath.Join(data, "stuck.log")
	m.SetSettings("stuck", map[string]any{"logfile": logf})
	began := time.Now()
	m.Emit(map[string]any{"type": "print_started"})
	m.Emit(map[string]any{"type": "print_finished"})
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if b, _ := os.ReadFile(logf); strings.Contains(string(b), "print_finished") {
			if d := time.Since(began); d > 2500*time.Millisecond {
				t.Errorf("the next event waited %v for the stuck plugin", d)
			}
			return
		}
	}
	t.Error("the event after the stuck one never arrived")
}

// Sending a file to a printer may take far longer than an ordinary plugin
// call is allowed. It used to be cut off at the ordinary limit (45 s), which a
// large print over Wi-Fi easily passes.
func TestPrinterUploadOutlivesTheUsualLimit(t *testing.T) {
	m, _ := testPlugins(t, map[string]string{
		"slowprinter/plugin.json": `{"id":"slowprinter","name":"Slow Printer","kind":"printer","output":"gcode"}`,
		"slowprinter/plugin.py": `import time
sent = {}
def status(ctx, printer=None):
    if sent.get("slow_status"):
        time.sleep(1.2)
    return {"state": "printing" if sent.get("f") else "idle"}
def upload(ctx, printer=None, file=None, name=None, start=False):
    time.sleep(1.2)           # a big file on slow Wi-Fi
    sent["f"] = name
    sent["slow_status"] = True
    return {"ok": True}
`})
	m.client.Timeout = 500 * time.Millisecond // stands in for the usual 45 s
	var man Manifest
	for _, mm := range m.List() {
		if mm.ID == "slowprinter" {
			man = mm
		}
	}
	d := &pyPrinter{m: m, id: "slowprinter", man: man, p: printers.Printer{ID: "p1"}}
	if err := d.Upload(context.Background(), "big.gcode", strings.NewReader("G28\n"), true); err != nil {
		t.Fatalf("a slow upload was cut off: %v", err)
	}
	// Everything else keeps the usual limit, so a hung plugin can't hang a page.
	if _, err := d.Status(context.Background()); err == nil {
		t.Error("a slow status call wasn't cut off at the usual limit")
	}
	// Giving up on the send (the job was cancelled) still stops the upload.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := d.Upload(ctx, "big.gcode", strings.NewReader("G28\n"), true); err == nil {
		t.Error("a cancelled upload carried on")
	}
}
