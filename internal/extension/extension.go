// Package extension is PrintHarbor's browser extension for Chrome and Edge:
// it lets your normal browser file model downloads into the library.
package extension

import (
	"bytes"
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed files
var files embed.FS

// Version of the extension (from its manifest).
const Version = "1.0.0"

// DefaultPort is the port written into the extension's source files.
const DefaultPort = "7621"

// WriteTo keeps the unpacked extension in dir up to date (Chrome loads it
// from there; after an update, Chrome's reload button picks it up). port is
// the port PrintHarbor listens on: the extension reports downloads to
// http://localhost:<port>, so it follows "listen" in config.json.
func WriteTo(dir, port string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return fs.WalkDir(files, "files", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := files.ReadFile(p)
		b = withPort(p, b, port)
		dst := filepath.Join(dir, filepath.FromSlash(p[len("files/"):]))
		if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, b) {
			return nil
		}
		return os.WriteFile(dst, b, 0o644)
	})
}

// withPort points background.js at PrintHarbor's port.
func withPort(name string, b []byte, port string) []byte {
	if port == "" || port == DefaultPort || !strings.HasSuffix(name, "background.js") {
		return b
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return b // not a port number: leave the file alone
		}
	}
	return bytes.ReplaceAll(b, []byte(":"+DefaultPort+`"`), []byte(":"+port+`"`))
}
