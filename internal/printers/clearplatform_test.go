package printers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/store"
)

// Like FlashBridge (bridge.dart: "A finished/cancelled job must be acknowledged
// (plate cleared) before the next one"): a FlashForge still showing a finished
// or cancelled job is sent "clear platform" before a print is started on it.
func TestFlashForgeClearsAFinishedJobBeforeTheNext(t *testing.T) {
	old := ffClearWait
	ffClearWait = time.Millisecond
	defer func() { ffClearWait = old }()
	var mu sync.Mutex
	status, calls := "completed", []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/uploadGcode":
			io.Copy(io.Discard, r.Body)
			calls = append(calls, "upload")
		case "/printGcode":
			calls = append(calls, "print")
		case "/control":
			var body struct {
				Payload struct {
					Cmd  string         `json:"cmd"`
					Args map[string]any `json:"args"`
				} `json:"payload"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Payload.Cmd == "stateCtrl_cmd" && body.Payload.Args["action"] == "setClearPlatform" {
				calls = append(calls, "clear")
			}
		case "/detail":
			io.WriteString(w, `{"code":0,"message":"Success","detail":{"status":"`+status+`","printProgress":1,"printFileName":"old.gcode"}}`)
			return
		}
		io.WriteString(w, `{"code":0,"message":"Success"}`)
	}))
	defer srv.Close()
	f, err := NewFlashForge(srv.URL, "SN1", "abc")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	did := func() string {
		mu.Lock()
		defer mu.Unlock()
		s := strings.Join(calls, ",")
		calls = nil
		return s
	}
	set := func(s string) { mu.Lock(); status = s; mu.Unlock() }
	for _, st := range []string{"completed", "cancel"} {
		set(st)
		if err := f.Upload(ctx, "new.gcode.3mf", strings.NewReader("x"), true); err != nil {
			t.Fatal(err)
		}
		if got := did(); got != "clear,upload" {
			t.Errorf("printer showing %q, send and print: did %q, want clear then upload", st, got)
		}
		if err := f.StartFile(ctx, "onprinter.gcode"); err != nil {
			t.Fatal(err)
		}
		if got := did(); got != "clear,print" {
			t.Errorf("printer showing %q, print a file on it: did %q, want clear then print", st, got)
		}
	}
	// Only uploading (not starting) leaves the printer's message alone.
	set("completed")
	f.Upload(ctx, "new.gcode.3mf", strings.NewReader("x"), false)
	if got := did(); got != "upload" {
		t.Errorf("upload without starting: did %q", got)
	}
	// Nothing to acknowledge: nothing extra is sent.
	set("ready")
	f.Upload(ctx, "new.gcode.3mf", strings.NewReader("x"), true)
	if got := did(); got != "upload" {
		t.Errorf("idle printer: did %q", got)
	}
}

type clearable struct {
	scripted
	cleared int
}

func (c *clearable) ClearPlatform(context.Context) error {
	c.cleared++
	c.set(Status{State: StateIdle}) // the printer drops its "finished" message
	return nil
}

// "Auto clear platform" clears the printer's message and nothing else: the bed
// still counts as not cleared, so nothing is sent onto a part that is still
// there. A person pressing Clear platform does free the bed.
func TestAutoClearDoesNotFreeTheBed(t *testing.T) {
	dev := &clearable{}
	Register(DriverInfo{Name: "clearable", Label: "Clearable"}, func(Printer) (Driver, error) { return dev, nil })
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(st)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Put(Printer{Name: "FF", Driver: "clearable", Address: "x"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	poll := func(s Status) View {
		dev.set(s)
		r.PollOnce(ctx)
		v, _ := r.Get(p.ID)
		return v
	}
	finishAPrint := func() {
		poll(Status{State: StatePrinting, Progress: 0.5})
		if v := poll(Status{State: StateFinished, Progress: 1}); v.BedClear || v.Ready {
			t.Fatalf("right after a print: %+v", v)
		}
	}
	// PrintHarbor's own automatic clear.
	finishAPrint()
	if err := r.ClearPlatformAuto(ctx, p.ID); err != nil || dev.cleared != 1 {
		t.Fatal(err, dev.cleared)
	}
	r.PollOnce(ctx)
	if v, _ := r.Get(p.ID); v.BedClear || v.Ready {
		t.Errorf("after the automatic clear the bed counts as free (bed_clear=%v, ready=%v): the next plate could be sent onto the part", v.BedClear, v.Ready)
	}
	// You then say the parts are off.
	r.SetBedClear(p.ID, true)
	if v, _ := r.Get(p.ID); !v.BedClear || !v.Ready {
		t.Errorf("after Parts removed: %+v", v)
	}
	// A person clearing it (the button here, or at the printer's own screen) frees the bed.
	finishAPrint()
	if err := r.ClearPlatform(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	r.PollOnce(ctx)
	if v, _ := r.Get(p.ID); !v.BedClear || !v.Ready {
		t.Errorf("after a person cleared the platform: bed_clear=%v ready=%v", v.BedClear, v.Ready)
	}
}
