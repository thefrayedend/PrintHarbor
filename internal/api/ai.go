package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thefrayedend/printharbor/internal/ai"
	"github.com/thefrayedend/printharbor/internal/printers"
)

// Built-in AI print-failure detection, ported from FlashBridge's
// ai_monitor.dart. While a printer prints, a camera frame goes to the local
// AI service (Obico's open-source model) every few seconds. Scores are
// smoothed with Obico's own algorithm so one odd frame never alerts. A
// warning alerts (page, Discord); with "pause" chosen, a stronger signal
// pauses the print. Everything is set per printer.

// aiPrinter is one printer's detection state.
type aiPrinter struct {
	pred       ai.Prediction
	status     string // off | idle | watching | warning | paused | ignored | no-camera | ai-offline | not-installed
	message    string
	jobFile    string
	warned     bool
	pausedByAI bool
	ignored    bool
	lastCheck  time.Time
	ms         int
	image      []byte
	w, h       int
	dets       []ai.Detection
	history    []float64
	nextDue    time.Time
	busy       bool
	lastState  printers.State
}

// AIState is what the page shows for a printer.
type AIState struct {
	Status       string         `json:"status"`
	Message      string         `json:"message"`
	JobFile      string         `json:"job_file,omitempty"`
	Risk         float64        `json:"risk"`
	P            float64        `json:"p"`
	EWM          float64        `json:"ewm"`
	Frames       int            `json:"frames"`
	WarmupFrames int            `json:"warmup_frames"`
	Warned       bool           `json:"warned"`
	PausedByAI   bool           `json:"paused_by_ai"`
	Ignored      bool           `json:"ignored"`
	LastCheck    *time.Time     `json:"last_check,omitempty"`
	InferenceMs  int            `json:"inference_ms"`
	HasImage     bool           `json:"has_image"`
	ImageWidth   int            `json:"image_width"`
	ImageHeight  int            `json:"image_height"`
	Detections   []ai.Detection `json:"detections"`
	History      []float64      `json:"history"`
}

type aiMonitor struct {
	mu     sync.Mutex
	states map[string]*aiPrinter
}

func (a *App) aiFor(id string) *aiPrinter {
	s := a.aim.states[id]
	if s == nil {
		s = &aiPrinter{status: "idle", lastState: printers.StateIdle}
		a.loadAILongTerm(id, s)
		a.aim.states[id] = s
	}
	return s
}

func (a *App) aiStateFile(id string) string {
	return filepath.Join(a.cfg().DataDir, "ai", "state", id+".json")
}

func (a *App) loadAILongTerm(id string, s *aiPrinter) {
	if b, err := os.ReadFile(a.aiStateFile(id)); err == nil {
		_ = json.Unmarshal(b, &s.pred)
	}
}

func (a *App) saveAILongTerm(id string, s *aiPrinter) {
	b, _ := json.Marshal(&s.pred)
	_ = os.MkdirAll(filepath.Dir(a.aiStateFile(id)), 0o755)
	_ = os.WriteFile(a.aiStateFile(id), b, 0o644)
}

// aiUsesService is true when the built-in service does the detecting (not
// an external Obico ML server).
func (a *App) aiUsesService() bool { return strings.TrimSpace(a.cfg().ObicoMLURL) == "" }

// aiAvailable reports whether detection can run at all.
func (a *App) aiAvailable() bool { return !a.aiUsesService() || a.AI.Installed() }

// aiLoop runs every second, like FlashBridge's _onTick, and starts the AI
// service when a printer has detection switched on.
func (a *App) aiLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.aiTick(ctx)
	}
}

func (a *App) aiTick(ctx context.Context) {
	now := time.Now()
	list := a.Printers.List()
	anyOn := false
	a.aim.mu.Lock()
	defer a.aim.mu.Unlock()
	seen := map[string]bool{}
	for _, v := range list {
		seen[v.ID] = true
		s := a.aiFor(v.ID)
		a.aiTrackJob(v, s)
		if !v.AIDetect {
			s.status, s.message = "off", "Failure detection is switched off for this printer."
			continue
		}
		anyOn = true
		st := v.Status.State
		if st != printers.StatePrinting {
			if st == printers.StatePaused && s.pausedByAI {
				s.status, s.message = "paused", "Paused by AI. Check the print, then resume or cancel it."
			} else {
				s.status = "idle"
				switch {
				case st == printers.StateOffline:
					s.message = "Printer is offline."
				case st == printers.StatePaused:
					s.message = "Print is paused."
				default:
					s.message = "Waiting for a print to start."
				}
			}
			continue
		}
		if !a.aiAvailable() {
			s.status, s.message = "not-installed", "Install AI failure detection in Settings to start watching."
			continue
		}
		if v.Status.SnapshotURL == "" {
			s.status, s.message = "no-camera", "No camera found for this printer."
			continue
		}
		if s.busy || now.Before(s.nextDue) {
			continue
		}
		iv := v.AIInterval
		if iv < 5 || iv > 120 {
			iv = 10
		}
		s.nextDue = now.Add(time.Duration(iv) * time.Second)
		s.busy = true
		go func(v printers.View, s *aiPrinter) {
			a.aiCheck(ctx, v, s)
			a.aim.mu.Lock()
			s.busy = false
			a.aim.mu.Unlock()
		}(v, s)
	}
	for id := range a.aim.states {
		if !seen[id] {
			delete(a.aim.states, id)
		}
	}
	if anyOn && a.aiUsesService() && a.AI.Installed() {
		go a.AI.Run(ctx) // no-op when already running
	}
}

