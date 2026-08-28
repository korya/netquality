package engine

import (
	"math"
	"sync"
	"time"
)

// Observation is what the I/O layer reports to the engine once per interval.
type Observation struct {
	// Elapsed is the wall time since the previous observation (or phase start).
	Elapsed time.Duration
	// Bytes is the cumulative byte count of the phase so far.
	Bytes int64
	// Flows is the number of load-generating connections currently open.
	Flows int
	// Foreign and Self are the probe samples completed during this interval.
	Foreign, Self []LatencySample
}

// Decision is what the engine asks the I/O layer to do after an observation.
type Decision struct {
	// AddFlows is how many load-generating connections to open now.
	AddFlows int
	// Stop is true when both series are stable and the phase should end.
	Stop bool
	// Interval-level figures for progress reporting.
	Interval                               int
	ThroughputBPS                          float64 // moving-average goodput
	RPM                                    float64 // 0 until throughput is stable
	ThroughputStable, ResponsivenessStable bool
}

// Summary is the engine's final view of a phase; the I/O layer adds bytes,
// duration, protocol and error facts.
type Summary struct {
	Intervals                              int
	ThroughputBPS, PeakThroughputBPS       float64
	ThroughputStable, ResponsivenessStable bool
	ThroughputConfidence                   Confidence
	ResponsivenessConfidence               Confidence
	RPM, ForeignRPM, SelfRPM               float64
	Foreign, Self                          []LatencySample // samples the final figures were computed from
}

// Engine implements the draft's per-interval algorithm without any I/O or
// clock: feed it one Observation per interval and act on the Decision.
// It is safe for concurrent use of ProbeGap with Interval.
type Engine struct {
	p        StabilityParams
	maxFlows int

	mu            sync.Mutex
	tp, rp        *Tracker
	intervals     int
	lastBytes     int64
	peak          float64
	goodputStable bool
	foreign, self [][]LatencySample // per completed interval
	stopped       bool
}

// New returns an engine for one load phase.
func New(p StabilityParams, maxFlows int) *Engine {
	p = p.WithDefaults()
	return &Engine{
		p:        p,
		maxFlows: maxFlows,
		tp:       NewTracker(p.MovingAverageDistance, p.StdDevTolerance),
		rp:       NewTracker(p.MovingAverageDistance, p.StdDevTolerance),
	}
}

// InitialFlows is how many connections to open before the first interval.
func (e *Engine) InitialFlows() int {
	n := e.p.InitialFlows
	if n > e.maxFlows {
		n = e.maxFlows
	}
	return n
}

// Interval processes one completed interval and returns what to do next.
func (e *Engine) Interval(o Observation) Decision {
	e.mu.Lock()
	defer e.mu.Unlock()
	elapsed := o.Elapsed
	if elapsed <= 0 {
		elapsed = e.p.Interval
	}
	goodput := float64(o.Bytes-e.lastBytes) * 8 / elapsed.Seconds()
	e.lastBytes = o.Bytes
	if goodput > e.peak {
		e.peak = goodput
	}
	avg := e.tp.Push(goodput)
	e.intervals++
	e.foreign = append(e.foreign, o.Foreign)
	e.self = append(e.self, o.Self)

	d := Decision{Interval: e.intervals, ThroughputBPS: avg}
	if !e.goodputStable && e.tp.Stable() {
		e.goodputStable = true
	}
	d.ThroughputStable = e.goodputStable
	if e.goodputStable {
		f, s := e.window(e.p.MovingAverageDistance)
		cur, _, _ := Responsiveness(f, s, e.p.TrimmedMeanPercent)
		d.RPM = cur
		if cur > 0 {
			e.rp.Push(cur)
		}
		if e.rp.Stable() {
			d.ResponsivenessStable = true
			d.Stop = true
			e.stopped = true
			return d
		}
	}
	if o.Flows < e.maxFlows {
		d.AddFlows = e.p.FlowIncrement
		if o.Flows+d.AddFlows > e.maxFlows {
			d.AddFlows = e.maxFlows - o.Flows
		}
	}
	return d
}

// ProbeGap is the spacing between individual probes: 1/MPS, stretched so that
// probe traffic stays under PTC of the current goodput estimate.
func (e *Engine) ProbeGap(foreignBytes, selfBytes int) time.Duration {
	gap := time.Second / time.Duration(e.p.MaxProbesPerSecond)
	e.mu.Lock()
	bps := e.tp.Current()
	e.mu.Unlock()
	if bps > 0 {
		perProbe := float64(foreignBytes+selfBytes) / 2 * 8
		if g := time.Duration(perProbe / (e.p.ProbeCapacityPercent * bps) * float64(time.Second)); g > gap {
			gap = g
		}
	}
	return gap
}

// Summary computes the final figures. current holds samples of an interval
// that was in progress when the phase ended; they are used only when no
// interval completed at all.
func (e *Engine) Summary(currentForeign, currentSelf []LatencySample) Summary {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Summary{
		Intervals:                e.intervals,
		ThroughputBPS:            e.tp.Current(),
		PeakThroughputBPS:        e.peak,
		ThroughputStable:         e.tp.Stable(),
		ThroughputConfidence:     e.tp.Confidence(),
		ResponsivenessStable:     e.rp.Stable(),
		ResponsivenessConfidence: e.rp.Confidence(),
	}
	if !e.goodputStable {
		s.ResponsivenessConfidence = ConfidenceLow
	}
	f, sl := e.window(e.p.MovingAverageDistance)
	if len(f)+len(sl) == 0 {
		f, sl = e.window(len(e.foreign))
		f = append(f, currentForeign...)
		sl = append(sl, currentSelf...)
	}
	s.RPM, s.ForeignRPM, s.SelfRPM = Responsiveness(f, sl, e.p.TrimmedMeanPercent)
	s.Foreign, s.Self = f, sl
	return s
}

// window returns samples from the last n completed intervals.
func (e *Engine) window(n int) (foreign, self []LatencySample) {
	start := len(e.foreign) - n
	if start < 0 {
		start = 0
	}
	for i := start; i < len(e.foreign); i++ {
		foreign = append(foreign, e.foreign[i]...)
		self = append(self, e.self[i]...)
	}
	return
}

// Stopped reports whether Interval has returned Stop.
func (e *Engine) Stopped() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopped
}

var _ = math.Abs // keep math imported for future bound arithmetic
