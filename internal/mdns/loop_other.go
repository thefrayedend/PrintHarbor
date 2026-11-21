//go:build !windows && !linux

package mdns

import "net"

// loopback is only set up on Windows and Linux; elsewhere the name still works
// for other devices, which is what it is for.
func loopback(c *net.UDPConn) {}
