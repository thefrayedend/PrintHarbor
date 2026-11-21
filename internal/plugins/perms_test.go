package plugins

import (
	"strings"
	"testing"
)

func TestEffectivePermissions(t *testing.T) {
	cases := []struct {
		name string
		man  Manifest
		want string // comma-joined, in display order
	}{
		{"declared network", Manifest{Permissions: []string{"network"}}, "network"},
		{"printer kind infers printers", Manifest{Kind: "printer"}, "printers"},
		{"on_event infers events", Manifest{Functions: []string{"run", "on_event"}}, "events"},
		{"snapshot infers camera", Manifest{Kind: "printer", Functions: []string{"snapshot"}}, "printers,camera"},
		{"needs_key infers settings", Manifest{NeedsKey: true}, "settings"},
		{"deduped + ordered", Manifest{Kind: "printer", Permissions: []string{"NETWORK", "printers"}, Functions: []string{"on_event", "snapshot"}},
			"printers,camera,network,events"},
		{"none = local only", Manifest{Kind: "tool", Functions: []string{"run"}}, ""},
		{"unknown declared kept", Manifest{Permissions: []string{"network", "quantum"}}, "network,quantum"},
	}
	for _, c := range cases {
		got := strings.Join(effectivePermissions(c.man), ",")
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
