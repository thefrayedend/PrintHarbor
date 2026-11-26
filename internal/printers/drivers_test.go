package printers

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFlashForge(t *testing.T) {
	var mu sync.Mutex
	var actions []string
	var upload struct {
		name, printNow, mapping string
		chunked                 bool
		contentLen              int64
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/uploadGcode" {
			if r.Header.Get("checkCode") != "abc" {
				io.WriteString(w, `{"code":1,"message":"check code error"}`)
				return
			}
			mu.Lock()
			upload.contentLen = r.ContentLength
			for _, te := range r.TransferEncoding {
				if te == "chunked" {
					upload.chunked = true
				}
			}
			mu.Unlock()
			r.ParseMultipartForm(1 << 20)
			_, h, _ := r.FormFile("gcodeFile")
			mu.Lock()
			upload.name, upload.printNow = h.Filename, r.Header.Get("printNow")
			if b, err := base64.StdEncoding.DecodeString(r.Header.Get("materialMappings")); err == nil {
				upload.mapping = string(b)
			}
			mu.Unlock()
			io.WriteString(w, `{"code":0,"message":"Success"}`)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["checkCode"] != "abc" {
			io.WriteString(w, `{"code":1,"message":"check code error"}`)
			return
		}
		switch r.URL.Path {
		case "/detail":
			io.WriteString(w, `{"code":0,"message":"Success","detail":{"status":"printing","printProgress":0.5,"printFileName":"part.gcode",
			  "rightTemp":220,"rightTargetTemp":220,"platTemp":60,"platTargetTemp":60,"estimatedTime":600,"lightStatus":"open",
			  "matlStationInfo":{"slotInfos":[{"slotId":1,"materialName":"PLA","materialColor":"#FF0000","hasFilament":true},{"slotId":2,"hasFilament":false}]}}}`)
		case "/control":
			p := body["payload"].(map[string]any)
			args := p["args"].(map[string]any)
			mu.Lock()
			actions = append(actions, p["cmd"].(string)+":"+strings.TrimSpace(strv(args["action"])+strv(args["status"])))
			mu.Unlock()
			io.WriteString(w, `{"code":0,"message":"Success"}`)
		}
	}))
	defer srv.Close()
	f, err := NewFlashForge(srv.URL, "SN1", "abc")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := f.Status(ctx)
	if err != nil || st.State != StatePrinting || st.Progress != 0.5 || st.Light != "on" || len(st.Slots) != 2 || st.Slots[0].Color != "#FF0000" || st.Slots[1].Type != "" {
		t.Fatalf("%+v %v", st, err)
	}
	f.Pause(ctx)
	f.SetLight(ctx, false)
	if err := f.Upload(WithSlotMap(ctx, []int{1}), "a.gcode", strings.NewReader("G28"), true); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(actions, ",") != "jobCtl_cmd:pause,lightControl_cmd:close" || upload.name != "a.gcode" || upload.printNow != "true" || !strings.Contains(upload.mapping, `"slotId":1`) {
		t.Fatalf("actions %v upload %+v", actions, upload)
	}
	// FlashForge firmware 500s on a chunked upload: it must have a real
	// Content-Length. The material mapping is OrcaSlicer's JSON, base64-encoded.
	if upload.chunked || upload.contentLen <= 0 {
		t.Errorf("upload must have Content-Length and not be chunked: len=%d chunked=%v", upload.contentLen, upload.chunked)
	}
	if !strings.Contains(upload.mapping, `"materialName":"PLA"`) || !strings.Contains(upload.mapping, `"toolId":0`) {
		t.Errorf("mapping not OrcaSlicer format: %s", upload.mapping)
	}
	bad, _ := NewFlashForge(srv.URL, "SN1", "nope")
	if _, err := bad.Status(ctx); err == nil || !strings.Contains(err.Error(), "pairing code") {
		t.Fatal("bad code", err)
	}
}

