package slicer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/store"
)

// fakeOrca is a shell script standing in for orca-slicer: it records its
// arguments and writes a sliced 3MF containing Metadata/plate_N.gcode.
const fakeOrca = `#!/bin/sh
echo "$@" > "$PWD/args.txt"
out=""; plate=""
while [ $# -gt 0 ]; do
  case "$1" in
    --export-3mf) out="$2"; shift ;;
    --outputdir) outdir="$2"; shift ;;
    --slice) plate="$2"; shift ;;
  esac
  shift
done
mkdir -p tmp3mf/Metadata
echo "G28 ; plate $plate" > tmp3mf/Metadata/plate_$plate.gcode
cd tmp3mf && python3 -c "import zipfile,sys;z=zipfile.ZipFile(sys.argv[1],'w');z.write('Metadata/plate_'+sys.argv[2]+'.gcode');z.close()" "$outdir/$out" "$plate"
echo "slicing done"
`

func TestOrcaRunner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "orca-slicer")
	os.WriteFile(bin, []byte(fakeOrca), 0o755)
	o := &Orca{Path: bin, Args: config.DefaultSliceArgs}
	work := filepath.Join(dir, "job")
	res, err := o.Slice(context.Background(), Request{
		Inputs: []string{"/projects/My Project.3mf"}, Plate: 3,
		Machine: map[string]any{"name": "m"}, Process: map[string]any{"name": "p"},
		Filaments: []map[string]any{{"name": "f1"}, {"name": "f2"}}, WorkDir: work,
	})
	if err != nil {
		t.Fatal(err, res.Log)
	}
	g, _ := os.ReadFile(res.GCode)
	if !strings.Contains(string(g), "plate 3") {
		t.Fatalf("gcode %q", g)
	}
	args, _ := os.ReadFile(filepath.Join(work, "args.txt"))
	a := string(args)
	for _, want := range []string{"--slice 3", "machine.json;", "process.json", "filament_1.json;", "filament_2.json", "--arrange 1", "My Project.3mf"} {
		if !strings.Contains(a, want) {
			t.Errorf("args missing %q: %s", want, a)
		}
	}
}

