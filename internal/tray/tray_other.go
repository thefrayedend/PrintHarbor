//go:build !windows

package tray

// Run does nothing outside Windows.
func Run(m Menu) (stop func()) { return func() {} }

// Alert prints nothing outside Windows; the log has the message.
func Alert(title, msg string) {}

// AutoStart support is Windows only.
func AutoStartSupported() bool                     { return false }
func AutoStartEnabled(name string) bool            { return false }
func SetAutoStart(name, cmd string, on bool) error { return nil }
