package slicer

import (
	"fmt"
	"strings"
)

// Newer OrcaSlicer profiles describe every nozzle option a printer can have.
// Some settings then come as lists with one value per (extruder, nozzle kind),
// e.g. the Snapmaker U1 machine profile:
//
//	printer_extruder_id      = 1, 1, 2, 2, 3, 3, 4, 4
//	printer_extruder_variant = Direct Drive Standard, Direct Drive High Flow, …
//	retraction_length        = 1.5, 1.5, …   (8 values for 4 extruders)
//
// OrcaSlicer 2.5.1's command line on Windows crashes at once (exit code
// -1073741819, 0xC0000005) on every plate when such a profile maps extruders
// beyond the second one: found on the Snapmaker U1, and the Prusa CORE One
// INDX 4T stock profile does the same, while the two-extruder Bambu H2D and
// H2C profiles slice. The same files slice on Linux. Ruled out one group at a
// time on the real U1 slice: only printer_extruder_id set it off, and keeping
// it with one entry per extruder ("1,2,3,4") still crashed.
//
// For those printers PrintHarbor does the nozzle choice itself before Orca
// sees the profiles, as Orca's window does: per extruder it keeps the kind the
// profile has as its default (default_nozzle_volume_type, e.g. "Standard"),
// shortens every per-kind list to those values, and leaves out the lists that
// describe the kinds (Orca then takes extruder 1's entry for each extruder,
// which holds that extruder's own value once the lists are one per
// extruder). On the U1 that sliced on Windows, and on Linux the G-code came
// out the same as from the untouched profile.
//
// Printers with one or two extruders, and profiles without nozzle kinds, are
// left exactly as they are.
func collapseVariants(machine, process map[string]any, filaments []map[string]any) {
	ids, vars := strList(machine["printer_extruder_id"]), strList(machine["printer_extruder_variant"])
	if len(ids) == 0 || len(ids) != len(vars) || distinct(ids) <= 2 {
		return
	}
	pick := pickVariants(ids, vars, strList(machine["default_nozzle_volume_type"]))
	chosen := make([]string, len(pick))
	for i, p := range pick {
		chosen[i] = vars[p]
	}
	keepIndexes(machine, printerVariantKeys, len(ids), pick)
	delete(machine, "printer_extruder_id")
	delete(machine, "printer_extruder_variant")
	delete(machine, "extruder_variant_list")

	if process != nil {
		pids, pvars := strList(process["print_extruder_id"]), strList(process["print_extruder_variant"])
		if len(pids) > 0 && len(pids) == len(pvars) {
			// The same choice: extruder n gets the kind chosen for it above.
			var ppick []int
			for e := range chosen {
				id, at := fmt.Sprint(e+1), -1
				for i := range pids {
					if pids[i] == id && (at < 0 || (pvars[i] == chosen[e] && pvars[at] != chosen[e])) {
						at = i
					}
				}
				if at >= 0 {
					ppick = append(ppick, at)
				}
			}
			if len(ppick) == len(chosen) {
				keepIndexes(process, printVariantKeys, len(pids), ppick)
				delete(process, "print_extruder_id")
				delete(process, "print_extruder_variant")
			}
		}
	}
	for _, f := range filaments {
		fvars := strList(f["filament_extruder_variant"])
		if len(fvars) == 0 {
			continue
		}
		at := 0
		for i, v := range fvars {
			if v == chosen[0] {
				at = i
				break
			}
		}
		keepIndexes(f, filamentVariantKeys, len(fvars), []int{at})
		delete(f, "filament_extruder_variant")
	}
}

func distinct(l []string) int {
	seen := map[string]bool{}
	for _, s := range l {
		seen[s] = true
	}
	return len(seen)
}

// pickVariants chooses, for each extruder in order, the index of the entry
// with the wanted nozzle kind (want[extruder], e.g. "Standard" or "High
// Flow"); without one, the extruder's first entry.
func pickVariants(ids, vars, want []string) []int {
	var order []string
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	pick := make([]int, 0, len(order))
	for e, id := range order {
		w := ""
		if e < len(want) {
			w = want[e]
		} else if len(want) > 0 {
			w = want[0]
		}
		first, match := -1, -1
		for i := range ids {
			if ids[i] != id {
				continue
			}
			if first < 0 {
				first = i
			}
			if match < 0 && w != "" && strings.HasSuffix(vars[i], w) {
				match = i
			}
		}
		if match < 0 {
			match = first
		}
		pick = append(pick, match)
	}
	return pick
}

// keepIndexes shortens the listed settings that have one value per nozzle
// kind (exactly n values) to the values at pick.
func keepIndexes(m map[string]any, keys map[string]bool, n int, pick []int) {
	for k, v := range m {
		l, ok := v.([]any)
		if !ok || len(l) != n || !keys[k] {
			continue
		}
		out := make([]any, len(pick))
		for i, p := range pick {
			out[i] = l[p]
		}
		m[k] = out
	}
}

func strList(v any) []string {
	l, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(l))
	for i, x := range l {
		out[i] = fmt.Sprint(x)
	}
	return out
}
