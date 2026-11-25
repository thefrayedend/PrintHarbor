// Package slicer runs the stock OrcaSlicer CLI as a separate process and
// queues slice jobs so slow machines never run more than they can handle.
package slicer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Request is everything Orca needs to slice one plate for one printer.
// Presets are already resolved (flat JSON, no inherits).
type Request struct {
	Inputs    []string         // original 3MF, or STL/OBJ files (read only)
	Plate     int              // 1-based plate index
	Machine   map[string]any   // resolved machine preset
	Process   map[string]any   // resolved process preset with overrides applied
	Filaments []map[string]any // resolved filament presets, in project filament order
	WorkDir   string           // empty dir for this job
	// KeepPlacement: the models are already placed (the slicer page):
	// Orca must not arrange them.
	KeepPlacement bool `json:",omitempty"`
	// LevelGcode, when set, is appended to the machine start G-code so the bed
	// is leveled as part of this print (after the profile's own homing).
	LevelGcode string `json:",omitempty"`
}

type Result struct {
	GCode     string // path to plain G-code, if produced
	Sliced3MF string // path to sliced 3MF, if produced
	Log       string // tail of Orca's output
	// Notes: things the person should know about how this was sliced (for
	// example that the walls were made the Classic way, see Slice).
	Notes []string `json:",omitempty"`
	// silentFail: OrcaSlicer stopped with "slicing failed" and said nothing.
	silentFail bool
}

