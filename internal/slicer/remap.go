package slicer

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Toolchanger / multi-extruder remapping.
//
// Orca prints project filament N with tool T(N-1). When PrintHarbor maps a
// project color to a different tool (say filament 1 → tool 3 because that's
// where the red spool is), the sliced G-code's tool numbers are rewritten:
// standalone "Tn" lines and the T parameter of temperature/fan commands.
// Experimental: only used for Klipper multi-tool printers.

var (
	reToolLine  = regexp.MustCompile(`^(\s*)T(\d+)(\s*(;.*)?)$`)
	reToolParam = regexp.MustCompile(`^(\s*(?:M104|M109|M106|M107|M221|M218|M563|M568)\b[^;]*?\bT)(\d+)`)
)

// ToolMap converts a slot map (element i = 1-based slot for project
// filament i+1, 0 = default) into old→new tool numbers. Identity entries
// are dropped; nil means nothing to do.
func ToolMap(slotMap []int) map[int]int {
	m := map[int]int{}
	used := map[int]bool{}
	for i, s := range slotMap {
		if s > 0 {
			if used[s-1] {
				return nil // two colors on one tool can't be remapped
			}
			used[s-1] = true
			if s-1 != i {
				m[i] = s - 1
			}
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// RemapTools rewrites tool numbers in a G-code file in place.
func RemapTools(path string, tools map[int]int) (int, error) {
	if len(tools) == 0 {
		return 0, nil
	}
	in, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	tmp := path + ".remap"
	out, err := os.Create(tmp)
	if err != nil {
		in.Close()
		return 0, err
	}
	n, err := remapStream(in, out, tools)
	in.Close()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return n, os.Rename(tmp, path)
}

func remapStream(r io.Reader, w io.Writer, tools map[int]int) (int, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	bw := bufio.NewWriterSize(w, 1<<20)
	changed := 0
	swap := func(num string) string {
		t, _ := strconv.Atoi(num)
		if nt, ok := tools[t]; ok {
			changed++
			return strconv.Itoa(nt)
		}
		return num
	}
	for sc.Scan() {
		line := sc.Text()
		if strings.IndexByte(line, 'T') >= 0 {
			if m := reToolLine.FindStringSubmatch(line); m != nil {
				line = m[1] + "T" + swap(m[2]) + m[3]
			} else if m := reToolParam.FindStringSubmatchIndex(line); m != nil {
				line = line[:m[3]] + swap(line[m[4]:m[5]]) + line[m[5]:]
			}
		}
		if _, err := fmt.Fprintln(bw, line); err != nil {
			return changed, err
		}
	}
	if err := sc.Err(); err != nil {
		return changed, err
	}
	return changed, bw.Flush()
}
