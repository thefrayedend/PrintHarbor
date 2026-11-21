package printers

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Elegoo printers that speak SDCP (Centauri Carbon and newer): a websocket
// on port 3030 for status/control, HTTP upload on the same port, camera on
// 3031. Address = printer IP. The mainboard ID is discovered automatically.
func init() {
	Register(DriverInfo{Name: "elegoo", Label: "Elegoo Centauri / SDCP", AddressHint: "192.168.1.80",
		Help: "For the Centauri Carbon and other SDCP printers. Elegoo's Klipper printers (Neptune 4) use the Klipper connection."},
		func(p Printer) (Driver, error) { return NewElegoo(p.Address, p.Serial) })
}

type Elegoo struct {
	host      string
	port      int
	discPort  int
	mainboard string
	client    *http.Client

	mu        sync.Mutex
	ws        *wsConn
	status    map[string]any
	connected bool
	lastErr   error
	started   bool
	wmu       sync.Mutex
}

func NewElegoo(addr, mainboard string) (*Elegoo, error) {
	h := strings.TrimSpace(addr)
	h = strings.TrimPrefix(strings.TrimPrefix(h, "http://"), "ws://")
	h = strings.TrimSuffix(h, "/")
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	if h == "" {
		return nil, fmt.Errorf("address is required")
	}
	return &Elegoo{host: h, port: 3030, discPort: 3000, mainboard: strings.TrimSpace(mainboard), client: &http.Client{Timeout: 10 * time.Minute}, status: map[string]any{}}, nil
}

func (e *Elegoo) Capabilities() Capabilities {
	return Capabilities{Status: true, Pause: true, Resume: true, Cancel: true, Upload: true, StartPrint: true, Temperatures: true, Camera: true, Light: true}
}

func (e *Elegoo) OutputFormat() string { return "gcode" }

// discover asks the printer for its mainboard ID over UDP (SDCP "M99999").
func (e *Elegoo) discover() (string, error) {
	conn, err := net.DialTimeout("udp", net.JoinHostPort(e.host, strconv.Itoa(e.discPort)), 3*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte("M99999")); err != nil {
		return "", err
	}
	buf := make([]byte, 8192)
	n, err := conn.Read(buf)
	if err != nil {
		return "", fmt.Errorf("printer didn't answer discovery: %w", err)
	}
	var r struct {
		Data struct {
			MainboardID string `json:"MainboardID"`
		} `json:"Data"`
	}
	if json.Unmarshal(buf[:n], &r) != nil || r.Data.MainboardID == "" {
		return "", fmt.Errorf("unexpected discovery reply")
	}
	return r.Data.MainboardID, nil
}

func (e *Elegoo) ensure() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.started {
		e.started = true
		go e.loop()
	}
}