type Orca struct {
	Path string
	Args []string // template, see config.DefaultSliceArgs
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Args expands the template for a request. Exposed for tests and for the UI's
// "show command" button.
func (o *Orca) expand(req Request, machine, process string, fils []string, outdir, out3mf string) []string {
	rep := strings.NewReplacer(
		"{plate}", strconv.Itoa(req.Plate),
		"{settings}", machine+";"+process,
		"{machine}", machine,
		"{process}", process,
		"{filaments}", strings.Join(fils, ";"),
		"{outdir}", outdir,
		// Orca writes --export-3mf inside --outputdir: give it the file name only.
		"{out3mf}", filepath.Base(out3mf),
	)
	var args []string
	for i := 0; i < len(o.Args); i++ {
		a := o.Args[i]
		// Drop a flag whose value would be empty (e.g. no filaments).
		if i+1 < len(o.Args) && strings.HasPrefix(a, "--") && o.Args[i+1] == "{filaments}" && len(fils) == 0 {
			i++
			continue
		}
		if req.KeepPlacement && a == "--arrange" {
			if i+1 < len(o.Args) && !strings.HasPrefix(o.Args[i+1], "--") {
				i++
			}
			args = append(args, "--arrange", "0")
			continue
		}
		if a == "{input}" {
			args = append(args, req.Inputs...) // several STLs = several arguments
			continue
		}
		args = append(args, rep.Replace(a))
	}
	return args
}

// ClassicWallsNote is what the person is told when a plate was sliced with
// Classic walls because Arachne failed on it.
const ClassicWallsNote = "OrcaSlicer's Arachne wall maker failed on this plate, so it was sliced with Classic walls instead. Very thin tips and edges can come out slightly different."

// Slice runs OrcaSlicer on one plate.
//
// If it fails without saying why while the walls are set to Arachne, it is run
// once more with Classic walls. Found on a real part (the crest of a helmet,
// two sharp horn tips at the top): OrcaSlicer's command line stopped with its
// "slicing failed" code and no message for most layer heights, with supports on
// or off, wherever the part stood on the bed. Blunting the top 2 mm of the
// tips, or wall_generator = classic, made every one of those slices work, and
// nothing else did. The result says so in Notes.
func (o *Orca) Slice(ctx context.Context, req Request) (Result, error) {
	res, err := o.sliceOnce(ctx, req)
	if err == nil || ctx.Err() != nil || !res.silentFail || !usesArachne(req.Process) {
		return res, err
	}
	firstLog := res.Log
	req.Process["wall_generator"] = "classic"
	res2, err2 := o.sliceOnce(ctx, req)
	if err2 != nil {
		req.Process["wall_generator"] = "arachne"
		return res, err // Classic did not help: report the first failure
	}
	res2.Notes = append(res2.Notes, ClassicWallsNote)
	_ = os.WriteFile(filepath.Join(req.WorkDir, "orca-arachne-failed.log"), []byte(firstLog), 0o644)
	return res2, nil
}

// usesArachne: the walls of this process are made by Arachne.
func usesArachne(process map[string]any) bool {
	if process == nil {
		return false
	}
	v := process["wall_generator"]
	if l, ok := v.([]any); ok && len(l) > 0 {
		v = l[0]
	}
	return strings.TrimSpace(fmt.Sprint(v)) == "arachne"
}

func (o *Orca) sliceOnce(ctx context.Context, req Request) (Result, error) {
	var res Result
	if _, err := os.Stat(o.Path); err != nil {
		return res, fmt.Errorf("OrcaSlicer not found at %q: set orca_path in config.json", o.Path)
	}
	if err := os.MkdirAll(req.WorkDir, 0o755); err != nil {
		return res, err
	}
	collapseVariants(req.Machine, req.Process, req.Filaments)
	ensureLayerReset(req.Machine, req.Process)
	if strings.TrimSpace(req.LevelGcode) != "" {
		injectLeveling(req.Machine, req.LevelGcode)
	}
	machine := filepath.Join(req.WorkDir, "machine.json")
	process := filepath.Join(req.WorkDir, "process.json")
	if err := writeJSON(machine, req.Machine); err != nil {
		return res, err
	}
	if err := writeJSON(process, req.Process); err != nil {
		return res, err
	}
	var fils []string
	for i, f := range req.Filaments {
		p := filepath.Join(req.WorkDir, fmt.Sprintf("filament_%d.json", i+1))
		if err := writeJSON(p, f); err != nil {
			return res, err
		}
		fils = append(fils, p)
	}
	outdir := filepath.Join(req.WorkDir, "out")
	_ = os.RemoveAll(outdir) // a second try must not find the first one's files
	_ = os.MkdirAll(outdir, 0o755)
	out3mf := filepath.Join(outdir, fmt.Sprintf("plate_%d.gcode.3mf", req.Plate))

	cmd := exec.CommandContext(ctx, o.Path, o.expand(req, machine, process, fils, outdir, out3mf)...)
	cmd.Dir = req.WorkDir
	var buf tailBuffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	runErr := cmd.Run()
	res.Log = buf.String()
	_ = os.WriteFile(filepath.Join(req.WorkDir, "orca.log"), []byte(res.Log), 0o644)

	if msg := readResultJSON(outdir); msg != "" {
		return res, fmt.Errorf("Orca: %s", msg)
	}
	if ctx.Err() != nil {
		return res, fmt.Errorf("slicing stopped: %w", ctx.Err())
	}

	if _, err := os.Stat(out3mf); err == nil {
		res.Sliced3MF = out3mf
	}
	// Plain G-code: either written by Orca directly or embedded in the 3MF.
	if g := findGCode(outdir); g != "" {
		res.GCode = g
	} else if res.Sliced3MF != "" {
		g := filepath.Join(outdir, fmt.Sprintf("plate_%d.gcode", req.Plate))
		if err := extractGCode(res.Sliced3MF, req.Plate, g); err == nil {
			res.GCode = g
		}
	}
	if res.GCode == "" && res.Sliced3MF == "" {
		if runErr != nil {
			code, coded := exitCode(runErr)
			res.silentFail = coded && code == -100 && lastSaid(res.Log) == ""
			return res, errors.New(explainFailure(runErr, res.Log))
		}
		return res, fmt.Errorf("Orca finished but produced no output")
	}
	return res, nil
}

// readResultJSON reads Orca's result.json (if any) and returns an error
// message when it reports a failure.
func readResultJSON(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		return ""
	}
	var r struct {
		ReturnCode  int    `json:"return_code"`
		ErrorString string `json:"error_string"`
	}
	if json.Unmarshal(b, &r) != nil || r.ReturnCode == 0 {
		return ""
	}
	if r.ErrorString == "" {
		r.ErrorString = fmt.Sprintf("return code %d", r.ReturnCode)
	}
	return r.ErrorString
}

