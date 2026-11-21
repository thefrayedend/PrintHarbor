package printers

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// wsConn is a minimal client websocket: text frames, no extensions.
type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
}

// wsDial opens ws(s)://host/path on base's host.
func wsDial(ctx context.Context, base *url.URL, path string) (*wsConn, error) {
	host := base.Host
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	if base.Scheme == "https" || base.Scheme == "wss" {
		tc := tls.Client(conn, &tls.Config{ServerName: base.Hostname()})
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tc
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	keyb := make([]byte, 16)
	_, _ = rand.Read(keyb)
	key := base64.StdEncoding.EncodeToString(keyb)
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, host, key)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("websocket upgrade failed: %s", resp.Status)
	}
	return &wsConn{conn: conn, br: br}, nil
}

func (w *wsConn) WriteText(b []byte) error { return wsWrite(w.conn, 0x1, b) }

// ReadText returns the next text message, answering pings and skipping
// other frames.
func (w *wsConn) ReadText() ([]byte, error) {
	for {
		op, payload, err := wsRead(w.br)
		if err != nil {
			return nil, err
		}
		switch op {
		case 0x8:
			return nil, fmt.Errorf("websocket closed by printer")
		case 0x9:
			_ = wsWrite(w.conn, 0xA, payload)
		case 0x1:
			return payload, nil
		}
	}
}

func (w *wsConn) SetDeadline(t time.Time) { _ = w.conn.SetDeadline(t) }

func (w *wsConn) Close() error {
	_ = wsWrite(w.conn, 0x8, nil)
	return w.conn.Close()
}

// errNoReply means the request was delivered but the printer never answered
// (older Snapmaker firmware accepts camera.start_monitor silently).
var errNoReply = fmt.Errorf("no reply")

// wsCall opens Moonraker's websocket, sends one JSON-RPC request and returns
// the matching response. Some vendor methods (like Snapmaker's
// camera.start_monitor) are only available over the websocket transport.
func wsCall(ctx context.Context, base *url.URL, method string, params any) (json.RawMessage, error) {
	const id = 4242
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params, "id": id})
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	w, err := wsDial(ctx, base, "/websocket")
	if err != nil {
		return nil, err
	}
	defer w.Close()
	if err := w.WriteText(req); err != nil {
		return nil, err
	}
	for {
		payload, err := w.ReadText()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return nil, errNoReply
			}
			return nil, err
		}
		var msg struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &msg) != nil || msg.ID != id {
			continue // notifications and other traffic
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	}
}

// wsWrite sends one masked frame (clients must mask).
func wsWrite(w io.Writer, op byte, payload []byte) error {
	hdr := []byte{0x80 | op}
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 65536:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	mask := make([]byte, 4)
	_, _ = rand.Read(mask)
	hdr = append(hdr, mask...)
	body := make([]byte, n)
	for i := range payload {
		body[i] = payload[i] ^ mask[i%4]
	}
	_, err := w.Write(append(hdr, body...))
	return err
}

// wsRead reads one frame, reassembling fragmented messages.
func wsRead(r *bufio.Reader) (byte, []byte, error) {
	var op byte
	var msg []byte
	for {
		h := make([]byte, 2)
		if _, err := io.ReadFull(r, h); err != nil {
			return 0, nil, err
		}
		fin := h[0]&0x80 != 0
		if o := h[0] & 0x0F; o != 0 {
			op = o
		}
		n := uint64(h[1] & 0x7F)
		switch n {
		case 126:
			b := make([]byte, 2)
			if _, err := io.ReadFull(r, b); err != nil {
				return 0, nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b))
		case 127:
			b := make([]byte, 8)
			if _, err := io.ReadFull(r, b); err != nil {
				return 0, nil, err
			}
			n = binary.BigEndian.Uint64(b)
		}
		if n > 16<<20 {
			return 0, nil, fmt.Errorf("websocket frame too large")
		}
		var mask []byte
		if h[1]&0x80 != 0 {
			mask = make([]byte, 4)
			if _, err := io.ReadFull(r, mask); err != nil {
				return 0, nil, err
			}
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(r, p); err != nil {
			return 0, nil, err
		}
		if mask != nil {
			for i := range p {
				p[i] ^= mask[i%4]
			}
		}
		msg = append(msg, p...)
		if fin {
			return op, msg, nil
		}
	}
}