func TestPrusaLinkDigest(t *testing.T) {
	var gotPrint, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Digest ") {
			w.Header().Set("WWW-Authenticate", `Digest realm="Printer API", nonce="abc123", qop="auth"`)
			w.WriteHeader(401)
			return
		}
		d := parseDigest(auth)
		ha1 := md5hex("maker:Printer API:secret")
		want := md5hex(ha1 + ":abc123:" + d["nc"] + ":" + d["cnonce"] + ":auth:" + md5hex(r.Method+":"+d["uri"]))
		if d["response"] != want {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/api/v1/status":
			io.WriteString(w, `{"printer":{"state":"PRINTING","temp_nozzle":215,"target_nozzle":215,"temp_bed":60,"target_bed":60},"job":{"id":42,"progress":25,"time_remaining":900}}`)
		case r.URL.Path == "/api/v1/job":
			io.WriteString(w, `{"id":42,"file":{"display_name":"benchy.gcode"}}`)
		case strings.HasPrefix(r.URL.Path, "/api/v1/files/usb/"):
			gotPath, gotPrint = r.URL.Path, r.Header.Get("Print-After-Upload")
			w.WriteHeader(201)
		case r.URL.Path == "/api/v1/job/42/pause":
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	p, _ := NewPrusaLink(srv.URL, "maker:secret")
	ctx := context.Background()
	st, err := p.Status(ctx)
	if err != nil || st.State != StatePrinting || st.Progress != 0.25 || st.JobName != "benchy.gcode" {
		t.Fatalf("%+v %v", st, err)
	}
	if err := p.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Upload(ctx, "part one.gcode", strings.NewReader("G28"), true); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/files/usb/part one.gcode" || gotPrint != "?1" {
		t.Fatal(gotPath, gotPrint)
	}
	wrong, _ := NewPrusaLink(srv.URL, "maker:nope")
	if _, err := wrong.Status(ctx); err == nil {
		t.Fatal("wrong password must fail")
	}
}

func TestElegooSDCP(t *testing.T) {
	var mu sync.Mutex
	var cmds []float64
	var uploaded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/uploadFile/upload":
			r.ParseMultipartForm(8 << 20)
			f, _, _ := r.FormFile("File")
			b, _ := io.ReadAll(f)
			off, _ := strconv.Atoi(r.FormValue("Offset"))
			mu.Lock()
			if off == len(uploaded) {
				uploaded = append(uploaded, b...)
			}
			mu.Unlock()
			io.WriteString(w, `{"code":"000000","success":true}`)
		case "/websocket":
			h := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			conn, brw, _ := w.(http.Hijacker).Hijack()
			defer conn.Close()
			io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+
				base64.StdEncoding.EncodeToString(h[:])+"\r\n\r\n")
			br := bufio.NewReader(brw)
			send := func(v any) {
				b, _ := json.Marshal(v)
				frame := []byte{0x81, 126, byte(len(b) >> 8), byte(len(b))}
				conn.Write(append(frame, b...))
			}
			for {
				_, p, err := wsRead(br)
				if err != nil {
					return
				}
				var m struct {
					Data struct {
						Cmd         float64 `json:"Cmd"`
						MainboardID string  `json:"MainboardID"`
					} `json:"Data"`
				}
				json.Unmarshal(p, &m)
				mu.Lock()
				cmds = append(cmds, m.Data.Cmd)
				mu.Unlock()
				if m.Data.Cmd == 0 {
					send(map[string]any{"Topic": "sdcp/status/" + m.Data.MainboardID, "Status": map[string]any{
						"CurrentStatus": []any{1}, "TempOfNozzle": 210, "TempTargetNozzle": 210, "TempOfHotbed": 60, "TempTargetHotbed": 60,
						"PrintInfo":   map[string]any{"Status": 13, "CurrentLayer": 50, "TotalLayer": 200, "Filename": "rex.gcode", "CurrentTicks": 100, "TotalTicks": 400},
						"LightStatus": map[string]any{"SecondLight": 1}}})
				}
			}
		}
	}))
	defer srv.Close()
	// UDP discovery responder.
	udp, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer udp.Close()
	go func() {
		buf := make([]byte, 64)
		for {
			n, addr, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			if string(buf[:n]) == "M99999" {
				udp.WriteTo([]byte(`{"Id":"x","Data":{"Name":"CC","MainboardID":"MB42"}}`), addr)
			}
		}
	}()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	e, _ := NewElegoo("127.0.0.1", "")
	e.port, _ = strconv.Atoi(port)
	e.discPort = udp.LocalAddr().(*net.UDPAddr).Port
	ctx := context.Background()
	var st Status
	for i := 0; i < 100; i++ {
		st, _ = e.Status(ctx)
		if st.State == StatePrinting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.State != StatePrinting || st.Progress != 0.25 || st.RemainingSec != 300 || st.JobName != "rex.gcode" || st.Light != "on" || e.mainboard != "MB42" {
		t.Fatalf("%+v mb=%s", st, e.mainboard)
	}
	big := strings.Repeat("G1 X1\n", 300000) // ~1.8 MB → two chunks
	if err := e.Upload(ctx, "rex.gcode", strings.NewReader(big), true); err != nil {
		t.Fatal(err)
	}
	e.Pause(ctx)
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(uploaded) != len(big) {
		t.Fatalf("uploaded %d of %d bytes", len(uploaded), len(big))
	}
	has := func(c float64) bool {
		for _, x := range cmds {
			if x == c {
				return true
			}
		}
		return false
	}
	if !has(128) || !has(129) {
		t.Fatalf("commands %v", cmds)
	}
}

