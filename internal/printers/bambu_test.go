package printers

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func selfSigned(t *testing.T) *tls.Config {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "printer"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

// fakeBambu runs a tiny MQTT broker, FTPS server and camera like a P1S.
type fakeBambu struct {
	t                          *testing.T
	mu                         sync.Mutex
	requests                   []map[string]any
	uploaded                   map[string][]byte
	mqttPort, ftpPort, camPort int
}

func startFakeBambu(t *testing.T, code string) *fakeBambu {
	f := &fakeBambu{t: t, uploaded: map[string][]byte{}}
	cfg := selfSigned(t)
	listen := func(handle func(net.Conn)) int {
		l, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go handle(c)
			}
		}()
		return l.Addr().(*net.TCPAddr).Port
	}
	f.mqttPort = listen(func(c net.Conn) { f.mqtt(c, code) })
	f.ftpPort = listen(func(c net.Conn) { f.ftp(c, code, cfg) })
	f.camPort = listen(func(c net.Conn) {
		defer c.Close()
		auth := make([]byte, 80)
		io.ReadFull(c, auth)
		if !bytes.HasPrefix(auth[48:], []byte(code)) {
			return
		}
		jpg := []byte{0xFF, 0xD8, 'j', 'p', 'g', 0xFF, 0xD9}
		hdr := make([]byte, 16)
		binary.LittleEndian.PutUint32(hdr, uint32(len(jpg)))
		c.Write(append(hdr, jpg...))
	})
	return f
}

func (f *fakeBambu) mqtt(c net.Conn, code string) {
	defer c.Close()
	br := bufio.NewReader(c)
	typ, body, err := mqttRead(br)
	if err != nil || typ != 0x10 {
		return
	}
	if !bytes.Contains(body, []byte(code)) {
		c.Write([]byte{0x20, 2, 0, 5}) // not authorized
		return
	}
	c.Write([]byte{0x20, 2, 0, 0})
	pub := func(v any) {
		j, _ := json.Marshal(v)
		b := append(mqttString("device/SER123/report"), j...)
		pkt := append([]byte{0x30}, mqttRemaining(len(b))...)
		c.Write(append(pkt, b...))
	}
	for {
		typ, body, err := mqttRead(br)
		if err != nil {
			return
		}
		switch typ >> 4 {
		case 8: // SUBSCRIBE
			c.Write([]byte{0x90, 3, body[0], body[1], 0})
		case 3: // PUBLISH
			tl := int(binary.BigEndian.Uint16(body))
			var req map[string]any
			json.Unmarshal(body[2+tl:], &req)
			f.mu.Lock()
			f.requests = append(f.requests, req)
			f.mu.Unlock()
			if p, ok := req["pushing"].(map[string]any); ok && p["command"] == "pushall" {
				pub(map[string]any{"print": map[string]any{"gcode_state": "RUNNING", "mc_percent": 40, "mc_remaining_time": 30,
					"nozzle_temper": 220.5, "nozzle_target_temper": 220, "bed_temper": 55, "bed_target_temper": 55, "subtask_name": "Benchy",
					"ams": map[string]any{"ams": []any{map[string]any{"id": "0", "tray": []any{
						map[string]any{"id": "0", "tray_type": "PLA", "tray_color": "000000FF"},
						map[string]any{"id": "1", "tray_type": ""},
						map[string]any{"id": "2", "tray_type": "PETG", "tray_color": "FF0000FF"}}}}},
					"lights_report": []any{map[string]any{"node": "chamber_light", "mode": "on"}}}})
				// Incremental update (P1 style): only progress changes.
				pub(map[string]any{"print": map[string]any{"mc_percent": 41}})
			}
		case 12: // PINGREQ
			c.Write([]byte{0xD0, 0})
		}
	}
}

