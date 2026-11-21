// Package tray puts PrintHarbor in the Windows notification area (system
// tray) so it runs without a console window. On other systems it does
// nothing: there PrintHarbor runs as a normal service or terminal program.
package tray

// Menu describes the tray icon and what its menu items do.
type Menu struct {
	Tooltip      string
	Balloon      string // shown once when the icon appears ("" = none)
	OnOpen       func()
	OnRestart    func()
	OnLog        func()
	OnQuit       func()
	AutoStart    func() bool // nil hides "Start with Windows"
	SetAutoStart func(bool) error
}