func TestKlipperObjects(t *testing.T) {
	tools, chamber, light := klipperObjects([]string{"webhooks", "extruder", "extruder2", "extruder1", "extruder3",
		"heater_bed", "temperature_sensor chamber_temp", "output_pin caselight", "fan"})
	if len(tools) != 4 || tools[1] != "extruder1" || tools[3] != "extruder3" {
		t.Fatalf("tools %v", tools)
	}
	if chamber != "temperature_sensor chamber_temp" || light != "output_pin caselight" {
		t.Fatalf("%q %q", chamber, light)
	}
}

// Creator 5 Pro: four nozzles, chamber, filtration; temperatures go as a
// nozzles array and colours snap to the firmware palette (FlashBridge ff_client).
func TestFlashForgeCreator5(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/detail":
			io.WriteString(w, `{"code":0,"message":"Success","detail":{"pid":41,"name":"Creator 5 Pro","status":"completed","printFileName":"cube.gcode",
			  "nozzleTemps":[23,210,25,24],"nozzleTargetTemps":[0,220,0,0],"platTemp":22,"platTargetTemp":60,"chamberTemp":23,"chamberTargetTemp":0,
			  "lightStatus":"open","internalFanStatus":"open","externalFanStatus":"close","printSpeedAdjust":100,"zAxisCompensation":0.02,
			  "coolingFanSpeed":50,"chamberFanSpeed":0,"matlStationInfo":{"currentSlot":2,"slotInfos":[{"slotId":1,"materialName":"PLA","materialColor":"FFFFFFFF","hasFilament":true}]}}}`)
		case "/control":
			got = append(got, body["payload"].(map[string]any))
			io.WriteString(w, `{"code":0,"message":"Success"}`)
		}
	}))
	defer srv.Close()
	f, _ := NewFlashForge(srv.URL, "SN1", "abc")
	ctx := context.Background()
	st, err := f.Status(ctx)
	if err != nil || st.State != StateFinished || len(st.Tools) != 4 || st.Tools[1].Target != 220 || st.ChamberHeater == "" || st.Slots[0].Color != "#FFFFFF" {
		t.Fatalf("%+v %v", st, err)
	}
	if st.FF == nil || !st.FF.HasFiltration || !st.FF.FiltrationInternal || st.FF.CurrentSlot != 2 {
		t.Fatalf("%+v", st.FF)
	}
	if err := f.Control(ctx, "temp", map[string]any{"heater": "tool2", "value": 230.0}); err != nil {
		t.Fatal(err)
	}
	if err := f.Control(ctx, "zoffset", map[string]any{"distance": 0.01}); err != nil {
		t.Fatal(err)
	}
	if err := f.Control(ctx, "filtration", map[string]any{"external": true}); err != nil {
		t.Fatal(err)
	}
	if err := f.SetSlot(ctx, 1, "PETG", "#FF0000"); err != nil {
		t.Fatal(err)
	}
	if err := f.ClearPlatform(ctx); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	want := []string{`"nozzles":[-200,-200,230,-200]`, `"zAxisCompensation":0.03`, `"external":"open","internal":"open"`, `"rgb":"F82D29"`, `"action":"setClearPlatform"`}
	for _, w := range want {
		if !strings.Contains(string(b), w) {
			t.Fatalf("missing %s in %s", w, b)
		}
	}
}

// The firmware sends whole numbers as 0.0, numbers as strings and nulls; all
// must parse like FlashBridge's models.dart helpers.
func TestFFDetailLenient(t *testing.T) {
	var d ffDetail
	err := json.Unmarshal([]byte(`{"pid":41.0,"printSpeedAdjust":0.0,"printLayer":"12","targetPrintLayer":null,"nozzleTemps":[23.5,"24",null,25],
	  "chamberTemp":"23","lightStatus":null,"errorCode":0,"matlStationInfo":{"currentSlot":"2","slotInfos":[{"slotId":1.0,"hasFilament":"true","materialName":"PLA","materialColor":"FF0000"}]}}`), &d)
	if err != nil {
		t.Fatal(err)
	}
	if !d.isCreator5Pro() || d.PrintSpeedAdjust.I() != 0 || d.PrintLayer.I() != 12 || len(d.NozzleTemps) != 4 || d.NozzleTemps[1].F() != 24 ||
		d.ChamberTemp.F() != 23 || d.MatlStationInfo.CurrentSlot.I() != 2 || !bool(d.MatlStationInfo.SlotInfos[0].HasFilament) || d.ErrorCode != "0" {
		t.Fatalf("%+v", d)
	}
}

