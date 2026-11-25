package slicer

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Why OrcaSlicer failed, in plain words.
//
// When a slice fails all OrcaSlicer's command line gives back is a number
// (shown raw it read "exit status 0xffffff9c") and whatever it printed. This
// turns the two into something a person can act on: what went wrong, and what
// to try. The numbers are OrcaSlicer's own (its CLI_… codes); Windows reports
// them as large unsigned numbers, so they are read as signed here.

var orcaCodes = map[int]string{
	-3:   "a file it needed was not found",
	-5:   "it could not read one of the profiles",
	-14:  "it ran out of memory. Try a thicker layer height, fewer or simpler supports, or fewer parts on the plate",
	-17:  "this quality profile does not fit this printer",
	-50:  "nothing on this plate fits on the printer's bed",
	-51:  "the settings do not work together",
	-52:  "part of a model sticks out past the edge of the bed. Move it fully onto the bed",
	-100: "the slicing itself failed",
	-101: "the print would collide with itself (parts or the prime tower overlap)",
	-102: "some of the print would be outside the area this printer can reach",
	-103: "one of the filaments can't be printed on this bed plate type (see the bed type setting)",
	// 0xC0000005 on Windows: OrcaSlicer itself crashed (read memory it shouldn't).
	-1073741819: "OrcaSlicer crashed while reading the settings or the model. Try another quality profile, or another layout of the parts; if every plate does this on one printer, its profile is the cause",
}

// orcaKnown are messages OrcaSlicer prints for faults we know what to do about.
var orcaKnown = []struct{ has, say string }{
	{"std::bad_alloc", "OrcaSlicer ran out of memory. Try a thicker layer height, fewer or simpler supports, or fewer parts on the plate."},
}

// exitCode is the program's exit code as OrcaSlicer meant it (signed).
func exitCode(err error) (int, bool) {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 0, false
	}
	c := int(int32(uint32(ee.ExitCode())))
	if c > 127 && c < 256 { // Linux and macOS keep only the low byte: -100 arrives as 156
		c -= 256
	}
	return c, true
}

// lastSaid is the last thing OrcaSlicer printed that says something: its
// closing "found error, exit" is left out, and so is "Not precalculated
// Placeable areas", which its tree supports print by the dozen on slices that
// work as well as on ones that fail (it was once taken for the cause here; it
// is not).
func lastSaid(log string) string {
	lines := strings.Split(strings.ReplaceAll(log, "\r", ""), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" || strings.Contains(l, "found error, exit") || strings.Contains(l, "Not precalculated Placeable areas") {
			continue
		}
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(l, "Error:"), "error:"))
		if len(l) > 240 {
			l = l[:240] + "…"
		}
		return l
	}
	return ""
}

// explainFailure says why a slice failed, from the way OrcaSlicer ended and
// what it printed.
func explainFailure(runErr error, log string) string {
	for _, k := range orcaKnown {
		if strings.Contains(log, k.has) {
			return k.say
		}
	}
	msg := "OrcaSlicer could not slice this plate"
	code, coded := exitCode(runErr)
	switch {
	case coded && orcaCodes[code] != "":
		msg += ": " + orcaCodes[code] + "."
	case coded:
		msg += fmt.Sprintf(" (it stopped with code %d).", code)
	case runErr != nil:
		msg += fmt.Sprintf(" (%v).", runErr)
	default:
		msg += "."
	}
	if said := lastSaid(log); said != "" {
		msg += " OrcaSlicer said: " + said
	} else if coded && code == -100 {
		msg += " OrcaSlicer gave no reason. Things that have got such a plate through: a slightly different layer height or first layer height, or laying the part another way up."
	}
	return msg
}
