// Package engine holds the pure, clock-free measurement logic of netquality:
// latency statistics, the draft's stability criterion, and (after extraction)
// the per-interval decisions. It performs no I/O so it can be driven by real
// transports or by a simulator.
package engine

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

func (p StabilityParams) WithDefaults() StabilityParams {
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

// Tracker implements the draft's moving-average stability criterion
// for a single series (goodput or responsiveness). It is deterministic and
// independent of wall time: callers push one value per interval.
type Tracker struct {
	mad       int
	tolerance float64
	raw       []float64 // per-interval values
	averages  []float64 // moving averages (one per interval once enough data)
}

func NewTracker(mad int, tolerance float64) *Tracker {
	return &Tracker{mad: mad, tolerance: tolerance}
}

// Push records the value for the current interval and returns the moving average
// over the last MAD intervals (partial window while warming up).
func (t *Tracker) Push(v float64) float64 {
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

// Current returns the most recent moving average.
func (t *Tracker) Current() float64 {
	if len(t.averages) == 0 {
		return 0
	}
	return t.averages[len(t.averages)-1]
}

// Intervals returns how many values have been pushed.
func (t *Tracker) Intervals() int { return len(t.raw) }

// Stable reports whether the standard deviation of the last MAD moving averages
// is within tolerance of the current moving average. Requires at least MAD
// intervals (draft: "Low" confidence otherwise).
func (t *Tracker) Stable() bool {
	if len(t.averages) < t.mad {
		return false
	}
	window := t.averages[len(t.averages)-t.mad:]
	cur := t.Current()
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

func (t *Tracker) Confidence() Confidence {
	switch {
	case t.Stable():
		return ConfidenceHigh
	case t.Intervals() >= t.mad:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}
