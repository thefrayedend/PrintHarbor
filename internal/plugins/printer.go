package plugins

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/thefrayedend/printharbor/internal/printers"
)

// Printer plugins (kind "printer"): a Python plugin that adds a printer
// PrintHarbor can control, without a new Go driver. The plugin implements, with
// the configured printer passed in each call:
//
//	status(ctx, printer)                 -> dict matching printers.Status JSON
//	upload(ctx, printer, file, name, start)  send the sliced file at path `file`
//	pause/resume/cancel(ctx, printer)    optional
//	snapshot(ctx, printer)               optional: {data:<base64>, content_type}
//	light(ctx, printer, on)              optional
//
// pyPrinter adapts one of these to printers.Driver. RegisterPrinterDrivers wires
// each printer plugin into the printer registry so it shows up in "Add printer".

type pyPrinter struct {
	m   *Manager
	id  string
	p   printers.Printer
	man Manifest
}

// printerArg is the configured printer the plugin talks to.
func (d *pyPrinter) printerArg() map[string]any {
	slots := make([]map[string]any, 0, len(d.p.Slots))
	for _, s := range d.p.Slots {
		slots = append(slots, map[string]any{"index": s.Index, "type": s.Type, "color": s.Color, "name": s.Name})
	}
	return map[string]any{
		"id": d.p.ID, "name": d.p.Name, "address": d.p.Address, "api_key": d.p.APIKey,
		"serial": d.p.Serial, "machine_preset": d.p.MachinePreset, "slots": slots,
	}
}

func (d *pyPrinter) has(fn string) bool {
	for _, man := range d.m.List() {
		if man.ID == d.id {
			for _, f := range man.Functions {
				if f == fn {
					return true
				}
			}
		}
	}
	return false
}

func (d *pyPrinter) call(fn string, extra map[string]any, out any) error {
	return d.callWithin(context.Background(), 0, fn, extra, out)
}

// uploadLimit is how long a plugin may take to send one sliced file to its
// printer. A big multi-colour G-code over Wi-Fi takes minutes, far longer than
// the 45 seconds every other plugin call gets.
const uploadLimit = 30 * time.Minute

func (d *pyPrinter) callWithin(ctx context.Context, limit time.Duration, fn string, extra map[string]any, out any) error {
	args := map[string]any{"printer": d.printerArg()}
	for k, v := range extra {
		args[k] = v
	}
	return d.m.rpcWithin(ctx, limit, d.id, fn, args, out)
}

func (d *pyPrinter) Capabilities() printers.Capabilities {
	return printers.Capabilities{
		Status:        d.has("status"),
		Temperatures:  d.has("status"),
		FilamentSlots: d.has("status"),
		Pause:         d.has("pause"),
		Resume:        d.has("resume"),
		Cancel:        d.has("cancel"),
		Upload:        d.has("upload"),
		StartPrint:    d.has("upload"),
		Camera:        d.has("snapshot"),
		Light:         d.has("light"),
	}
}

func (d *pyPrinter) Status(ctx context.Context) (printers.Status, error) {
	var st printers.Status
	if err := d.call("status", nil, &st); err != nil {
		return printers.Status{State: printers.StateOffline, Message: err.Error()}, err
	}
	if st.State == "" {
		st.State = printers.StateIdle
	}
	return st, nil
}

func (d *pyPrinter) Pause(ctx context.Context) error  { return d.call("pause", nil, nil) }
func (d *pyPrinter) Resume(ctx context.Context) error { return d.call("resume", nil, nil) }
func (d *pyPrinter) Cancel(ctx context.Context) error { return d.call("cancel", nil, nil) }

func (d *pyPrinter) OutputFormat() string {
	if d.man.Output == "gcode.3mf" {
		return "gcode.3mf"
	}
	return "gcode"
}

// Upload writes the sliced file to a temp path and hands that path to the
// plugin, which sends it to the printer. The host and plugins run on the same
// machine, so a path is the simplest reliable hand-off for a big file.
func (d *pyPrinter) Upload(ctx context.Context, filename string, r io.Reader, start bool) error {
	tmp, err := os.CreateTemp("", "ph-plugin-upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return d.callWithin(ctx, uploadLimit, "upload", map[string]any{"file": tmp.Name(), "name": filename, "start": start}, nil)
}

// Snapshot asks the plugin for a camera frame (base64 in the result).
func (d *pyPrinter) Snapshot(ctx context.Context) ([]byte, string, error) {
	var r struct {
		Data        string `json:"data"`
		ContentType string `json:"content_type"`
	}
	if err := d.call("snapshot", nil, &r); err != nil {
		return nil, "", err
	}
	b, err := base64.StdEncoding.DecodeString(r.Data)
	if err != nil {
		return nil, "", fmt.Errorf("the plugin's snapshot wasn't valid base64: %w", err)
	}
	ct := r.ContentType
	if ct == "" {
		ct = "image/jpeg"
	}
	return b, ct, nil
}

// SetLight switches the printer's light, if the plugin supports it.
func (d *pyPrinter) SetLight(ctx context.Context, on bool) error {
	return d.call("light", map[string]any{"on": on}, nil)
}

// RegisterPrinterDrivers registers every loaded printer plugin as a printer
// driver, so it appears in "Add printer" and its configured printers work. Safe
// to call again after a reload (it overwrites by driver name). A driver is never
// registered over a built-in one of the same name.
func RegisterPrinterDrivers(m *Manager) {
	builtinDrivers := map[string]bool{}
	for _, di := range printers.Drivers() {
		builtinDrivers[di.Name] = true
	}
	for _, man := range m.List() {
		if man.Kind != "printer" || man.Error != "" || man.Off {
			continue
		}
		id := man.ID
		if builtinDrivers[id] && !registeredPlugin[id] {
			continue // don't shadow a built-in driver of the same name
		}
		di := printers.DriverInfo{Name: id, Label: man.Name}
		if man.Driver != nil {
			di.AddressHint = man.Driver.AddressHint
			di.NeedsKey = man.Driver.NeedsKey
			di.KeyLabel = man.Driver.KeyLabel
			di.NeedsSerial = man.Driver.NeedsSerial
			di.Help = man.Driver.Help
		}
		mm := man
		printers.Register(di, func(p printers.Printer) (printers.Driver, error) {
			return &pyPrinter{m: m, id: id, p: p, man: mm}, nil
		})
		registeredPlugin[id] = true
	}
}

// registeredPlugin remembers driver names we created, so a reload may re-register
// them (updating the factory) without us mistaking them for built-ins.
var registeredPlugin = map[string]bool{}
