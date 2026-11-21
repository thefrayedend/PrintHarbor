//go:build !windows

package api

import "fmt"

// makeShortcut is Windows-only; elsewhere PrintHarbor is opened by URL.
func (a *App) makeShortcut(engine string) (string, error) {
	_ = engine
	return "", fmt.Errorf("desktop shortcuts are only set up on Windows; open http://localhost:%s in your browser and bookmark it", listenPort(a.cfg().Listen))
}
