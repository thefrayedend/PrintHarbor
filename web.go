// Package printharbor embeds the web UI so the whole app ships as one binary.
package printharbor

import (
	"embed"
	"io/fs"
)

//go:embed web
var webFiles embed.FS

// Web returns the static UI files.
func Web() fs.FS {
	sub, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	return sub
}
