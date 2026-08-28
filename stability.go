package netquality

import "time"

// StabilityParams are the draft's algorithm parameters (Section 5.2).
type StabilityParams struct {
	// MovingAverageDistance (MAD): number of intervals in the moving average.
	MovingAverageDistance int
	// Interval (ID): how often stability is re-evaluated and flows are added.
	Interval time.Duration
	// TrimmedMeanPercent (TMP): single-sided trimmed-mean percentile for latency.
	TrimmedMeanPercent float64
	// StdDevTolerance (SDT): stability is declared when the standard deviation of
	// the last MAD moving averages is below this fraction of the current one.
	StdDevTolerance float64
	// InitialFlows (INP) and FlowIncrement (INC).
	InitialFlows  int
	FlowIncrement int
	// MaxProbesPerSecond (MPS) and ProbeCapacityPercent (PTC).
	MaxProbesPerSecond   int
	ProbeCapacityPercent float64
}

// DefaultStabilityParams returns the draft-09 defaults, except Interval, which
// is 1s instead of 5s so that a phase can stabilise within the 12s MaxDuration
// budget (see README "Deviations").
func DefaultStabilityParams() StabilityParams {
	return StabilityParams{
		MovingAverageDistance: 4,
		Interval:              time.Second,
		TrimmedMeanPercent:    95,
		StdDevTolerance:       0.05,
		InitialFlows:          1,
		FlowIncrement:         1,
		MaxProbesPerSecond:    100,
		ProbeCapacityPercent:  0.05,
	}
}

func (p StabilityParams) withDefaults() StabilityParams {
	d := DefaultStabilityParams()
	if p.MovingAverageDistance <= 0 {
		p.MovingAverageDistance = d.MovingAverageDistance
	}
	if p.Interval <= 0 {
		p.Interval = d.Interval
	}
	if p.TrimmedMeanPercent <= 0 || p.TrimmedMeanPercent > 100 {
		p.TrimmedMeanPercent = d.TrimmedMeanPercent
	}
	if p.StdDevTolerance <= 0 {
		p.StdDevTolerance = d.StdDevTolerance
	}
	if p.InitialFlows <= 0 {
		p.InitialFlows = d.InitialFlows
	}
	if p.FlowIncrement <= 0 {
		p.FlowIncrement = d.FlowIncrement
	}
	if p.MaxProbesPerSecond <= 0 {
		p.MaxProbesPerSecond = d.MaxProbesPerSecond
	}
	if p.ProbeCapacityPercent <= 0 {
		p.ProbeCapacityPercent = d.ProbeCapacityPercent
	}
	return p
}

// stabilityTracker implements the draft's moving-average stability criterion
// for a single series (goodput or responsiveness). It is deterministic and
// independent of wall time: callers push one value per interval.
type stabilityTracker struct {
	mad       int
	tolerance float64
	raw       []float64 // per-interval values
	averages  []float64 // moving averages (one per interval once enough data)
}

func newStabilityTracker(mad int, tolerance float64) *stabilityTracker {
	return &stabilityTracker{mad: mad, tolerance: tolerance}
}

// push records the value for the current interval and returns the moving average
// over the last MAD intervals (partial window while warming up).
func (t *stabilityTracker) push(v float64) float64 {
	t.raw = append(t.raw, v)
	start := len(t.raw) - t.mad
	if start < 0 {
		start = 0
	}
	var sum float64
	for _, x := range t.raw[start:] {
		sum += x
	}
	avg := sum / float64(len(t.raw)-start)
	t.averages = append(t.averages, avg)
	return avg
}

// current returns the most recent moving average.
func (t *stabilityTracker) current() float64 {
	if len(t.averages) == 0 {
		return 0
	}
	return t.averages[len(t.averages)-1]
}

// intervals returns how many values have been pushed.
func (t *stabilityTracker) intervals() int { return len(t.raw) }

// stable reports whether the standard deviation of the last MAD moving averages
// is within tolerance of the current moving average. Requires at least MAD
// intervals (draft: "Low" confidence otherwise).
func (t *stabilityTracker) stable() bool {
	if len(t.averages) < t.mad {
		return false
	}
	window := t.averages[len(t.averages)-t.mad:]
	cur := t.current()
	if cur <= 0 {
		return false
	}
	return stddev(window) < t.tolerance*cur
}

// Confidence is the draft's Section 5.4.1 confidence score.
type Confidence string

const (
	// ConfidenceLow: fewer than MAD intervals ran; moving average is partial.
	ConfidenceLow Confidence = "low"
	// ConfidenceMedium: at least MAD intervals ran but stability was not reached.
	ConfidenceMedium Confidence = "medium"
	// ConfidenceHigh: stability was reached.
	ConfidenceHigh Confidence = "high"
)

func (t *stabilityTracker) confidence() Confidence {
	switch {
	case t.stable():
		return ConfidenceHigh
	case t.intervals() >= t.mad:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}
