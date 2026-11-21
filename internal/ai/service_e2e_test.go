package ai

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestServiceE2E runs the real ffai_server.py when AI_E2E_DIR points at an
// installed folder (venv + a model). Skipped otherwise.
func TestServiceE2E(t *testing.T) {
	dir := os.Getenv("AI_E2E_DIR")
	if dir == "" {
		t.Skip("set AI_E2E_DIR")
	}
	s := New(dir, 7699)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	deadline := time.Now().Add(60 * time.Second)
	for {
		s.mu.Lock()
		s.healthAt = time.Time{}
		s.mu.Unlock()
		if h, ok := s.CheckHealth(ctx); ok && h.Ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("service never became ready: %+v", s.Status(ctx))
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, f := range []string{"red.jpg", "green.jpg"} {
		b, err := os.ReadFile(os.Getenv("AI_E2E_IMAGES") + "/" + f)
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.Detect(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d detections %+v %dx%d %dms", f, len(r.Detections), r.Detections, r.Width, r.Height, r.Ms)
		if f == "red.jpg" && (len(r.Detections) == 0 || r.Detections[0].Confidence < 0.5) {
			t.Fatal("red should look like a failure with the dummy model")
		}
		if f == "green.jpg" && len(r.Detections) != 0 {
			t.Fatal("green should be clean")
		}
	}
	cancel()
	time.Sleep(time.Second)
}