// aiTrackJob resets per-print state when a new print starts, and gives a
// fresh warm-up after the user resumes a print the AI paused.
func (a *App) aiTrackJob(v printers.View, s *aiPrinter) {
	st := v.Status.State
	active := st == printers.StatePrinting || st == printers.StatePaused
	wasActive := s.lastState == printers.StatePrinting || s.lastState == printers.StatePaused
	if active && (s.jobFile != v.Status.JobName || !wasActive) {
		s.pred.ResetForNewPrint()
		s.jobFile = v.Status.JobName
		s.warned, s.pausedByAI, s.ignored = false, false, false
		s.history = nil
	} else if st == printers.StatePrinting && s.lastState == printers.StatePaused && s.pausedByAI {
		s.pred.ResetForNewPrint()
		s.pausedByAI, s.warned = false, false
	}
	if !active && s.lastState != st {
		a.saveAILongTerm(v.ID, s)
	}
	s.lastState = st
}

// aiDetect analyses one frame with the built-in service or Obico's ML API.
func (a *App) aiDetect(ctx context.Context, printerID string, jpeg []byte) (ai.Result, error) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if a.aiUsesService() {
		return a.AI.Detect(cctx, jpeg)
	}
	ml := strings.TrimRight(a.cfg().ObicoMLURL, "/")
	img := a.selfURL() + "/api/printers/" + printerID + "/snapshot"
	req, _ := http.NewRequestWithContext(cctx, "GET", ml+"/p/?img="+url.QueryEscape(img), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ai.Result{}, fmt.Errorf("AI service not answering: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return ai.Result{}, fmt.Errorf("AI service: %s", resp.Status)
	}
	return ai.ParseResult(b)
}

// aiRunOnce takes a picture and analyses it (also the "Test now" button).
func (a *App) aiRunOnce(ctx context.Context, id string, update bool) (*aiPrinter, error) {
	jpeg, _, err := a.Printers.Snapshot(ctx, id)
	if err != nil || len(jpeg) == 0 {
		return nil, fmt.Errorf("couldn't get a camera picture: %v", err)
	}
	r, err := a.aiDetect(ctx, id, jpeg)
	if err != nil {
		return nil, err
	}
	a.aim.mu.Lock()
	defer a.aim.mu.Unlock()
	s := a.aiFor(id)
	s.image, s.dets, s.w, s.h, s.ms, s.lastCheck = jpeg, r.Detections, r.Width, r.Height, r.Ms, time.Now()
	if update {
		s.pred.Update(r.Detections)
		s.history = append(s.history, math.Round(s.pred.EWMMean*1000)/1000)
		if len(s.history) > 360 {
			s.history = s.history[len(s.history)-360:]
		}
	}
	return s, nil
}

func (a *App) aiCheck(ctx context.Context, v printers.View, s *aiPrinter) {
	if _, err := a.aiRunOnce(ctx, v.ID, true); err != nil {
		a.aim.mu.Lock()
		if strings.Contains(err.Error(), "AI service") {
			s.status, s.message = "ai-offline", "The AI service is not answering yet (it can take a minute to start)."
		} else {
			s.status, s.message = "watching", "Camera problem: "+err.Error()
		}
		a.aim.mu.Unlock()
		return
	}
	if cur, ok := a.Printers.Get(v.ID); !ok || cur.Status.State != printers.StatePrinting {
		return // the print changed while we were looking
	}
	sens := ai.SensitivityFactor(v.AISensitivity)
	a.aim.mu.Lock()
	warmingUp := s.pred.CurrentFrameNum < ai.InitSafeFrameNum
	if s.ignored {
		s.status, s.message = "ignored", `Alerts muted for this print ("Not a failure" was pressed).`
		a.aim.mu.Unlock()
		return
	}
	if v.AIPause && !s.pausedByAI && s.pred.IsFailing(sens, ai.EscalatingFactor) {
		s.pausedByAI, s.warned = true, true
		s.status, s.message = "paused", "Paused by AI. Check the print, then resume or cancel it."
		img, score := s.image, s.pred.EWMMean
		a.aim.mu.Unlock()
		err := a.Printers.Action(ctx, v.ID, "pause")
		msg := fmt.Sprintf("AI failure detection: %s looks like it's failing (score %.2f). ", v.Status.JobName, score)
		if err == nil {
			msg += "The print was PAUSED. Check it, then resume or cancel."
		} else {
			msg += "Pausing FAILED, check the printer! (" + err.Error() + ")"
		}
		log.Printf("AI: likely failure on %s: %s", v.Name, msg)
		a.Alert(v.ID, "error", msg, img)
		a.emitEvent("print_failed", v.ID, map[string]any{
			"reason": "AI failure detection", "job_name": v.Status.JobName, "score": score, "paused": err == nil})
		return
	}
	if !s.warned && s.pred.IsFailing(sens, 1) {
		s.warned = true
		s.status, s.message = "warning", "Possible print failure detected. Please check the printer."
		img, score := s.image, s.pred.EWMMean
		a.aim.mu.Unlock()
		log.Printf("AI: possible failure on %s (ewm %.2f)", v.Name, score)
		a.Alert(v.ID, "warning", fmt.Sprintf("AI failure detection: possible print failure (spaghetti) on %s (score %.2f). Please check the printer.", v.Status.JobName, score), img)
		return
	}
	if s.warned {
		s.status, s.message = "warning", "Possible print failure detected. Please check the printer."
	} else {
		s.status = "watching"
		if warmingUp {
			s.message = fmt.Sprintf("Watching. Learning what a good print looks like (%d/%d).", s.pred.CurrentFrameNum, ai.InitSafeFrameNum)
		} else {
			s.message = "Watching. Everything looks fine."
		}
	}
	a.aim.mu.Unlock()
}

