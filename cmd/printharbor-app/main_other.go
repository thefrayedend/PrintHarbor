//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// The desktop app window uses Windows WebView2; on other systems, open
// PrintHarbor in a browser.
func main() {
	dir := ""
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Dir(exe)
	}
	fmt.Printf("The PrintHarbor app window is Windows-only. Open http://localhost:%s in your browser.\n", loadAppConfig(dir, os.Args[1:]).Port)
}
