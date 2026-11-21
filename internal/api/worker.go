package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thefrayedend/printharbor/internal/slicer"
)

// Network workers: another PrintHarbor (say, on a gaming PC) slices for
// this one. The hub sends the model files and resolved presets; the worker
// runs its own Orca and sends back the G-code and sliced 3MF. If no worker
// answers, the hub slices locally as usual.

// workerSlicer implements slicer.Slicer.
type workerSlicer struct{ a *App }

func (w *workerSlicer) Slice(ctx context.Context, req slicer.Request) (slicer.Result, error) {
	a := w.a
	if len(a.cfg().Workers) > 0 && a.cfg().WorkerToken != "" {
		for _, wk := range a.cfg().Workers {
			if !a.workerAlive(ctx, wk) {
				continue
			}
			res, err := a.remoteSlice(ctx, wk, req)
			if err == nil {
				res.Log = "sliced on worker " + wk + "\n" + res.Log
				return res, nil
			}
			log.Printf("worker %s failed, trying the next one: %v", wk, err)
		}
	}
	return a.localSlicer.Slice(ctx, req)
}

func (a *App) workerAlive(ctx context.Context, base string) bool {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, "GET", strings.TrimRight(base, "/")+"/api/worker/ping", nil)
	req.Header.Set("X-Worker-Token", a.cfg().WorkerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var p struct {
		OK   bool `json:"ok"`
		Orca bool `json:"orca"`
	}
	return resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&p) == nil && p.OK && p.Orca
}

type workerJob struct {
	Plate     int              `json:"plate"`
	Machine   map[string]any   `json:"machine"`
	Process   map[string]any   `json:"process"`
	Filaments []map[string]any `json:"filaments"`
	Inputs    []string         `json:"inputs"` // file names, in order
}

func (a *App) remoteSlice(ctx context.Context, base string, req slicer.Request) (slicer.Result, error) {
	var res slicer.Result
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			job := workerJob{Plate: req.Plate, Machine: req.Machine, Process: req.Process, Filaments: req.Filaments}
			for _, in := range req.Inputs {
				job.Inputs = append(job.Inputs, filepath.Base(in))
			}
			j, _ := json.Marshal(job)
			if err := mw.WriteField("job", string(j)); err != nil {
				return err
			}
			for _, in := range req.Inputs {
				f, err := os.Open(in)
				if err != nil {
					return err
				}
				fw, err := mw.CreateFormFile("input", filepath.Base(in))
				if err == nil {
					_, err = io.Copy(fw, f)
				}
				f.Close()
				if err != nil {
					return err
				}
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()
	hreq, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/api/worker/slice", pr)
	hreq.Header.Set("Content-Type", mw.FormDataContentType())
	hreq.Header.Set("X-Worker-Token", a.cfg().WorkerToken)
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<30))
	if err != nil {
		return res, err
	}
	if resp.StatusCode != 200 {
		return res, fmt.Errorf("worker: %s %s", resp.Status, strings.TrimSpace(string(body[:min(len(body), 300)])))
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return res, err
	}
	out := filepath.Join(req.WorkDir, "out")
	_ = os.MkdirAll(out, 0o755)
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		rc, err := f.Open()
		if err != nil {
			return res, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return res, err
		}
		if name == "orca.log" {
			res.Log = string(b)
			continue
		}
		dst := filepath.Join(out, name)
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return res, err
		}
		switch {
		case strings.HasSuffix(name, ".gcode.3mf"):
			res.Sliced3MF = dst
		case strings.HasSuffix(name, ".gcode"):
			res.GCode = dst
		}
	}
	if res.GCode == "" && res.Sliced3MF == "" {
		return res, fmt.Errorf("worker returned no sliced files")
	}
	return res, nil
}

func (a *App) workerAuth(r *http.Request) bool {
	tok := a.cfg().WorkerToken
	return tok != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Worker-Token")), []byte(tok)) == 1
}

func (a *App) workerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/worker/ping", func(w http.ResponseWriter, r *http.Request) {
		if !a.workerAuth(r) {
			fail(w, 403, fmt.Errorf("worker key missing or wrong"))
			return
		}
		_, err := os.Stat(a.cfg().OrcaPath)
		writeJSON(w, 200, map[string]any{"ok": true, "orca": err == nil, "version": Version})
	})
	mux.HandleFunc("POST /api/worker/slice", func(w http.ResponseWriter, r *http.Request) {
		if !a.workerAuth(r) {
			fail(w, 403, fmt.Errorf("worker key missing or wrong"))
			return
		}
		work, err := os.MkdirTemp(a.Store.Dir(), "worker-*")
		if err != nil {
			fail(w, 500, err)
			return
		}
		defer os.RemoveAll(work)
		mr, err := r.MultipartReader()
		if err != nil {
			fail(w, 400, err)
			return
		}
		var job workerJob
		var inputs []string
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				fail(w, 400, err)
				return
			}
			switch part.FormName() {
			case "job":
				if err := json.NewDecoder(io.LimitReader(part, 16<<20)).Decode(&job); err != nil {
					fail(w, 400, err)
					return
				}
			case "input":
				name := filepath.Base(part.FileName())
				dst := filepath.Join(work, "in-"+name)
				f, err := os.Create(dst)
				if err != nil {
					fail(w, 500, err)
					return
				}
				_, err = io.Copy(f, part)
				f.Close()
				if err != nil {
					fail(w, 400, err)
					return
				}
				inputs = append(inputs, dst)
			}
		}
		if len(inputs) == 0 || job.Machine == nil {
			fail(w, 400, fmt.Errorf("incomplete worker job"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(a.cfg().SliceTimeoutMinutes)*time.Minute)
		defer cancel()
		res, err := a.localSlicer.Slice(ctx, slicer.Request{Inputs: inputs, Plate: job.Plate, Machine: job.Machine,
			Process: job.Process, Filaments: job.Filaments, WorkDir: filepath.Join(work, "job")})
		if err != nil {
			fail(w, 422, err)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		zw := zip.NewWriter(w)
		add := func(path, name string) {
			if path == "" {
				return
			}
			f, err := os.Open(path)
			if err != nil {
				return
			}
			defer f.Close()
			if zf, err := zw.Create(name); err == nil {
				io.Copy(zf, f)
			}
		}
		add(res.GCode, fmt.Sprintf("plate_%d.gcode", job.Plate))
		add(res.Sliced3MF, fmt.Sprintf("plate_%d.gcode.3mf", job.Plate))
		if zf, err := zw.Create("orca.log"); err == nil {
			io.WriteString(zf, res.Log)
		}
		zw.Close()
	})
}