func (e *Elegoo) loop() {
	backoff := time.Second
	for {
		if e.mainboard == "" {
			if id, err := e.discover(); err == nil {
				e.mu.Lock()
				e.mainboard = id
				e.mu.Unlock()
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		w, err := wsDial(ctx, &url.URL{Scheme: "ws", Host: net.JoinHostPort(e.host, strconv.Itoa(e.port))}, "/websocket")
		cancel()
		if err != nil {
			e.mu.Lock()
			e.connected, e.lastErr = false, err
			e.mu.Unlock()
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		w.SetDeadline(time.Time{})
		e.mu.Lock()
		e.ws, e.connected, e.lastErr = w, true, nil
		e.mu.Unlock()
		_ = e.request(0, map[string]any{}) // status
		stop := make(chan struct{})
		go func() { // the printer only pushes while we keep asking now and then
			t := time.NewTicker(20 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					_ = e.request(0, map[string]any{})
				}
			}
		}()
		for {
			msg, err := w.ReadText()
			if err != nil {
				break
			}
			var m struct {
				Topic  string         `json:"Topic"`
				Status map[string]any `json:"Status"`
				Data   struct {
					Data map[string]any `json:"Data"`
				} `json:"Data"`
			}
			if json.Unmarshal(msg, &m) != nil {
				continue
			}
			if m.Status != nil {
				e.mu.Lock()
				deepMerge(e.status, m.Status)
				e.mu.Unlock()
			}
		}
		close(stop)
		w.Close()
		e.mu.Lock()
		e.connected, e.ws = false, nil
		e.mu.Unlock()
	}
}

func newHexID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (e *Elegoo) request(cmd int, data map[string]any) error {
	e.mu.Lock()
	w, mb := e.ws, e.mainboard
	e.mu.Unlock()
	if w == nil {
		return fmt.Errorf("not connected to the printer")
	}
	msg := map[string]any{"Id": "", "Data": map[string]any{"Cmd": cmd, "Data": data, "RequestID": newHexID(), "MainboardID": mb,
		"TimeStamp": time.Now().Unix(), "From": 1}, "Topic": "sdcp/request/" + mb}
	b, _ := json.Marshal(msg)
	e.wmu.Lock()
	defer e.wmu.Unlock()
	return w.WriteText(b)
}

func (e *Elegoo) Status(ctx context.Context) (Status, error) {
	e.ensure()
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.connected && len(e.status) == 0 {
		if e.lastErr != nil {
			return Status{State: StateOffline}, e.lastErr
		}
		return Status{State: StateOffline, Message: "connecting…"}, nil
	}
	s := e.status
	st := Status{Nozzle: num64(s["TempOfNozzle"]), NozzleTarget: num64(s["TempTargetNozzle"]), Bed: num64(s["TempOfHotbed"]),
		BedTarget: num64(s["TempTargetHotbed"]), Chamber: num64(s["TempOfBox"]), SnapshotURL: "elegoo"}
	pi, _ := s["PrintInfo"].(map[string]any)
	if pi != nil {
		st.JobName = strv(pi["Filename"])
		if p := num64(pi["Progress"]); p > 0 {
			st.Progress = p / 100
		} else if tl := num64(pi["TotalLayer"]); tl > 0 {
			st.Progress = num64(pi["CurrentLayer"]) / tl
		}
		if tt := num64(pi["TotalTicks"]); tt > 0 {
			st.RemainingSec = int(tt - num64(pi["CurrentTicks"]))
		}
	}
	printing := false
	if cs, ok := s["CurrentStatus"].([]any); ok {
		for _, c := range cs {
			printing = printing || num64(c) == 1
		}
	}
	code := -1.0
	if pi != nil {
		code = num64(pi["Status"])
	}
	switch {
	case code == 5 || code == 6:
		st.State = StatePaused
	case code == 9:
		st.State = StateFinished
	case printing:
		st.State = StatePrinting
	default:
		st.State = StateIdle
	}
	if !e.connected {
		st.State, st.Message = StateOffline, "reconnecting…"
	}
	if ls, ok := s["LightStatus"].(map[string]any); ok {
		if num64(ls["SecondLight"]) > 0 {
			st.Light = "on"
		} else {
			st.Light = "off"
		}
	}
	return st, nil
}

func (e *Elegoo) Pause(ctx context.Context) error  { return e.request(129, map[string]any{}) }
func (e *Elegoo) Cancel(ctx context.Context) error { return e.request(130, map[string]any{}) }
func (e *Elegoo) Resume(ctx context.Context) error { return e.request(131, map[string]any{}) }

func (e *Elegoo) SetLight(ctx context.Context, on bool) error {
	v := 0
	if on {
		v = 1
	}
	return e.request(403, map[string]any{"LightStatus": map[string]any{"SecondLight": v, "RgbLight": []int{0, 0, 0}}})
}

func (e *Elegoo) Snapshot(ctx context.Context) ([]byte, string, error) {
	_ = e.request(386, map[string]any{"Enable": 1}) // make sure the video stream is on
	b, err := mjpegFrame(ctx, e.client, "http://"+net.JoinHostPort(e.host, "3031")+"/video")
	return b, "image/jpeg", err
}

// Upload sends the file in 1 MB chunks (SDCP upload API), then starts it.
func (e *Elegoo) Upload(ctx context.Context, name string, r io.Reader, start bool) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	sum := md5.Sum(data)
	md5hex := hex.EncodeToString(sum[:])
	uuid := newHexID()
	const chunk = 1 << 20
	for off := 0; off < len(data) || off == 0; off += chunk {
		end := off + chunk
		if end > len(data) {
			end = len(data)
		}
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("S-File-MD5", md5hex)
		_ = mw.WriteField("Check", "1")
		_ = mw.WriteField("Offset", strconv.Itoa(off))
		_ = mw.WriteField("Uuid", uuid)
		_ = mw.WriteField("TotalSize", strconv.Itoa(len(data)))
		fw, _ := mw.CreateFormFile("File", name)
		fw.Write(data[off:end])
		mw.Close()
		req, _ := http.NewRequestWithContext(ctx, "POST", "http://"+net.JoinHostPort(e.host, strconv.Itoa(e.port))+"/uploadFile/upload", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := e.client.Do(req)
		if err != nil {
			return err
		}
		var res struct {
			Code    string `json:"code"`
			Success bool   `json:"success"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		if resp.StatusCode != 200 || (!res.Success && res.Code != "000000") {
			return fmt.Errorf("elegoo upload failed (%s %s)", resp.Status, res.Code)
		}
		if end == len(data) {
			break
		}
	}
	if !start {
		return nil
	}
	e.ensure()
	return e.request(128, map[string]any{"Filename": "/local/" + name, "StartLayer": 0, "Calibration_switch": 0, "PrintPlatformType": 0, "Tlc": 0})
}
