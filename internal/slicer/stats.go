package slicer

import (
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var (
	reTime   = regexp.MustCompile(`(?i);\s*(?:estimated printing time \(normal mode\)|total estimated time|estimated printing time)\s*[=:]\s*([0-9dhms ]+)`)
	reGrams  = regexp.MustCompile(`(?i);\s*total filament used \[g\]\s*=\s*([0-9.]+)`)
	reGrams2 = regexp.MustCompile(`(?im)^;\s*filament used \[g\]\s*=\s*([0-9., ]+)`)
)

// GCodeStats reads Orca's summary comments (print time, filament weight)
// from the start and end of a G-code file.
func GCodeStats(path string) (seconds int, grams float64) {
	seconds, grams, _ = GCodeUsage(path)
	return
}

// GCodeUsage is GCodeStats plus the weight used of EACH filament, in the
// order the filaments were given to the slicer ("; filament used [g] = 3.2,
// 0.0, 1.5"). perFilament is nil when the G-code doesn't list them.
func GCodeUsage(path string) (seconds int, grams float64, perFilament []float64) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, nil
	}
	defer f.Close()
	fi, _ := f.Stat()
	const chunk = 128 << 10
	var text strings.Builder
	head := make([]byte, chunk)
	n, _ := io.ReadFull(f, head)
	text.Write(head[:n])
	if fi.Size() > chunk {
		tail := make([]byte, chunk)
		off := fi.Size() - chunk
		if off < int64(n) {
			off = int64(n)
		}
		m, _ := f.ReadAt(tail, off)
		text.Write(tail[:m])
	}
	s := text.String()
	if m := reTime.FindStringSubmatch(s); m != nil {
		seconds = parseDuration(m[1])
	}
	sum := 0.0
	if m := reGrams2.FindStringSubmatch(s); m != nil {
		for _, p := range strings.Split(m[1], ",") {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			v, _ := strconv.ParseFloat(p, 64)
			perFilament = append(perFilament, v)
			sum += v
		}
	}
	if m := reGrams.FindStringSubmatch(s); m != nil {
		grams, _ = strconv.ParseFloat(m[1], 64)
	} else {
		grams = sum
	}
	return
}

// parseDuration reads "1d 2h 3m 4s" style durations.
func parseDuration(s string) int {
	total, num := 0, 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			num = num*10 + int(r-'0')
		case r == 'd':
			total += num * 86400
			num = 0
		case r == 'h':
			total += num * 3600
			num = 0
		case r == 'm':
			total += num * 60
			num = 0
		case r == 's':
			total += num
			num = 0
		}
	}
	return total
}
