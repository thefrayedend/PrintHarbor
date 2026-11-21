package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed ffai_server.py
var serverPy []byte

//go:embed requirements.txt
var requirements []byte

// Obico's model (obico-server/ml_api/model/model-weights.onnx.{url,sha256}),
// the same file and checksum FlashBridge's fetch-model.sh uses.
const (
	ModelURL    = "https://www.obico.io/static_pub/ml-models/model-weights-5a6b1be1fa.onnx"
	ModelSHA256 = "0a6ebd8e30dbf6a450c50f9c0a5406f04ba7eb1c99fd5996e888c78bb383b9aa"
)

// Service installs and runs the local AI service: a Python venv with ONNX
// Runtime in PrintHarbor's data folder, the model, and ffai_server.py.
type Service struct {
	Dir  string // data/ai
	Port int

	mu         sync.Mutex
	installing bool
	step       string
	progress   float64 // 0..1 for the current step
	installErr string
	cmd        *exec.Cmd
	running    bool
	health     Health
	healthAt   time.Time
	cancelRun  context.CancelFunc
	client     *http.Client
}

// Health is the service's /health answer.
type Health struct {
	Ready   bool    `json:"ready"`
	Model   string  `json:"model"`
	Backend string  `json:"backend"`
	AvgMs   float64 `json:"avg_ms"`
	Error   string  `json:"error"`
}

// Status is shown on the Settings page.
type Status struct {
	Installed  bool    `json:"installed"`
	Installing bool    `json:"installing"`
	Step       string  `json:"step,omitempty"`
	Progress   float64 `json:"progress"`
	Error      string  `json:"error,omitempty"`
	Running    bool    `json:"running"`
	Reachable  bool    `json:"reachable"`
	Health
}

func New(dir string, port int) *Service {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs // programs run with Dir set; relative paths would point inside it
	}
	return &Service{Dir: dir, Port: port, client: &http.Client{Timeout: 60 * time.Second}}
}

func (s *Service) venvPython(gui bool) string {
	if runtime.GOOS == "windows" {
		if gui {
			return filepath.Join(s.Dir, "venv", "Scripts", "pythonw.exe")
		}
		return filepath.Join(s.Dir, "venv", "Scripts", "python.exe")
	}
	return filepath.Join(s.Dir, "venv", "bin", "python3")
}

func (s *Service) modelPath() string { return filepath.Join(s.Dir, "model-weights.onnx") }
func (s *Service) marker() string    { return filepath.Join(s.Dir, "installed.txt") }

// Installed reports whether everything is in place.
func (s *Service) Installed() bool {
	if _, err := os.Stat(s.marker()); err != nil {
		return false
	}
	if _, err := os.Stat(s.modelPath()); err != nil {
		return false
	}
	_, err := os.Stat(s.venvPython(false))
	return err == nil
}

func (s *Service) set(step string, p float64) {
	s.mu.Lock()
	s.step, s.progress = step, p
	s.mu.Unlock()
}

// Install sets everything up (a few minutes: ~200 MB model, ONNX Runtime).
// It runs in the background; Status shows progress.
func (s *Service) Install(after func()) error {
	s.mu.Lock()
	if s.installing {
		s.mu.Unlock()
		return fmt.Errorf("already installing")
	}
	s.installing, s.installErr = true, ""
	s.mu.Unlock()
	go func() {
		err := s.install(context.Background())
		s.mu.Lock()
		s.installing, s.step = false, ""
		if err != nil {
			s.installErr = err.Error()
		}
		s.mu.Unlock()
		if err != nil {
			log.Printf("AI install failed: %v", err)
			return
		}
		log.Printf("AI failure detection installed in %s", s.Dir)
		if after != nil {
			after()
		}
	}()
	return nil
}

