package mdns

import (
	"encoding/binary"
	"net"
	"testing"
)

func query(name string, qtype uint16, qu bool) []byte {
	msg := []byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, l := range []string{name, "local"} {
		msg = append(msg, byte(len(l)))
		msg = append(msg, l...)
	}
	msg = append(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, qtype)
	class := uint16(1)
	if qu {
		class |= 0x8000
	}
	return binary.BigEndian.AppendUint16(msg, class)
}

func TestWantsAndAnswer(t *testing.T) {
	if u, ok := Wants(query("Harbor", 1, true), "harbor.local"); !ok || !u {
		t.Fatal("A query with QU bit should match, unicast")
	}
	if _, ok := Wants(query("harbor", 28, false), "harbor.local"); ok {
		t.Fatal("AAAA query should not match")
	}
	if _, ok := Wants(query("other", 1, false), "harbor.local"); ok {
		t.Fatal("other name should not match")
	}
	resp := Answer("harbor.local", []net.IP{net.IPv4(192, 168, 1, 5)})
	if resp[2] != 0x84 || resp[7] != 1 {
		t.Fatalf("header % x", resp[:12])
	}
	ip := resp[len(resp)-4:]
	if !net.IP(ip).Equal(net.IPv4(192, 168, 1, 5)) {
		t.Fatal(ip)
	}
	name, _, err := readName(resp, 12)
	if err != nil || name != "harbor.local" {
		t.Fatal(name, err)
	}
	if Wants(resp, "harbor.local"); false {
		t.Fatal()
	}
	if _, ok := Wants(resp, "harbor.local"); ok {
		t.Fatal("responses must not be treated as questions")
	}
}

// The answer must name only the address on the asker's own network: a PC with
// a virtual adapter must not send a phone to that adapter's address.
func TestAnswerForPicksTheAskersNetwork(t *testing.T) {
	mk := func(cidr string) *net.IPNet {
		ip, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		return &net.IPNet{IP: ip.To4(), Mask: n.Mask}
	}
	lan, virt := mk("192.168.1.40/24"), mk("172.28.80.1/20")
	all := []net.IP{virt.IP, lan.IP}

	ips, mine := answerFor(net.ParseIP("192.168.1.77"), []*net.IPNet{lan, virt}, all)
	if !mine || len(ips) != 1 || !ips[0].Equal(lan.IP) {
		t.Fatalf("phone on the LAN got %v (mine=%v), want only %v", ips, mine, lan.IP)
	}
	ips, mine = answerFor(net.ParseIP("172.28.81.9"), []*net.IPNet{lan, virt}, all)
	if !mine || len(ips) != 1 || !ips[0].Equal(virt.IP) {
		t.Fatalf("asker on the virtual network got %v, want only %v", ips, virt.IP)
	}
	ips, mine = answerFor(net.ParseIP("10.9.9.9"), []*net.IPNet{lan, virt}, all)
	if mine || len(ips) != 2 {
		t.Fatalf("asker on no known network got %v (mine=%v), want every address", ips, mine)
	}
}
