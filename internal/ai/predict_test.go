package ai

import "testing"

// A steady print never alerts; spaghetti after the warm-up does, and the
// pause signal (escalating factor) comes after the warning.
func TestPrediction(t *testing.T) {
	var p Prediction
	for i := 0; i < 200; i++ {
		p.Update([]Detection{{Confidence: 0.05}})
		if p.IsFailing(1, 1) {
			t.Fatalf("steady low scores alerted at frame %d", i)
		}
	}
	p.ResetForNewPrint()
	warnAt, pauseAt := -1, -1
	for i := 0; i < 80; i++ {
		c := 0.0
		if i >= 35 {
			c = 1.2
		}
		p.Update([]Detection{{Confidence: c}})
		if warnAt < 0 && p.IsFailing(1, 1) {
			warnAt = i
		}
		if pauseAt < 0 && p.IsFailing(1, EscalatingFactor) {
			pauseAt = i
		}
	}
	if warnAt < 35 || pauseAt < warnAt {
		t.Fatalf("warn %d pause %d", warnAt, pauseAt)
	}
	if SensitivityFactor("high") <= SensitivityFactor("medium") || SensitivityFactor("low") >= 1 {
		t.Fatal("sensitivity")
	}
}

func TestParseResult(t *testing.T) {
	r, err := ParseResult([]byte(`{"detections":[["failure",0.82,[100,120,40,30]]],"width":640,"height":480,"ms":95}`))
	if err != nil || len(r.Detections) != 1 || r.Detections[0].Confidence != 0.82 || r.Detections[0].W != 40 || r.Width != 640 || r.Ms != 95 {
		t.Fatalf("%+v %v", r, err)
	}
}
