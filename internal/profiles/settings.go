package profiles

// Setting describes one Orca setting for the web UI: a friendly label, a
// group, a control type and (for choices) the allowed values. The Key is the
// exact Orca JSON key, so nothing is translated on the way to the slicer.
//
// Only the settings people change often are listed. Everything else is still
// reachable through the "All settings" search, which edits raw keys.
type Setting struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Group   string   `json:"group"`
	Kind    string   `json:"kind"` // number | percent | bool | choice | text
	Unit    string   `json:"unit,omitempty"`
	Choices []Choice `json:"choices,omitempty"`
	Help    string   `json:"help,omitempty"`
	Simple  bool     `json:"simple"` // shown on the simple screen
	For     string   `json:"for"`    // process | filament
}

type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func ch(pairs ...string) []Choice {
	var out []Choice
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Choice{pairs[i], pairs[i+1]})
	}
	return out
}

// Catalog is the curated list of settings shown in the friendly editor.
var Catalog = []Setting{
	// Quality
	{Key: "layer_height", Label: "Layer height", Group: "Quality", Kind: "number", Unit: "mm", Simple: true, For: "process",
		Help: "Thinner layers look smoother but take longer."},
	{Key: "initial_layer_print_height", Label: "First layer height", Group: "Quality", Kind: "number", Unit: "mm", For: "process"},
	{Key: "seam_position", Label: "Seam position", Group: "Quality", Kind: "choice", For: "process",
		Choices: ch("aligned", "Aligned", "nearest", "Nearest", "back", "Back", "random", "Random")},
	{Key: "ironing_type", Label: "Ironing", Group: "Quality", Kind: "choice", For: "process",
		Choices: ch("no ironing", "Off", "top", "Top surfaces", "topmost", "Topmost surface only", "solid", "All solid layers")},

	// Strength
	{Key: "wall_loops", Label: "Walls", Group: "Strength", Kind: "number", Simple: true, For: "process",
		Help: "Number of perimeter loops. More walls = stronger parts."},
	{Key: "top_shell_layers", Label: "Top layers", Group: "Strength", Kind: "number", For: "process"},
	{Key: "bottom_shell_layers", Label: "Bottom layers", Group: "Strength", Kind: "number", For: "process"},
	{Key: "sparse_infill_density", Label: "Infill", Group: "Strength", Kind: "percent", Unit: "%", Simple: true, For: "process"},
	{Key: "sparse_infill_pattern", Label: "Infill pattern", Group: "Strength", Kind: "choice", Simple: true, For: "process",
		Choices: ch("grid", "Grid", "gyroid", "Gyroid", "cubic", "Cubic", "adaptivecubic", "Adaptive cubic",
			"honeycomb", "Honeycomb", "3dhoneycomb", "3D honeycomb", "triangles", "Triangles", "tri-hexagon", "Tri-hexagon",
			"lightning", "Lightning", "crosshatch", "Cross hatch", "rectilinear", "Rectilinear", "line", "Line", "zig-zag", "Zig zag")},

	// Support
	{Key: "enable_support", Label: "Supports", Group: "Support", Kind: "bool", Simple: true, For: "process"},
	{Key: "support_type", Label: "Support type", Group: "Support", Kind: "choice", Simple: true, For: "process",
		Choices: ch("tree(auto)", "Tree (auto)", "normal(auto)", "Normal (auto)", "tree(manual)", "Tree (painted only)", "normal(manual)", "Normal (painted only)")},
	{Key: "support_style", Label: "Support style", Group: "Support", Kind: "choice", For: "process",
		Help:    "How the supports are built. Default is Grid for normal supports and Organic for tree supports.",
		Choices: ch("default", "Default (Grid / Organic)", "grid", "Grid", "snug", "Snug", "organic", "Organic", "tree_slim", "Tree Slim", "tree_strong", "Tree Strong", "tree_hybrid", "Tree Hybrid")},
	{Key: "support_on_build_plate_only", Label: "Supports on build plate only", Group: "Support", Kind: "bool", For: "process"},
	{Key: "support_threshold_angle", Label: "Overhang threshold", Group: "Support", Kind: "number", Unit: "°", For: "process"},

	// Adhesion
	{Key: "brim_type", Label: "Brim", Group: "Adhesion", Kind: "choice", Simple: true, For: "process",
		Choices: ch("auto_brim", "Auto", "no_brim", "None", "outer_only", "Outer only", "inner_only", "Inner only", "outer_and_inner", "Outer and inner", "brim_ears", "Mouse ears")},
	{Key: "brim_width", Label: "Brim width", Group: "Adhesion", Kind: "number", Unit: "mm", For: "process"},
	{Key: "skirt_loops", Label: "Skirt loops", Group: "Adhesion", Kind: "number", For: "process"},

	// Speed
	{Key: "outer_wall_speed", Label: "Outer wall speed", Group: "Speed", Kind: "number", Unit: "mm/s", For: "process"},
	{Key: "inner_wall_speed", Label: "Inner wall speed", Group: "Speed", Kind: "number", Unit: "mm/s", For: "process"},
	{Key: "sparse_infill_speed", Label: "Infill speed", Group: "Speed", Kind: "number", Unit: "mm/s", For: "process"},
	{Key: "travel_speed", Label: "Travel speed", Group: "Speed", Kind: "number", Unit: "mm/s", For: "process"},

	// Special modes
	{Key: "spiral_mode", Label: "Vase mode", Group: "Special", Kind: "bool", For: "process"},
	{Key: "print_sequence", Label: "Print sequence", Group: "Special", Kind: "choice", For: "process",
		Choices: ch("by layer", "All objects together", "by object", "One object at a time")},

	// Filament
	{Key: "nozzle_temperature", Label: "Nozzle temperature", Group: "Filament", Kind: "number", Unit: "°C", Simple: true, For: "filament"},
	{Key: "nozzle_temperature_initial_layer", Label: "First layer nozzle temperature", Group: "Filament", Kind: "number", Unit: "°C", For: "filament"},
	{Key: "hot_plate_temp", Label: "Bed temperature (smooth plate)", Group: "Filament", Kind: "number", Unit: "°C", For: "filament"},
	{Key: "textured_plate_temp", Label: "Bed temperature (textured plate)", Group: "Filament", Kind: "number", Unit: "°C", For: "filament"},
	{Key: "fan_max_speed", Label: "Max part cooling fan", Group: "Filament", Kind: "number", Unit: "%", For: "filament"},
	{Key: "filament_flow_ratio", Label: "Flow ratio", Group: "Filament", Kind: "number", For: "filament",
		Help: "How much plastic is pushed out. 1 is normal; 0.95 to 1.05 is the usual range."},
	{Key: "filament_max_volumetric_speed", Label: "Fastest flow", Group: "Filament", Kind: "number", Unit: "mm³/s", For: "filament",
		Help: "The most plastic the nozzle can melt per second. Lower it if fast prints come out thin or rough."},
	{Key: "fan_min_speed", Label: "Min part cooling fan", Group: "Filament", Kind: "number", Unit: "%", For: "filament"},
	{Key: "filament_retraction_length", Label: "Retraction length", Group: "Filament", Kind: "number", Unit: "mm", For: "filament",
		Help: "How far the filament is pulled back between moves. More helps against strings."},
	{Key: "enable_pressure_advance", Label: "Use pressure advance", Group: "Filament", Kind: "bool", For: "filament"},
	{Key: "pressure_advance", Label: "Pressure advance", Group: "Filament", Kind: "number", For: "filament"},
	{Key: "filament_cost", Label: "Price per kg", Group: "Filament", Kind: "number", For: "filament"},
}

// ApplyOverrides writes string values into a resolved preset, keeping Orca's
// shape: if the existing value is a list (filament settings usually are), the
// override is applied to every element.
func ApplyOverrides(resolved map[string]any, overrides map[string]string) {
	for k, v := range overrides {
		switch cur := resolved[k].(type) {
		case []any:
			n := len(cur)
			if n == 0 {
				n = 1
			}
			list := make([]any, n)
			for i := range list {
				list[i] = v
			}
			resolved[k] = list
		default:
			resolved[k] = v
		}
	}
}
