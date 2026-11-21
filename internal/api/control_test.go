package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/printers"
)

func TestControlGcode(t *testing.T) {
	idle := printers.View{Status: printers.Status{State: printers.StateIdle, Homed: "xyz"}}
	printing := printers.View{Status: printers.Status{State: printers.StatePrinting}}
	cases := []struct {
		req  ControlRequest
		v    printers.View
		want string
		err  string
	}{
		{ControlRequest{Op: "temp", Heater: "nozzle", Value: 215}, idle, "M104 S215", ""},
		{ControlRequest{Op: "temp", Heater: "bed", Value: 200}, idle, "", "0–130"},
		{ControlRequest{Op: "home", Axis: "xy"}, idle, "G28 X Y", ""},
		{ControlRequest{Op: "home"}, printing, "", "while a print"},
		{ControlRequest{Op: "jog", Axis: "z", Distance: -1}, idle, "G91\nG1 Z-1.00 F600\nG90", ""},
		{ControlRequest{Op: "jog", Axis: "x", Distance: 10}, printers.View{Status: printers.Status{State: printers.StateIdle, Homed: "z"}}, "", "home X"},
		{ControlRequest{Op: "fan", Value: 50}, printing, "M106 S128", ""},
		{ControlRequest{Op: "fan", Value: 0}, printing, "M107", ""},
		{ControlRequest{Op: "speed", Value: 120}, printing, "M220 S120", ""},
		{ControlRequest{Op: "flow", Value: 300}, printing, "", "50–150"},
		{ControlRequest{Op: "zoffset", Distance: 0.02}, printing, "SET_GCODE_OFFSET Z_ADJUST=0.020 MOVE=1", ""},
	}
	for _, c := range cases {
		got, err := controlGcode(c.req, c.v, "moonraker")
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%+v: want error %q, got %q %v", c.req, c.err, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%+v: got %q %v want %q", c.req, got, err, c.want)
		}
	}
	if _, err := controlGcode(ControlRequest{Op: "zoffset", Distance: 0.02}, printing, "bambu"); err == nil {
		t.Error("z offset is Klipper-only")
	}
}

func TestControlOnMock(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(t.TempDir(), "d")
	a, _ := New(cfg)
	p, _ := a.Printers.Put(printers.Printer{Name: "Sim", Driver: "mock", Address: "60"})
	ctx := context.Background()
	a.Printers.PollOnce(ctx)
	g := func(script string) {
		if err := a.Printers.Gcode(ctx, p.ID, script); err != nil {
			t.Fatal(err)
		}
	}
	g("G28")
	g("G91\nG1 X10 F6000\nG90")
	g("M104 S200")
	g("M106 S255")
	a.Printers.PollOnce(ctx)
	v, _ := a.Printers.Get(p.ID)
	if v.Status.Homed != "xyz" || v.Status.Position[0] != 10 || v.Status.NozzleTarget != 200 || v.Status.Fan != 1 {
		t.Fatalf("%+v", v.Status)
	}
	if !v.Capabilities.Jog || !v.Capabilities.Console {
		t.Fatal("mock capabilities")
	}
	lines, _ := a.Printers.Console(ctx, p.ID)
	if len(lines) < 8 || lines[0].Message != "G28" {
		t.Fatalf("console %+v", lines)
	}
	if h := a.Printers.History(p.ID); len(h) < 2 || h[len(h)-1].NozzleTarget != 200 {
		t.Fatalf("history %+v", h)
	}
	a.Printers.Send(ctx, p.ID, "part.gcode", strings.NewReader("G28"), false)
	fm, _ := a.Printers.FileManager(p.ID)
	files, _ := fm.Files(ctx)
	if len(files) != 1 || files[0].Name != "part.gcode" {
		t.Fatal(files)
	}
}
