//go:build !windows

package plugins

import "os/exec"

func hidePyWindow(cmd *exec.Cmd) {}
