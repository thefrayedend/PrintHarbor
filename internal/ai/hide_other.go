//go:build !windows

package ai

import "os/exec"

func hideWindow(c *exec.Cmd) {}
