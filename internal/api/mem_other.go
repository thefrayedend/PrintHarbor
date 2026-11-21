//go:build !windows

package api

import (
	"os"
	"strconv"
	"strings"
)

func totalMemoryMB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Fields(l); len(f) >= 2 && f[0] == "MemTotal:" {
				if kb, err := strconv.Atoi(f[1]); err == nil {
					return kb / 1024
				}
			}
		}
	}
	return 16384
}
