package mdns

import (
	"context"
	"net"
	"testing"
	"time"
)

// A resolver on the same computer (how Windows itself looks up harbor.local):
// it asks from port 5353 and listens on the multicast group for the answer.
func TestSameHostResolverGetsAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, "harbortest")
	time.Sleep(300 * time.Millisecond)

	rc, err := net.ListenMulticastUDP("udp4", nil, group)
	if err != nil {
		t.Skip("no multicast here:", err)
	}
	defer rc.Close()
	loopback(rc) // a real resolver hears its own group
	q := []byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, l := range []string{"harbortest", "local"} {
		q = append(q, byte(len(l)))
		q = append(q, l...)
	}
	q = append(q, 0, 0, 1, 0, 1) // A, IN, multicast reply wanted
	if _, err := rc.WriteToUDP(q, group); err != nil {
		t.Skip("cannot send multicast here:", err)
	}
	rc.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1500)
	for {
		n, _, err := rc.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("no answer for harbortest.local reached a resolver on the same computer: %v", err)
		}
		if n > 12 && buf[2]&0x80 != 0 { // a response
			return
		}
	}
}
