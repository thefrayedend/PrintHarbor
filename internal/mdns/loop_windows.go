package mdns

import (
	"net"
	"syscall"
)

// loopback makes multicast packets this socket sends also reach listeners on
// this computer (see Run). Go turns this off when it joins a group.
func loopback(c *net.UDPConn) {
	rc, err := c.SyscallConn()
	if err != nil {
		return
	}
	_ = rc.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP, 1)
	})
}