func (f *fakeBambu) ftp(c net.Conn, code string, cfg *tls.Config) {
	defer c.Close()
	w := func(s string) { fmt.Fprintf(c, "%s\r\n", s) }
	br := bufio.NewReader(c)
	w("220 Bambu FTP")
	var data net.Listener
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		cmd, arg, _ := strings.Cut(line, " ")
		switch cmd {
		case "USER":
			w("331 pass")
		case "PASS":
			if arg != code {
				w("530 Login incorrect.")
				return
			}
			w("230 ok")
		case "PBSZ", "PROT", "TYPE":
			w("200 ok")
		case "PASV":
			data, _ = tls.Listen("tcp", "127.0.0.1:0", cfg)
			p := data.Addr().(*net.TCPAddr).Port
			w(fmt.Sprintf("227 Entering Passive Mode (0,0,0,0,%d,%d)", p/256, p%256))
		case "STOR":
			w("150 go")
			dc, err := data.Accept()
			if err != nil {
				return
			}
			b, _ := io.ReadAll(dc)
			dc.Close()
			data.Close()
			f.mu.Lock()
			f.uploaded[arg] = b
			f.mu.Unlock()
			w("226 done")
		case "QUIT":
			w("221 bye")
			return
		}
	}
}

func sliced3MF() []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("Metadata/plate_2.gcode")
	w.Write([]byte("G28"))
	zw.Close()
	return buf.Bytes()
}

func TestBambu(t *testing.T) {
	f := startFakeBambu(t, "12345678")
	b, err := NewBambu("127.0.0.1", "SER123", "12345678")
	if err != nil {
		t.Fatal(err)
	}
	b.mqttPort, b.ftpPort, b.camPort = f.mqttPort, f.ftpPort, f.camPort
	ctx := context.Background()
	var st Status
	for i := 0; i < 100; i++ {
		st, _ = b.Status(ctx)
		if st.Progress == 0.41 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.State != StatePrinting || st.Progress != 0.41 || st.RemainingSec != 1800 || st.JobName != "Benchy" || st.Light != "on" {
		t.Fatalf("status %+v", st)
	}
	// empty trays are listed too (shown as "Empty", like the other printers)
	if len(st.Slots) != 3 || st.Slots[0].Index != 1 || st.Slots[0].Color != "#000000" || st.Slots[1].Type != "" || st.Slots[2].Index != 3 || st.Slots[2].Type != "PETG" {
		t.Fatalf("slots %+v", st.Slots)
	}
	if err := b.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	// Upload + start with project filament 1 → slot 3, filament 2 → slot 1.
	if err := b.Upload(WithSlotMap(ctx, []int{3, 1}), "Robot_plate2.gcode.3mf", bytes.NewReader(sliced3MF()), true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	f.mu.Lock()
	up := f.uploaded["Robot_plate2.gcode.3mf"]
	var start map[string]any
	paused := false
	for _, r := range f.requests {
		if p, ok := r["print"].(map[string]any); ok {
			if p["command"] == "project_file" {
				start = p
			}
			paused = paused || p["command"] == "pause"
		}
	}
	f.mu.Unlock()
	if len(up) == 0 || !paused {
		t.Fatalf("upload %d bytes, paused %v", len(up), paused)
	}
	if start == nil || start["param"] != "Metadata/plate_2.gcode" || start["use_ams"] != true || fmt.Sprint(start["ams_mapping"]) != "[2 0]" {
		t.Fatalf("start command %+v", start)
	}
	img, ct, err := b.Snapshot(ctx)
	if err != nil || ct != "image/jpeg" || !bytes.HasPrefix(img, []byte{0xFF, 0xD8}) {
		t.Fatal("camera", err)
	}
	// Wrong access code is reported clearly.
	bad, _ := NewBambu("127.0.0.1", "SER123", "wrong")
	bad.mqttPort = f.mqttPort
	bad.Status(ctx)
	time.Sleep(200 * time.Millisecond)
	if _, err := bad.Status(ctx); err == nil || !strings.Contains(err.Error(), "access code") {
		t.Fatalf("want access code error, got %v", err)
	}
	_ = strconv.Itoa
}