func (s *Service) install(ctx context.Context) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	logf, _ := os.OpenFile(filepath.Join(s.Dir, "install.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if logf != nil {
		defer logf.Close()
	}
	run := func(name string, args ...string) error {
		c := exec.CommandContext(ctx, name, args...)
		c.Dir = s.Dir
		var out bytes.Buffer
		c.Stdout, c.Stderr = &out, &out
		hideWindow(c)
		err := c.Run()
		if logf != nil {
			fmt.Fprintf(logf, "$ %s %s\n%s\n", name, strings.Join(args, " "), out.String())
		}
		if err != nil {
			tail := strings.TrimSpace(out.String())
			if len(tail) > 600 {
				tail = "…" + tail[len(tail)-600:]
			}
			return fmt.Errorf("%s failed: %v\n%s", filepath.Base(name), err, tail)
		}
		return nil
	}
	_ = os.Remove(s.marker())

	if _, err := os.Stat(s.venvPython(false)); err != nil {
		s.set("Finding Python", 0)
		py, args, err := findPython(ctx)
		if err != nil {
			return err
		}
		s.set("Creating a private Python environment", 0)
		if err := run(py, append(args, "-m", "venv", filepath.Join(s.Dir, "venv"))...); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "requirements.txt"), requirements, 0o644); err != nil {
		return err
	}
	s.set("Installing ONNX Runtime (this takes a few minutes)", 0)
	_ = run(s.venvPython(false), "-m", "pip", "install", "--upgrade", "pip")
	if err := run(s.venvPython(false), "-m", "pip", "install", "-r", "requirements.txt"); err != nil {
		// ONNX Runtime may not have wheels for a brand-new Python yet. With
		// Windows' Python install manager ("py"), get 3.12 and retry once.
		if runtime.GOOS != "windows" {
			return fmt.Errorf("couldn't install ONNX Runtime for this Python version. Installing Python 3.12 usually fixes this (then click Install again).\n%v", err)
		}
		s.set("Getting Python 3.12 for ONNX Runtime", 0)
		_ = run("py", "install", "3.12", "--yes")
		if run("py", "-3.12", "--version") != nil {
			return fmt.Errorf("couldn't install ONNX Runtime for this Python version. Install Python 3.12 from python.org (tick \"Add python.exe to PATH\"), then click Install again.\n%v", err)
		}
		_ = os.RemoveAll(filepath.Join(s.Dir, "venv"))
		s.set("Creating a private Python environment", 0)
		if err := run("py", "-3.12", "-m", "venv", filepath.Join(s.Dir, "venv")); err != nil {
			return err
		}
		s.set("Installing ONNX Runtime (this takes a few minutes)", 0)
		_ = run(s.venvPython(false), "-m", "pip", "install", "--upgrade", "pip")
		if err := run(s.venvPython(false), "-m", "pip", "install", "-r", "requirements.txt"); err != nil {
			return fmt.Errorf("couldn't install ONNX Runtime.\n%v", err)
		}
	}
	if err := s.fetchModel(ctx); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "ffai_server.py"), serverPy, 0o644); err != nil {
		return err
	}
	return os.WriteFile(s.marker(), []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644)
}

// findPython looks for Python 3.9-3.13 (ONNX Runtime has wheels for these),
// then any Python 3.
func findPython(ctx context.Context) (string, []string, error) {
	type cand struct {
		name string
		args []string
	}
	var cands []cand
	if runtime.GOOS == "windows" {
		for _, v := range []string{"3.12", "3.13", "3.11", "3.10", "3.14"} {
			cands = append(cands, cand{"py", []string{"-" + v}})
		}
		cands = append(cands, cand{"py", []string{"-3"}}, cand{"python", nil}, cand{"python3", nil})
	} else {
		cands = []cand{{"python3.12", nil}, {"python3.13", nil}, {"python3.11", nil}, {"python3", nil}, {"python", nil}}
	}
	re := regexp.MustCompile(`Python 3\.(\d+)`)
	for _, c := range cands {
		cmd := exec.CommandContext(ctx, c.name, append(c.args, "--version")...)
		hideWindow(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			continue
		}
		m := re.FindSubmatch(out)
		if m == nil {
			continue
		}
		if minor, _ := strconv.Atoi(string(m[1])); minor >= 9 {
			return c.name, c.args, nil
		}
	}
	return "", nil, fmt.Errorf("Python 3 wasn't found. Install Python 3.12 from python.org (tick \"Add python.exe to PATH\"), then click Install again")
}

func (s *Service) fetchModel(ctx context.Context) error {
	if ok, _ := fileSHA(s.modelPath(), ModelSHA256); ok {
		return nil
	}
	s.set("Downloading the AI model (about 200 MB)", 0)
	req, _ := http.NewRequestWithContext(ctx, "GET", ModelURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't download the model: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("couldn't download the model: %s", resp.Status)
	}
	part := s.modelPath() + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	h := sha256.New()
	total := resp.ContentLength
	var got int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			h.Write(buf[:n])
			got += int64(n)
			if total > 0 {
				s.set("Downloading the AI model (about 200 MB)", float64(got)/float64(total))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return fmt.Errorf("model download interrupted: %v", rerr)
		}
	}
	f.Close()
	if sum := hex.EncodeToString(h.Sum(nil)); sum != ModelSHA256 {
		os.Remove(part)
		return fmt.Errorf("the downloaded model's checksum is wrong (%s); try again", sum[:12])
	}
	return os.Rename(part, s.modelPath())
}

func fileSHA(path, want string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == want, nil
}

