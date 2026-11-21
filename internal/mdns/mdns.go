// Package mdns answers multicast DNS questions for "<name>.local" with this
// computer's IPv4 addresses, so phones and laptops can open
// http://harbor.local:7621 without knowing the IP. Answer-only: it never
// browses or registers anything else.
package mdns

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
)

var group = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// Run answers queries until ctx is cancelled.
func Run(ctx context.Context, name string) error {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), ".local")) + ".local"
	conn, err := net.ListenMulticastUDP("udp4", nil, group)
	if err != nil {
		return fmt.Errorf("mdns: %w", err)
	}
	go func() { <-ctx.Done(); conn.Close() }()
	buf := make([]byte, 9000)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		unicast, ok := Wants(buf[:n], host)
		if !ok {
			continue
		}
		resp := Answer(host, LocalIPv4())
		if len(resp) == 0 {
			continue
		}
		if unicast || src.Port != 5353 {
			_, _ = conn.WriteToUDP(resp, src)
		} else {
			_, _ = conn.WriteToUDP(resp, group)
		}
	}
}

// Wants reports whether a DNS message is a query for host (A or ANY), and
// whether the asker requested a unicast reply.
func Wants(msg []byte, host string) (unicast, ok bool) {
	if len(msg) < 12 || msg[2]&0x80 != 0 { // responses aren't questions
		return false, false
	}
	qd := int(binary.BigEndian.Uint16(msg[4:]))
	off := 12
	for i := 0; i < qd; i++ {
		name, next, err := readName(msg, off)
		if err != nil || next+4 > len(msg) {
			return false, false
		}
		qtype := binary.BigEndian.Uint16(msg[next:])
		qclass := binary.BigEndian.Uint16(msg[next+2:])
		off = next + 4
		if strings.EqualFold(name, host) && (qtype == 1 || qtype == 255) {
			return qclass&0x8000 != 0, true
		}
	}
	return false, false
}

func readName(msg []byte, off int) (string, int, error) {
	var labels []string
	end := -1
	for hops := 0; hops < 32; hops++ {
		if off >= len(msg) {
			return "", 0, fmt.Errorf("short name")
		}
		l := int(msg[off])
		switch {
		case l == 0:
			if end < 0 {
				end = off + 1
			}
			return strings.Join(labels, "."), end, nil
		case l&0xC0 == 0xC0: // compression pointer
			if off+1 >= len(msg) {
				return "", 0, fmt.Errorf("bad pointer")
			}
			if end < 0 {
				end = off + 2
			}
			off = int(binary.BigEndian.Uint16(msg[off:]) & 0x3FFF)
		default:
			if off+1+l > len(msg) {
				return "", 0, fmt.Errorf("bad label")
			}
			labels = append(labels, string(msg[off+1:off+1+l]))
			off += 1 + l
		}
	}
	return "", 0, fmt.Errorf("name loop")
}

// Answer builds an authoritative mDNS response with one A record per IP.
func Answer(host string, ips []net.IP) []byte {
	if len(ips) == 0 {
		return nil
	}
	msg := []byte{0, 0, 0x84, 0, 0, 0, 0, byte(len(ips)), 0, 0, 0, 0}
	var name []byte
	for _, l := range strings.Split(host, ".") {
		name = append(name, byte(len(l)))
		name = append(name, l...)
	}
	name = append(name, 0)
	for _, ip := range ips {
		msg = append(msg, name...)
		msg = append(msg, 0, 1, 0x80, 1) // type A, class IN + cache-flush
		msg = binary.BigEndian.AppendUint32(msg, 120)
		msg = append(msg, 0, 4)
		msg = append(msg, ip.To4()...)
	}
	return msg
}

// LocalIPv4 lists this machine's non-loopback IPv4 addresses.
func LocalIPv4() []net.IP {
	var out []net.IP
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() {
				out = append(out, ipn.IP.To4())
			}
		}
	}
	return out
}
