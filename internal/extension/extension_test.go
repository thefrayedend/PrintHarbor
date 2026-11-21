package extension

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteToPort(t *testing.T) {
	dir := t.TempDir()
	if err := WriteTo(dir, "7621"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "background.js"))
	if !strings.Contains(string(b), `const PH = ["http://localhost:7621", "http://127.0.0.1:7621"];`) {
		t.Fatal("background.js doesn't have the address line this package rewrites")
	}
	for _, f := range []string{"manifest.json", "content.js", "page.js", "bridge.js"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s wasn't written", f)
		}
	}
	// Another port: the extension reports there, and nothing else changes.
	if err := WriteTo(dir, "8123"); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(dir, "background.js"))
	if !strings.Contains(string(b2), `const PH = ["http://localhost:8123", "http://127.0.0.1:8123"];`) || strings.Contains(string(b2), "7621") {
		t.Error("background.js doesn't use the configured port")
	}
	if len(b2) != len(b) {
		t.Error("more than the port changed")
	}
	// Back again (the file is rewritten when it differs), and junk is ignored.
	for _, port := range []string{"7621", "", "80; evil()"} {
		if err := WriteTo(dir, port); err != nil {
			t.Fatal(err)
		}
		if b3, _ := os.ReadFile(filepath.Join(dir, "background.js")); string(b3) != string(b) {
			t.Errorf("port %q: background.js isn't the built-in one", port)
		}
	}
}