// Run keeps the service running until ctx ends, restarting it if it stops.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	if s.cancelRun != nil {
		s.mu.Unlock()
		return
	}
	rctx, cancel := context.WithCancel(ctx)
	s.cancelRun = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.cancelRun, s.running = nil, false
		s.mu.Unlock()
	}()
	backoff := 2 * time.Second
	for rctx.Err() == nil {
		if !s.Installed() {
			return
		}
		_ = os.WriteFile(filepath.Join(s.Dir, "ffai_server.py"), serverPy, 0o644) // keep it current
		logf, _ := os.OpenFile(filepath.Join(s.Dir, "ai.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		cmd := exec.CommandContext(rctx, s.venvPython(true), "ffai_server.py")
		cmd.Dir = s.Dir
		cmd.Env = append(os.Environ(), "FFAI_MODEL="+s.modelPath(), "FFAI_PORT="+strconv.Itoa(s.Port), "FFAI_HOST=127.0.0.1", "PYTHONUNBUFFERED=1")
		if logf != nil {
			cmd.Stdout, cmd.Stderr = logf, logf
		}
		hideWindow(cmd)
		start := time.Now()
		err := cmd.Start()
		if err == nil {
			s.mu.Lock()
			s.cmd, s.running = cmd, true
			s.mu.Unlock()
			log.Printf("AI service started (port %d)", s.Port)
			err = cmd.Wait()
		}
		if logf != nil {
			logf.Close()
		}
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		if rctx.Err() != nil {
			return
		}
		log.Printf("AI service stopped (%v); restarting", err)
		if time.Since(start) > time.Minute {
			backoff = 2 * time.Second
		}
		select {
		case <-rctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

// Stop ends the service (Run returns).
func (s *Service) Stop() {
	s.mu.Lock()
	c := s.cancelRun
	s.mu.Unlock()
	if c != nil {
		c()
	}
}

func (s *Service) base() string { return fmt.Sprintf("http://127.0.0.1:%d", s.Port) }

// CheckHealth asks the service how it's doing (cached for a few seconds).
func (s *Service) CheckHealth(ctx context.Context) (Health, bool) {
	s.mu.Lock()
	if time.Since(s.healthAt) < 4*time.Second {
		h, ok := s.health, s.health.Model != "" || s.health.Error != ""
		s.mu.Unlock()
		return h, ok
	}
	s.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, "GET", s.base()+"/health", nil)
	var h Health
	ok := false
	if resp, err := s.client.Do(req); err == nil {
		ok = json.NewDecoder(resp.Body).Decode(&h) == nil
		resp.Body.Close()
	}
	s.mu.Lock()
	s.health, s.healthAt = h, time.Now()
	s.mu.Unlock()
	return h, ok
}

// Status for the page.
func (s *Service) Status(ctx context.Context) Status {
	h, reach := s.CheckHealth(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{Installed: s.Installed(), Installing: s.installing, Step: s.step, Progress: s.progress, Error: s.installErr,
		Running: s.running, Reachable: reach, Health: h}
}

// Result is one analysed picture.
type Result struct {
	Detections []Detection
	Width      int
	Height     int
	Ms         int
}

// Detect runs the model on a JPEG.
func (s *Service) Detect(ctx context.Context, jpeg []byte) (Result, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", s.base()+"/detect", bytes.NewReader(jpeg))
	req.Header.Set("Content-Type", "image/jpeg")
	resp, err := s.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("AI service not answering: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		return Result{}, fmt.Errorf("AI service: %s", firstNonEmpty(e.Error, resp.Status))
	}
	return ParseResult(b)
}

// ParseResult reads ffai_server's (and Obico ml_api's) answer:
// {"detections": [[label, confidence, [xc, yc, w, h]], ...], "width", "height", "ms"}.
func ParseResult(b []byte) (Result, error) {
	var raw struct {
		Detections [][]any `json:"detections"`
		Width      float64 `json:"width"`
		Height     float64 `json:"height"`
		Ms         float64 `json:"ms"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return Result{}, err
	}
	r := Result{Width: int(raw.Width), Height: int(raw.Height), Ms: int(raw.Ms)}
	for _, d := range raw.Detections {
		if len(d) < 3 {
			continue
		}
		c, _ := d[1].(float64)
		box, _ := d[2].([]any)
		det := Detection{Label: fmt.Sprint(d[0]), Confidence: c}
		if len(box) >= 4 {
			f := func(v any) float64 { x, _ := v.(float64); return x }
			det.XC, det.YC, det.W, det.H = f(box[0]), f(box[1]), f(box[2]), f(box[3])
		}
		r.Detections = append(r.Detections, det)
	}
	return r, nil
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}
