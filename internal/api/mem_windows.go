//go:build windows

package api

import (
	"syscall"
	"unsafe"
)

// totalMemoryMB is this computer's RAM (the slicer page uses it to warn
// about plates too big to slice).
func totalMemoryMB() int {
	type memStatusEx struct {
		Length, MemoryLoad                     uint32
		TotalPhys, AvailPhys, TotalPageFile    uint64
		AvailPageFile, TotalVirtual, AvailVirt uint64
		AvailExtendedVirtual                   uint64
	}
	var m memStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	if r, _, _ := proc.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 16384
	}
	return int(m.TotalPhys >> 20)
}
