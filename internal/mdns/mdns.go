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
	"sync"
	"time"
)

var group = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// Run answers queries until ctx is cancelled.
//
// It listens on every network adapter, not just the one the system would pick
// by default: on a PC with more than one adapter (Wi-Fi and Ethernet, or the
// virtual adapters that WSL, Hyper-V, VirtualBox and VPNs add) the default is
// often not the one the phone is on, and the questions were never heard.
//
// Each answer carries only the addresses on the asker's own network. Answering
// with every adapter's address let a phone try an address it cannot reach (a
// virtual adapter's) and give up.
//
// Answers are also made audible on this computer itself, so harbor.local works
// in a browser on the PC that runs PrintHarbor (Go switches that off when it
// joins the group; see loopback).
func Run(ctx context.Context, name string) error {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), ".local")) + ".local"
	var (
		mu    sync.Mutex
		open  = map[int]*net.UDPConn{} // adapter index -> its listener
		first error
	)
	listen := func(ifc *net.Interface) {
		idx := 0
		if ifc != nil {
			idx = ifc.Index
		}
		mu.Lock()
		_, have := open[idx]
		mu.Unlock()
		if have {
			return
		}
		conn, err := net.ListenMulticastUDP("udp4", ifc, group)
		if err != nil {
			if first == nil {
				first = err
			}
			return
		}
		loopback(conn)
		mu.Lock()
		open[idx] = conn
		mu.Unlock()
		go func() {
			serve(conn, ifc, host)
			conn.Close()
			mu.Lock()
			delete(open, idx)
			mu.Unlock()
		}()
	}
	scan := func() {
		for _, ifc := range adapters() {
			ifc := ifc
			listen(&ifc)
		}
	}
	scan()
	mu.Lock()
	n := len(open)
	mu.Unlock()
	if n == 0 {
		listen(nil) // no adapter could be joined by name: let the system choose one
		mu.Lock()
		n = len(open)
		mu.Unlock()
		if n == 0 {
			return fmt.Errorf("mdns: %w", first)
		}
	}
	// Adapters come and go (Wi-Fi reconnects, a cable is plugged in): look again
	// now and then and start listening on new ones.
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			mu.Lock()
			for _, c := range open {
				c.Close()
			}
			mu.Unlock()
			return nil
		case <-t.C:
			scan()
		}
	}
}

// adapters lists the network adapters worth listening on: up, able to do
// multicast, not loopback, and holding an IPv4 address.
func adapters() []net.Interface {
	var out []net.Interface
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagMulticast == 0 {
			continue
		}
		if len(ipv4Nets(&ifc)) > 0 {
			out = append(out, ifc)
		}
	}
	return out
}

// ipv4Nets is an adapter's IPv4 addresses with their network masks.
func ipv4Nets(ifc *net.Interface) []*net.IPNet {
	var out []*net.IPNet
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() {
			out = append(out, &net.IPNet{IP: ipn.IP.To4(), Mask: ipn.Mask})
		}
	}
	return out
}

// answerFor picks what to tell an asker at src: this computer's addresses on
// the asker's own network. mine is true when one of them matched. When the
// asker is on none of our networks (unusual: mDNS relayed by a router), every
// address is given, as before.
func answerFor(src net.IP, nets []*net.IPNet, all []net.IP) (ips []net.IP, mine bool) {
	for _, n := range nets {
		if n.Contains(src) {
			ips = append(ips, n.IP)
		}
	}
	if len(ips) > 0 {
		return ips, true
	}
	return all, false
}

// serve answers the questions heard by one listener until it is closed. Every
// listener can hear a question that arrived on another adapter, so only the
// listener for the asker's network answers it (the others stay quiet); a
// question from outside all our networks is answered by whichever hears it.
func serve(conn *net.UDPConn, ifc *net.Interface, host string) {
	buf := make([]byte, 9000)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		unicast, ok := Wants(buf[:n], host)
		if !ok {
			continue
		}
		var nets, everyNet []*net.IPNet
		if ifc != nil {
			nets = ipv4Nets(ifc)
		}
		for _, a := range adapters() {
			a := a
			everyNet = append(everyNet, ipv4Nets(&a)...)
		}
		ips, mine := answerFor(src.IP, nets, LocalIPv4())
		if !mine && ifc != nil {
			if _, other := answerFor(src.IP, everyNet, nil); other {
				continue // the asker is on another adapter's network: that listener answers
			}
		}
		resp := Answer(host, ips)
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