func (s *aiPrinter) view() AIState {
	out := AIState{Status: s.status, Message: s.message, JobFile: s.jobFile, P: s.pred.CurrentP, EWM: s.pred.EWMMean,
		Frames: s.pred.CurrentFrameNum, WarmupFrames: ai.InitSafeFrameNum, Warned: s.warned, PausedByAI: s.pausedByAI,
		Ignored: s.ignored, InferenceMs: s.ms, HasImage: s.image != nil, ImageWidth: s.w, ImageHeight: s.h,
		Detections: []ai.Detection{}, History: append([]float64{}, s.history...)}
	switch s.status {
	case "watching", "warning", "paused", "ignored":
		out.Risk = math.Round(s.pred.RiskPercent())
	}
	if !s.lastCheck.IsZero() {
		t := s.lastCheck
		out.LastCheck = &t
	}
	for _, d := range s.dets {
		if d.Confidence >= ai.VisualizationThresh {
			out.Detections = append(out.Detections, d)
		}
	}
	return out
}

// AIStates is every printer's detection state.
func (a *App) AIStates() map[string]AIState {
	a.aim.mu.Lock()
	defer a.aim.mu.Unlock()
	out := map[string]AIState{}
	for id, s := range a.aim.states {
		out[id] = s.view()
	}
	return out
}

// AIScore is the smoothed score (for the assistant).
func (a *App) AIScore(id string) float64 {
	a.aim.mu.Lock()
	defer a.aim.mu.Unlock()
	if s := a.aim.states[id]; s != nil {
		return s.pred.EWMMean
	}
	return 0
}

func (a *App) aiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/ai", func(w http.ResponseWriter, r *http.Request) {
		st := a.AI.Status(r.Context())
		writeJSON(w, 200, map[string]any{"service": st, "external": !a.aiUsesService(), "printers": a.AIStates()})
	})
	mux.HandleFunc("POST /api/ai/install", func(w http.ResponseWriter, r *http.Request) {
		err := a.AI.Install(func() {
			ctx := a.runCtxOr()
			if ctx == nil {
				ctx = context.Background()
			}
			go a.AI.Run(ctx)
		})
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/ai/{id}/ignore", func(w http.ResponseWriter, r *http.Request) {
		a.aim.mu.Lock()
		s := a.aiFor(r.PathValue("id"))
		s.ignored, s.warned = true, false
		s.status, s.message = "ignored", `Alerts muted for this print ("Not a failure" was pressed).`
		a.aim.mu.Unlock()
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/ai/{id}/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.aiAvailable() {
			fail(w, 400, fmt.Errorf("install AI failure detection first"))
			return
		}
		if a.aiUsesService() {
			if h, ok := a.AI.CheckHealth(r.Context()); !ok || !h.Ready {
				go a.AI.Run(a.runCtxOr())
				fail(w, 503, fmt.Errorf("the AI service is starting; try again in a minute"))
				return
			}
		}
		s, err := a.aiRunOnce(r.Context(), r.PathValue("id"), false)
		if err != nil {
			fail(w, 400, err)
			return
		}
		a.aim.mu.Lock()
		v := s.view()
		a.aim.mu.Unlock()
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("GET /api/ai/{id}/image", func(w http.ResponseWriter, r *http.Request) {
		a.aim.mu.Lock()
		var img []byte
		if s := a.aim.states[r.PathValue("id")]; s != nil {
			img = s.image
		}
		a.aim.mu.Unlock()
		if img == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(img)
	})
}

func (a *App) runCtxOr() context.Context {
	a.cfgMu.RLock()
	ctx := a.runCtx
	a.cfgMu.RUnlock()
	if ctx != nil {
		return ctx
	}
	return context.Background()
}
