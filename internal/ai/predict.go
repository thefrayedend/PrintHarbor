// Package ai is PrintHarbor's built-in print-failure detection: Obico's
// open-source model run by a small local service (ffai_server.py, from
// FlashBridge), and the same scoring Obico and FlashBridge use to decide
// when a print is failing.
package ai

// Obico 1st-gen hyper-parameters (obico-server backend/config/settings.py),
// as in FlashBridge's ai_monitor.dart.
const (
	EWMSpan                  = 12
	RollingWinShort          = 310
	RollingWinLong           = 7200
	ThresholdLow             = 0.38
	ThresholdHigh            = 0.78
	InitSafeFrameNum         = 30
	RollingMeanShortMultiple = 3.8
	EscalatingFactor         = 1.75
	VisualizationThresh      = 0.2
)

// Detection is one box the model found.
type Detection struct {
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
	XC         float64 `json:"xc"`
	YC         float64 `json:"yc"`
	W          float64 `json:"w"`
	H          float64 `json:"h"`
}

// Prediction smooths per-frame scores so one odd frame never alerts
// (Obico backend/lib/prediction.py).
type Prediction struct {
	CurrentP         float64 `json:"-"`
	CurrentFrameNum  int     `json:"-"`
	LifetimeFrameNum int     `json:"lifetimeFrameNum"`
	EWMMean          float64 `json:"-"`
	RollingMeanShort float64 `json:"-"`
	RollingMeanLong  float64 `json:"rollingMeanLong"`
}

// ResetForNewPrint keeps the long-term baseline, clears the rest.
func (p *Prediction) ResetForNewPrint() {
	p.CurrentP, p.CurrentFrameNum, p.EWMMean, p.RollingMeanShort = 0, 0, 0, 0
}

func rolling(p, mean float64, count, win int) float64 {
	n := count + 1
	if win <= count {
		n = win
	}
	return mean + (p-mean)/float64(n)
}

// Update adds one frame's detections.
func (p *Prediction) Update(dets []Detection) {
	sum := 0.0
	for _, d := range dets {
		sum += d.Confidence
	}
	alpha := 2.0 / float64(EWMSpan+1)
	p.CurrentP = sum
	p.CurrentFrameNum++
	p.LifetimeFrameNum++
	p.EWMMean = sum*alpha + p.EWMMean*(1-alpha)
	p.RollingMeanShort = rolling(sum, p.RollingMeanShort, p.CurrentFrameNum, RollingWinShort)
	p.RollingMeanLong = rolling(sum, p.RollingMeanLong, p.LifetimeFrameNum, RollingWinLong)
}

// IsFailing is Obico's is_failing().
func (p *Prediction) IsFailing(sensitivity, escalating float64) bool {
	if p.CurrentFrameNum < InitSafeFrameNum {
		return false
	}
	adjusted := (p.EWMMean - p.RollingMeanLong) * sensitivity / escalating
	if adjusted < ThresholdLow {
		return false
	}
	if adjusted > ThresholdHigh {
		return true
	}
	return adjusted > (p.RollingMeanShort-p.RollingMeanLong)*RollingMeanShortMultiple
}

// RiskPercent is 0..100 for the page (100 = at the warning threshold).
func (p *Prediction) RiskPercent() float64 {
	r := (p.EWMMean - p.RollingMeanLong) / ThresholdHigh * 100
	if r < 0 {
		return 0
	}
	if r > 100 {
		return 100
	}
	return r
}

// SensitivityFactor maps the setting to Obico's multiplier.
func SensitivityFactor(s string) float64 {
	switch s {
	case "low":
		return 0.8
	case "high":
		return 1.2
	}
	return 1.0
}
