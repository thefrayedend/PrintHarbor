package printers

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// mqttClient is a minimal MQTT 3.1.1 client: QoS 0 publish/subscribe over
// TLS, enough for Bambu Lab's LAN broker. One connection, one reader.
type mqttClient struct {
	conn   net.Conn
	wmu    sync.Mutex
	nextID uint16
}

func mqttString(s string) []byte {
	b := make([]byte, 2+len(s))
	binary.BigEndian.PutUint16(b, uint16(len(s)))
	copy(b[2:], s)
	return b
}

func mqttRemaining(n int) []byte {
	var out []byte
	for {
		d := byte(n % 128)
		n /= 128
		if n > 0 {
			d |= 0x80
		}
		out = append(out, d)
		if n == 0 {
			return out
		}
	}
}

func (c *mqttClient) send(typ byte, body []byte) error {
	pkt := append([]byte{typ}, mqttRemaining(len(body))...)
	pkt = append(pkt, body...)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.conn.Write(pkt)
	return err
}

// readPacket returns the packet type byte and its body.
func mqttRead(r *bufio.Reader) (byte, []byte, error) {
	typ, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n, mult := 0, 1
	for i := 0; i < 4; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		n += int(b&0x7f) * mult
		if b&0x80 == 0 {
			break
		}
		mult *= 128
	}
	if n > 8<<20 {
		return 0, nil, fmt.Errorf("mqtt packet too large")
	}
	body := make([]byte, n)
	_, err = io.ReadFull(r, body)
	return typ, body, err
}

// mqttDial connects, authenticates and subscribes. Messages arrive on onMsg
// until the connection drops (the returned channel closes with the error).
func mqttDial(addr, clientID, user, pass string, tlsCfg *tls.Config, subs []string, onMsg func(topic string, payload []byte)) (*mqttClient, <-chan error, error) {
	d := &net.Dialer{Timeout: 8 * time.Second}
	var conn net.Conn
	var err error
	if tlsCfg != nil {
		conn, err = tls.DialWithDialer(d, "tcp", addr, tlsCfg)
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return nil, nil, err
	}
	c := &mqttClient{conn: conn, nextID: 1}
	var body []byte
	body = append(body, mqttString("MQTT")...)
	flags := byte(0x02) // clean session
	if user != "" {
		flags |= 0x80
	}
	if pass != "" {
		flags |= 0x40
	}
	body = append(body, 4, flags, 0, 60) // level 4, keepalive 60 s
	body = append(body, mqttString(clientID)...)
	if user != "" {
		body = append(body, mqttString(user)...)
	}
	if pass != "" {
		body = append(body, mqttString(pass)...)
	}
	if err := c.send(0x10, body); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReaderSize(conn, 64<<10)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	typ, ack, err := mqttRead(br)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if typ>>4 != 2 || len(ack) < 2 || ack[1] != 0 {
		conn.Close()
		code := byte(255)
		if len(ack) >= 2 {
			code = ack[1]
		}
		if code == 4 || code == 5 {
			return nil, nil, fmt.Errorf("printer refused the access code")
		}
		return nil, nil, fmt.Errorf("mqtt connect refused (code %d)", code)
	}
	for _, s := range subs {
		var sb []byte
		sb = binary.BigEndian.AppendUint16(sb, c.nextID)
		c.nextID++
		sb = append(sb, mqttString(s)...)
		sb = append(sb, 0)
		if err := c.send(0x82, sb); err != nil {
			conn.Close()
			return nil, nil, err
		}
	}
	done := make(chan error, 1)
	go func() {
		ping := time.NewTicker(30 * time.Second)
		defer ping.Stop()
		stop := make(chan struct{})
		go func() {
			for {
				select {
				case <-stop:
					return
				case <-ping.C:
					_ = c.send(0xC0, nil)
				}
			}
		}()
		var rerr error
		for {
			_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
			typ, body, err := mqttRead(br)
			if err != nil {
				rerr = err
				break
			}
			if typ>>4 == 3 && len(body) >= 2 { // PUBLISH
				tl := int(binary.BigEndian.Uint16(body))
				if 2+tl > len(body) {
					continue
				}
				topic := string(body[2 : 2+tl])
				payload := body[2+tl:]
				if (typ>>1)&3 > 0 && len(payload) >= 2 { // QoS 1/2: skip packet id
					payload = payload[2:]
				}
				onMsg(topic, payload)
			}
		}
		close(stop)
		conn.Close()
		done <- rerr
		close(done)
	}()
	return c, done, nil
}

func (c *mqttClient) Publish(topic string, payload []byte) error {
	body := append(mqttString(topic), payload...)
	return c.send(0x30, body)
}

func (c *mqttClient) Close() error {
	_ = c.send(0xE0, nil)
	return c.conn.Close()
}