func findGCode(dir string) string {
	m, _ := filepath.Glob(filepath.Join(dir, "*.gcode"))
	if len(m) > 0 {
		return m[0]
	}
	return ""
}

func extractGCode(src string, plate int, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	want := fmt.Sprintf("Metadata/plate_%d.gcode", plate)
	var fallback *zip.File
	for _, f := range zr.File {
		n := strings.TrimPrefix(f.Name, "/")
		if n == want {
			fallback = f
			break
		}
		if strings.HasPrefix(n, "Metadata/") && strings.HasSuffix(n, ".gcode") && fallback == nil {
			fallback = f
		}
	}
	if fallback == nil {
		return fmt.Errorf("no gcode in %s", src)
	}
	rc, err := fallback.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

// tailBuffer keeps the last 64 KB of output.
type tailBuffer struct{ b bytes.Buffer }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b.Write(p)
	if t.b.Len() > 64<<10 {
		keep := t.b.Bytes()[t.b.Len()-(48<<10):]
		nb := append([]byte(nil), keep...)
		t.b.Reset()
		t.b.Write(nb)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return t.b.String() }

// ensureLayerReset: with relative extrusion (Orca's default) Orca's command
// line refuses to slice unless the layer change G-code resets the extruder
// ("G92 E0"). The app adds this check only for non-Bambu printers, but on the
// command line a Bambu printer's preset is not recognised as one, so add the
// reset when it is missing (harmless with relative extrusion).
func ensureLayerReset(machine, process map[string]any) {
	rel := true
	for _, m := range []map[string]any{machine, process} {
		if v, ok := m["use_relative_e_distances"]; ok {
			s := strings.TrimSpace(fmt.Sprint(v))
			if l, ok := v.([]any); ok && len(l) > 0 {
				s = fmt.Sprint(l[0])
			}
			rel = s == "1" || strings.EqualFold(s, "true")
		}
	}
	if !rel {
		return
	}
	str := func(v any) string {
		if l, ok := v.([]any); ok && len(l) > 0 {
			return fmt.Sprint(l[0])
		}
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
	if strings.Contains(str(machine["layer_change_gcode"]), "G92 E0") || strings.Contains(str(machine["before_layer_change_gcode"]), "G92 E0") {
		return
	}
	cur := str(machine["layer_change_gcode"])
	if cur != "" && !strings.HasSuffix(cur, "\n") {
		cur += "\n"
	}
	machine["layer_change_gcode"] = cur + "G92 E0"
}

// injectLeveling appends the bed-leveling command to the machine start G-code,
// so it runs after the profile's own start sequence (which homes and heats)
// but before the print's first move. The command is already active for the
// print, so the mesh it builds is used. Firmware-specific commands come from
// the printer's LevelGcode / the driver default; this just places it.
func injectLeveling(machine map[string]any, cmd string) {
	one := func(v any) string {
		if l, ok := v.([]any); ok && len(l) > 0 {
			return fmt.Sprint(l[0])
		}
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
	cur := one(machine["machine_start_gcode"])
	if strings.Contains(cur, strings.TrimSpace(cmd)) {
		return // already there (e.g. the profile does its own mesh)
	}
	if cur != "" && !strings.HasSuffix(cur, "\n") {
		cur += "\n"
	}
	machine["machine_start_gcode"] = cur + "; PrintHarbor: level before print\n" + strings.TrimSpace(cmd) + "\n"
}
