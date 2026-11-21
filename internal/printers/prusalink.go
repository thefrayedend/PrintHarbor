package printers

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Prusa printers running PrusaLink (MK4/MK4S, MK3.9, XL, MINI, Core One).
// API key = the PrusaLink API key, or "user:password" for digest login
// (the default user is "maker").
func init() {
	Register(DriverInfo{Name: "prusalink", Label: "Prusa (PrusaLink)", AddressHint: "192.168.1.90",
		NeedsKey: true, KeyLabel: "PrusaLink API key, or maker:password", Help: "Find it on the printer under Settings → Network → PrusaLink."},
		func(p Printer) (Driver, error) { return NewPrusaLink(p.Address, p.APIKey) })
}

type PrusaLink struct {
	base   *url.URL
	key    string
	user   string
	pass   string
	client *http.Client

	mu     sync.Mutex
	jobID  int
	digest map[string]string // last digest challenge
	nc     int
}

func NewPrusaLink(addr, key string) (*PrusaLink, error) {
	a := strings.TrimSpace(addr)
	if a == "" {
		return nil, fmt.Errorf("address is required")
	}
	if !strings.Contains(a, "://") {
		a = "http://" + a
	}
	u, err := url.Parse(strings.TrimRight(a, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad address %q", addr)
	}
	p := &PrusaLink{base: u, client: &http.Client{Timeout: 10 * time.Minute}}
	if user, pass, ok := strings.Cut(strings.TrimSpace(key), ":"); ok {
		p.user, p.pass = user, pass
	} else {
		p.key = strings.TrimSpace(key)
	}
	return p, nil
}

func (p *PrusaLink) Capabilities() Capabilities {
	return Capabilities{Status: true, Pause: true, Resume: true, Cancel: true, Upload: true, StartPrint: true, Temperatures: true}
}

func (p *PrusaLink) OutputFormat() string { return "gcode" }

// do sends a request, answering an HTTP Digest challenge if one comes back.
func (p *PrusaLink) do(ctx context.Context, method, path string, body func() io.Reader, hdr map[string]string, out any) (int, error) {
	try := func(auth string) (*http.Response, error) {
		var r io.Reader
		if body != nil {
			r = body()
		}
		req, err := http.NewRequestWithContext(ctx, method, p.base.String()+path, r)
		if err != nil {
			return nil, err
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		if p.key != "" {
			req.Header.Set("X-Api-Key", p.key)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		return p.client.Do(req)
	}
	resp, err := try(p.digestHeader(method, path))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode == 401 && p.user != "" {
		ch := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		if !strings.HasPrefix(ch, "Digest ") {
			return 401, fmt.Errorf("printer refused the login")
		}
		p.mu.Lock()
		p.digest, p.nc = parseDigest(ch), 0
		p.mu.Unlock()
		if resp, err = try(p.digestHeader(method, path)); err != nil {
			return 0, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return resp.StatusCode, fmt.Errorf("printer refused the API key or password")
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, fmt.Errorf("prusalink %s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil && resp.StatusCode != 204 {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}

func parseDigest(h string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(h, "Digest "), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			out[k] = strings.Trim(v, `"`)
		}
	}
	return out
}

func md5hex(s string) string { h := md5.Sum([]byte(s)); return hex.EncodeToString(h[:]) }

func (p *PrusaLink) digestHeader(method, uri string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.user == "" || p.digest == nil {
		return ""
	}
	d := p.digest
	p.nc++
	nc := fmt.Sprintf("%08x", p.nc)
	cnonce := newHexID()[:16]
	ha1 := md5hex(p.user + ":" + d["realm"] + ":" + p.pass)
	ha2 := md5hex(method + ":" + uri)
	var resp string
	if strings.Contains(d["qop"], "auth") {
		resp = md5hex(ha1 + ":" + d["nonce"] + ":" + nc + ":" + cnonce + ":auth:" + ha2)
		return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", qop=auth, nc=%s, cnonce="%s", response="%s", opaque="%s"`,
			p.user, d["realm"], d["nonce"], uri, nc, cnonce, resp, d["opaque"])
	}
	resp = md5hex(ha1 + ":" + d["nonce"] + ":" + ha2)
	return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`, p.user, d["realm"], d["nonce"], uri, resp)
}

func (p *PrusaLink) Status(ctx context.Context) (Status, error) {
	var s struct {
		Printer struct {
			State        string  `json:"state"`
			TempNozzle   float64 `json:"temp_nozzle"`
			TargetNozzle float64 `json:"target_nozzle"`
			TempBed      float64 `json:"temp_bed"`
			TargetBed    float64 `json:"target_bed"`
		} `json:"printer"`
		Job *struct {
			ID            int     `json:"id"`
			Progress      float64 `json:"progress"`
			TimeRemaining int     `json:"time_remaining"`
		} `json:"job"`
	}
	if _, err := p.do(ctx, "GET", "/api/v1/status", nil, nil, &s); err != nil {
		return Status{State: StateOffline}, err
	}
	st := Status{Nozzle: s.Printer.TempNozzle, NozzleTarget: s.Printer.TargetNozzle, Bed: s.Printer.TempBed, BedTarget: s.Printer.TargetBed}
	if s.Job != nil {
		st.Progress, st.RemainingSec = s.Job.Progress/100, s.Job.TimeRemaining
		p.mu.Lock()
		p.jobID = s.Job.ID
		p.mu.Unlock()
		var j struct {
			File struct {
				DisplayName string `json:"display_name"`
			} `json:"file"`
		}
		if _, err := p.do(ctx, "GET", "/api/v1/job", nil, nil, &j); err == nil {
			st.JobName = j.File.DisplayName
		}
	}
	switch strings.ToUpper(s.Printer.State) {
	case "PRINTING", "BUSY":
		st.State = StatePrinting
	case "PAUSED":
		st.State = StatePaused
	case "FINISHED":
		st.State = StateFinished
	case "ERROR", "ATTENTION":
		st.State, st.Message = StateError, "check the printer screen"
	case "STOPPED":
		st.State, st.Stopped = StateIdle, true
	default: // IDLE, READY
		st.State = StateIdle
	}
	return st, nil
}

func (p *PrusaLink) jobAction(ctx context.Context, method, suffix string) error {
	p.mu.Lock()
	id := p.jobID
	p.mu.Unlock()
	if id == 0 {
		return fmt.Errorf("no job running")
	}
	_, err := p.do(ctx, method, "/api/v1/job/"+strconv.Itoa(id)+suffix, nil, nil, nil)
	return err
}

func (p *PrusaLink) Pause(ctx context.Context) error  { return p.jobAction(ctx, "PUT", "/pause") }
func (p *PrusaLink) Resume(ctx context.Context) error { return p.jobAction(ctx, "PUT", "/resume") }
func (p *PrusaLink) Cancel(ctx context.Context) error { return p.jobAction(ctx, "DELETE", "") }

// Upload PUTs the file to USB storage (falling back to internal storage).
func (p *PrusaLink) Upload(ctx context.Context, name string, r io.Reader, start bool) error {
	tmp, err := os.CreateTemp("", "prusa-*.gcode")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	size, err := io.Copy(tmp, r)
	tmp.Close()
	if err != nil {
		return err
	}
	hdr := map[string]string{"Content-Type": "application/octet-stream", "Overwrite": "?1", "Content-Length": strconv.FormatInt(size, 10)}
	if start {
		hdr["Print-After-Upload"] = "?1"
	}
	body := func() io.Reader { f, _ := os.Open(tmp.Name()); return f }
	var lastErr error
	for _, storage := range []string{"usb", "local"} {
		code, err := p.do(ctx, "PUT", "/api/v1/files/"+storage+"/"+url.PathEscape(name), body, hdr, nil)
		if err == nil {
			return nil
		}
		lastErr = err
		if code != 404 && code != 409 {
			break
		}
	}
	return lastErr
}