// Snapmaker U1: loaded filament from print_task_config, changed with
// SET_PRINT_FILAMENT_CONFIG (Snapmaker's u1-klipper print_task_config.py).
func TestMoonrakerU1Filament(t *testing.T) {
	var script string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/printer/objects/list":
			io.WriteString(w, `{"result":{"objects":["webhooks","extruder","extruder1","extruder2","extruder3","heater_bed","print_task_config"]}}`)
		case "/printer/objects/query":
			io.WriteString(w, `{"result":{"status":{"webhooks":{"state":"ready"},"print_stats":{"state":"standby"},
			  "print_task_config":{"filament_vendor":["Snapmaker","Generic","NONE","Snapmaker"],"filament_type":["PLA","PETG","NONE","PLA"],
			  "filament_sub_type":["SnapSpeed","Basic","NONE","Matte"],"filament_color_rgba":["E2DEDBFF","E72F1DFF","FFFFFFFF","080A0DFF"],
			  "filament_exist":[true,true,false,true]}}}}`)
		case "/printer/gcode/script":
			script = r.URL.Query().Get("script")
			io.WriteString(w, `{"result":"ok"}`)
		default:
			io.WriteString(w, `{"result":{}}`)
		}
	}))
	defer srv.Close()
	m, _ := NewMoonraker(srv.URL, "")
	ctx := context.Background()
	st, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Slots) != 4 || st.Slots[0].Type != "PLA" || st.Slots[0].Color != "#E2DEDB" || st.Slots[2].Type != "" || st.Slots[3].Name != "Matte" {
		t.Fatalf("%+v", st.Slots)
	}
	if !m.Capabilities().SetSlots {
		t.Fatal("U1 should allow changing filament")
	}
	if err := m.SetSlot(ctx, 2, "PETG", "#00ff00"); err != nil {
		t.Fatal(err)
	}
	want := "SET_PRINT_FILAMENT_CONFIG CONFIG_EXTRUDER=1 VENDOR=Generic FILAMENT_TYPE=PETG FILAMENT_SUBTYPE=Basic FILAMENT_COLOR_RGBA=00FF00FF FORCE=1"
	if script != want {
		t.Fatalf("got %q", script)
	}
}

func TestParseListLine(t *testing.T) {
	e, ok := parseListLine("-rw-r--r--    1 root     root      1234567 Nov 08 10:22 Benchy plate 1.gcode.3mf")
	if !ok || e.Name != "Benchy plate 1.gcode.3mf" || e.Size != 1234567 || e.IsDir {
		t.Fatalf("%+v %v", e, ok)
	}
	if d, ok := parseListLine("drwxr-xr-x    2 root     root         4096 Nov 08 10:22 cache"); !ok || !d.IsDir {
		t.Fatal("dir")
	}
}

// Bambu AMS report → slots (empty trays included) and the tray in use.
func TestBambuSlots(t *testing.T) {
	b, _ := NewBambu("127.0.0.1", "SN", "code")
	b.started, b.connected = true, true
	b.onMessage("", []byte(`{"print":{"gcode_state":"RUNNING","mc_percent":40,"ams":{"tray_now":"1","ams":[{"id":"0","tray":[
	  {"id":"0","tray_type":"PLA","tray_color":"FF0000FF"},{"id":"1","tray_type":"PETG","tray_color":"00FF00FF"},{"id":"2"},{"id":"3","tray_type":"PLA","tray_color":"000000FF"}]}]},
	  "vt_tray":{"tray_type":"TPU","tray_color":"FFFFFFFF"}}}`))
	st, _ := b.Status(context.Background())
	if len(st.Slots) != 5 || st.Slots[2].Type != "" || st.Slots[1].Color != "#00FF00" || st.CurrentSlot != 2 || st.Slots[4].Index != 99 {
		t.Fatalf("%+v current %d", st.Slots, st.CurrentSlot)
	}
}

// The Snapmaker U1's Moonraker drops chunked uploads; the upload must be sent
// with a real Content-Length, not Transfer-Encoding: chunked.
func TestMoonrakerUploadContentLength(t *testing.T) {
	var gotLen int64 = -1
	var chunked bool
	var sawFile, sawPrint bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLen = r.ContentLength
		for _, te := range r.TransferEncoding {
			if te == "chunked" {
				chunked = true
			}
		}
		if err := r.ParseMultipartForm(1 << 20); err == nil {
			if f, _, err := r.FormFile("file"); err == nil {
				f.Close()
				sawFile = true
			}
			sawPrint = r.FormValue("print") == "true"
		}
		io.WriteString(w, `{"result":{}}`)
	}))
	defer srv.Close()
	m, _ := NewMoonraker(srv.URL, "")
	if err := m.Upload(context.Background(), "dragon.gcode", strings.NewReader("G28\nG1 X1\n"), true); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if gotLen <= 0 {
		t.Errorf("no Content-Length set (got %d) — upload was chunked", gotLen)
	}
	if chunked {
		t.Error("upload used chunked transfer encoding")
	}
	if !sawFile || !sawPrint {
		t.Errorf("multipart wrong: file=%v print=%v", sawFile, sawPrint)
	}
}
