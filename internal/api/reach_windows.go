//go:build windows

package api

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd
}

func readFirewall(exe string) Reach {
	prof, err := hidden(exec.Command("netsh", "advfirewall", "show", "currentprofile")).Output()
	if err != nil {
		return Reach{State: "unknown", Why: "Windows Firewall could not be read."}
	}
	profile, on, allowIn, ok := parseFwProfile(string(prof))
	if !ok {
		return Reach{State: "unknown", Why: "Windows Firewall's answer was not understood."}
	}
	// netsh exits with 1 when no rule matches; the text is still what we need.
	rules, _ := hidden(exec.Command("netsh", "advfirewall", "firewall", "show", "rule", "name=all", "dir=in", "verbose")).Output()
	return fwVerdict(parseFwRules(string(rules), exe), profile, on, allowIn)
}

// allowInFirewall replaces whatever rules exist for this program with one that
// allows it on Private networks and on the kind of network the PC is on now.
// Changing the firewall needs administrator rights, so Windows shows its
// permission prompt (UAC); if that is refused nothing changes.
func allowInFirewall(exe, profile string) error {
	if strings.ContainsAny(exe, "\"%\r\n") {
		return fmt.Errorf("the program's path has characters that can't be put in a firewall rule: %s", exe)
	}
	profiles := "private"
	if p := strings.ToLower(profile); p == "public" || p == "domain" {
		profiles += "," + p
	}
	script := filepath.Join(os.TempDir(), "printharbor-firewall.cmd")
	body := "@echo off\r\n" +
		"netsh advfirewall firewall delete rule name=all program=\"" + exe + "\"\r\n" +
		"netsh advfirewall firewall add rule name=\"PrintHarbor\" dir=in action=allow program=\"" + exe + "\" enable=yes profile=" + profiles + "\r\n"
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		return err
	}
	defer os.Remove(script)
	ps := "Start-Process -FilePath '" + strings.ReplaceAll(script, "'", "''") + "' -Verb RunAs -Wait -WindowStyle Hidden"
	if out, err := hidden(exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)).CombinedOutput(); err != nil {
		return fmt.Errorf("Windows did not make the change (the permission prompt was refused, or: %s)", strings.TrimSpace(string(out)))
	}
	return nil
}