func TestOrcaMissing(t *testing.T) {
	o := &Orca{Path: "/nope/orca", Args: config.DefaultSliceArgs}
	if _, err := o.Slice(context.Background(), Request{WorkDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "orca_path") {
		t.Fatal(err)
	}
}

type countingSlicer struct {
	cur, max int32
}

func (c *countingSlicer) Slice(ctx context.Context, req Request) (Result, error) {
	n := atomic.AddInt32(&c.cur, 1)
	for {
		m := atomic.LoadInt32(&c.max)
		if n <= m || atomic.CompareAndSwapInt32(&c.max, m, n) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	atomic.AddInt32(&c.cur, -1)
	return Result{GCode: "x.gcode"}, nil
}

func TestQueueRespectsConcurrency(t *testing.T) {
	for _, workers := range []int{1, 2} {
		s, _ := store.New(t.TempDir())
		cs := &countingSlicer{}
		var mu sync.Mutex
		sent := 0
		prep := func(j *Job, wd string) (Request, error) { return Request{WorkDir: wd}, nil }
		send := func(ctx context.Context, j *Job) error { mu.Lock(); sent++; mu.Unlock(); return nil }
		q, err := NewQueue(s, cs, prep, send, workers, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			if _, err := q.Add(Job{Machine: "m", Process: "p", Send: true}); err != nil {
				t.Fatal(err)
			}
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			done := 0
			for _, j := range q.List() {
				if j.State == Done {
					done++
				}
			}
			if done == 5 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if got := atomic.LoadInt32(&cs.max); got != int32(workers) {
			t.Fatalf("workers=%d: max concurrent slices %d", workers, got)
		}
		mu.Lock()
		if sent != 5 {
			t.Fatalf("sent %d", sent)
		}
		mu.Unlock()
	}
}

func TestGCodeStats(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.gcode")
	os.WriteFile(p, []byte("; HEADER\nG28\n"+strings.Repeat("G1 X1 Y1 E0.1\n", 20000)+
		"; filament used [mm] = 1234.5\n; total filament used [g] = 12.34\n; estimated printing time (normal mode) = 1h 2m 3s\n"), 0o644)
	sec, g := GCodeStats(p)
	if sec != 3723 || g != 12.34 {
		t.Fatal(sec, g)
	}
	if parseDuration("2d 1h 5s") != 2*86400+3605 {
		t.Fatal("duration")
	}
}

func TestToolRemap(t *testing.T) {
	if ToolMap([]int{1, 2}) != nil || ToolMap([]int{0, 0}) != nil {
		t.Fatal("identity/default maps should be nil")
	}
	if ToolMap([]int{2, 2}) != nil {
		t.Fatal("two colors on one tool can't be remapped")
	}
	m := ToolMap([]int{3, 1})
	if m[0] != 2 || m[1] != 0 {
		t.Fatal(m)
	}
	p := filepath.Join(t.TempDir(), "a.gcode")
	os.WriteFile(p, []byte("G28\nT0\nM104 S210 T0\nM109 T1 S215 ; wait\nT1 ; change\nG1 X10 ; T0 in a comment stays\nTIMELAPSE_TAKE_FRAME\n"), 0o644)
	n, err := RemapTools(p, m)
	if err != nil || n != 4 {
		t.Fatal(n, err)
	}
	b, _ := os.ReadFile(p)
	want := "G28\nT2\nM104 S210 T2\nM109 T0 S215 ; wait\nT0 ; change\nG1 X10 ; T0 in a comment stays\nTIMELAPSE_TAKE_FRAME\n"
	if string(b) != want {
		t.Fatalf("got:\n%s", b)
	}
}

func TestInjectLeveling(t *testing.T) {
	// String start g-code: command is appended, the original start is kept.
	m := map[string]any{"machine_start_gcode": "G28\nM104 S200"}
	injectLeveling(m, "BED_MESH_CALIBRATE")
	s := m["machine_start_gcode"].(string)
	if !strings.HasPrefix(s, "G28\nM104 S200") {
		t.Fatalf("start g-code clobbered: %q", s)
	}
	if !strings.Contains(s, "BED_MESH_CALIBRATE") {
		t.Fatalf("leveling command not added: %q", s)
	}
	// Idempotent: a profile that already has the command isn't doubled.
	injectLeveling(m, "BED_MESH_CALIBRATE")
	if strings.Count(m["machine_start_gcode"].(string), "BED_MESH_CALIBRATE") != 1 {
		t.Fatalf("command added twice: %q", m["machine_start_gcode"])
	}
	// Array form (Orca stores some fields as arrays) and an empty start.
	m2 := map[string]any{"machine_start_gcode": []any{"G28 ; home"}}
	injectLeveling(m2, "  G29  ")
	if g := m2["machine_start_gcode"].(string); !strings.Contains(g, "G28 ; home") || !strings.Contains(g, "G29") || strings.Contains(g, "  G29") {
		t.Fatalf("array-form inject wrong: %q", g)
	}
	m3 := map[string]any{}
	injectLeveling(m3, "G29")
	if g, _ := m3["machine_start_gcode"].(string); !strings.Contains(g, "G29") {
		t.Fatalf("empty-start inject wrong: %q", g)
	}
}

func TestGCodeUsagePerFilament(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct {
		body  string
		total float64
		each  []float64
	}{
		"three colours":      {"; filament used [mm] = 100, 20, 30\n; filament used [g] = 20.00, 4.50, 6.25\n; total filament used [g] = 30.75\n", 30.75, []float64{20, 4.5, 6.25}},
		"one colour":         {"; filament used [g] = 12.34\n; total filament used [g] = 12.34\n", 12.34, []float64{12.34}},
		"no total line":      {"; filament used [g] = 3.0, 0.00, 1.5\n", 4.5, []float64{3, 0, 1.5}},
		"total only":         {"; total filament used [g] = 9.9\n", 9.9, nil},
		"not the total line": {"; total filament used [g] = 9.9\n; filament used [g] = 9.9\n", 9.9, []float64{9.9}},
		"nothing":            {"G28\n", 0, nil},
	} {
		p := filepath.Join(dir, "x.gcode")
		os.WriteFile(p, []byte("G28\n"+c.body+"; estimated printing time (normal mode) = 5m 0s\n"), 0o644)
		sec, total, each := GCodeUsage(p)
		if name != "nothing" && sec != 300 {
			t.Errorf("%s: %d s", name, sec)
		}
		if total < c.total-0.001 || total > c.total+0.001 || len(each) != len(c.each) {
			t.Errorf("%s: total %v, each %v; want %v, %v", name, total, each, c.total, c.each)
			continue
		}
		for i := range each {
			if each[i] != c.each[i] {
				t.Errorf("%s: each %v, want %v", name, each, c.each)
			}
		}
		if s2, g2 := GCodeStats(p); s2 != sec || g2 != total {
			t.Errorf("%s: GCodeStats disagrees", name)
		}
	}
}

// A failed slice must say what went wrong and what to try, not "exit status
// 0xffffff9c".
func TestExplainFailure(t *testing.T) {
	exit := func(code int) error {
		err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
		if err == nil {
			t.Fatal("the test program did not fail")
		}
		return err
	}
	treeLog := strings.Repeat("Error: Not precalculated Placeable areas requested, radius 0, layer 0, critical: 0\n", 29) + "Slic3r::CLI::run found error, exit\n"
	for name, c := range map[string]struct {
		err  error
		log  string
		want []string
		not  []string
	}{
		"no reason given (the tree support lines are noise)": {exit(156), treeLog, []string{"the slicing itself failed", "gave no reason"}, []string{"exit status", "0x", "Not precalculated", "tree supports failed", "Tree Hybrid"}},
		"out of memory":               {exit(242), "", []string{"ran out of memory", "thicker layer height"}, []string{"exit status"}},
		"sticks out past the bed":     {exit(204), "some detail\n", []string{"past the edge of the bed", "OrcaSlicer said: some detail"}, nil},
		"a code we have no words for": {exit(199), "Error: something odd\nSlic3r::CLI::run found error, exit\n", []string{"code -57", "OrcaSlicer said: something odd"}, []string{"found error"}},
		"could not even start":        {errors.New("file not found"), "", []string{"could not slice this plate", "file not found"}, nil},
	} {
		got := explainFailure(c.err, c.log)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q does not say %q", name, got, w)
			}
		}
		for _, w := range c.not {
			if strings.Contains(got, w) {
				t.Errorf("%s: %q still says %q", name, got, w)
			}
		}
	}
}

// A slice that fails without a word while the walls are Arachne is run again
// with Classic walls, and the result says so. A failure that gives a reason,
// or one with Classic walls already, is reported as it is.
func TestArachneFallsBackToClassic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the pretend slicer is a shell script")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "orca.sh")
	// Fails like OrcaSlicer (code -100) when process.json says arachne; with
	// SAY set it also gives a reason. Otherwise writes a G-code file.
	script := `#!/bin/sh
out=""; settings=""
while [ $# -gt 0 ]; do
  case "$1" in --outputdir) out="$2"; shift;; --load-settings) settings="$2"; shift;; esac
  shift
done
process="${settings#*;}"
echo run >> "` + dir + `/runs"
if grep -q '"wall_generator": "arachne"' "$process"; then
  if [ -f "` + dir + `/say" ]; then echo "Error: crest: One object has an empty first layer"; fi
  echo "Error: Not precalculated Placeable areas requested, radius 0, layer 0, critical: 0"
  echo "Slic3r::CLI::run found error, exit"
  exit 156
fi
if [ -f "` + dir + `/allfail" ]; then echo "Slic3r::CLI::run found error, exit"; exit 156; fi
echo "; classic" > "$out/plate_1.gcode"
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	o := &Orca{Path: fake, Args: []string{"--slice", "{plate}", "--load-settings", "{settings}", "--outputdir", "{outdir}", "{input}"}}
	runs := func() int { b, _ := os.ReadFile(filepath.Join(dir, "runs")); return strings.Count(string(b), "run") }
	slice := func(gen string) (Result, error) {
		_ = os.Remove(filepath.Join(dir, "runs"))
		return o.Slice(context.Background(), Request{Inputs: []string{"x.3mf"}, Plate: 1, Machine: map[string]any{}, Process: map[string]any{"wall_generator": gen}, WorkDir: filepath.Join(dir, "work")})
	}

	res, err := slice("arachne")
	if err != nil || res.GCode == "" || runs() != 2 {
		t.Fatalf("arachne failing silently: err=%v gcode=%q runs=%d, want a second run that works", err, res.GCode, runs())
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "Classic walls") {
		t.Fatalf("notes = %q, want the Classic walls note", res.Notes)
	}
	if _, err := os.Stat(filepath.Join(dir, "work", "orca-arachne-failed.log")); err != nil {
		t.Error("the first run's log was not kept")
	}

	if res, err = slice("classic"); err != nil || runs() != 1 || len(res.Notes) != 0 {
		t.Fatalf("classic: err=%v runs=%d notes=%q, want one clean run", err, runs(), res.Notes)
	}

	_ = os.WriteFile(filepath.Join(dir, "say"), nil, 0o644)
	if _, err = slice("arachne"); err == nil || runs() != 1 || !strings.Contains(err.Error(), "empty first layer") {
		t.Fatalf("a failure with a reason: err=%v runs=%d, want it reported and no second run", err, runs())
	}
	_ = os.Remove(filepath.Join(dir, "say"))

	_ = os.WriteFile(filepath.Join(dir, "allfail"), nil, 0o644)
	if _, err = slice("arachne"); err == nil || runs() != 2 || !strings.Contains(err.Error(), "gave no reason") {
		t.Fatalf("classic failing too: err=%v runs=%d, want the first failure reported", err, runs())
	}
}
