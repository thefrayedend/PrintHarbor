//go:build !windows

package api

import "fmt"

func readFirewall(exe string) Reach { return Reach{State: "unknown"} }

func allowInFirewall(exe, profile string) error {
	return fmt.Errorf("PrintHarbor only does this on Windows; allow its port in this computer's firewall yourself")
}
