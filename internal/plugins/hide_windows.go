//go:build windows

package plugins

import (
	"os/exec"
	"syscall"
)

// hidePyWindow keeps the Python process from flashing a console window.
func hidePyWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
