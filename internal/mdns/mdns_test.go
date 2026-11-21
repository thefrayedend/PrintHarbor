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
