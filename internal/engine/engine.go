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
	RPM                                    float64 // 0 until the ramp is done or throughput is stable
	ThroughputStable, ResponsivenessStable bool
	// Hold is true when no flow was added into this interval and it was not
	// a drain interval.
	Hold bool
	// Drain is true when the interval was excluded from measurement because
	// the send-buffer credit of newly opened flows would inflate it.
	Drain bool
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
	// LowerBoundBPS is the lowest goodput of the latest sustained window:
	// MovingAverageDistance consecutive measured (non-drain) intervals whose
	// goodputs are within StdDevTolerance of their mean — agreement across a
	// flow add shows the add changed nothing. 0 when no such window formed.
	// LowerBoundStart is the 1-based first interval of the window and
	// LowerBoundIntervals its length.
	LowerBoundBPS       float64
	LowerBoundStart     int
	LowerBoundIntervals int
	// RPMUpperBound is the responsiveness over the lower-bound window. The
	// queue may not have been full, so it bounds the loaded RPM from above.
	RPMUpperBound float64
	// WindowFrom is the 1-based first interval the RPM and the Foreign/Self
	// samples were taken from (through the last interval). PhaseForeign and
	// PhaseSelf count the samples of the whole phase, so a caller can tell a
	// series that never produced a sample from one whose samples all fell
	// outside the window.
	WindowFrom              int
	PhaseForeign, PhaseSelf int
}

// Engine implements the draft's per-interval algorithm without any I/O or
// clock: feed it one Observation per interval and act on the Decision.
// It is safe for concurrent use of ProbeGap with Interval.
//
// Deviations from the draft's ramp: flows are added in doubling steps and the
// ramp stops when a step adds less than RampGainTolerance of goodput; an
// interval inflated by send-buffer credit is excluded (see StabilityParams);
// a goodput drop beyond ChangeTolerance restarts goodput tracking.
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

	// ramp state
	addedInto   int     // flows added at the end of the previous interval
	awaitGain   bool    // an add happened; judge it on the next measured interval
	beforeAdd   float64 // goodput measured before the last add
	rampDone    bool
	drainCredit int64 // bytes of credit to absorb in the next interval
	// steadyFrom is the 1-based interval at which goodput became stable
	// (reset by a change): the start of the working-conditions window.
	steadyFrom int

	// measured intervals since the last reset, for the lower bound
	holdG   []float64
	holdIdx []int // 1-based interval numbers
	lb      Summary
}

// New returns an engine for one load phase.
func New(p StabilityParams, maxFlows int) *Engine {
	p = p.WithDefaults()
	e := &Engine{
		p:        p,
		maxFlows: maxFlows,
		tp:       NewTracker(p.MovingAverageDistance, p.StdDevTolerance),
		rp:       NewWindowedTracker(p.MovingAverageDistance, p.StdDevTolerance),
	}
	e.addedInto = e.InitialFlows()
	e.drainCredit = int64(e.addedInto) * p.SendBufferBytes
	return e
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
	bytes := o.Bytes - e.lastBytes
	goodput := float64(bytes) * 8 / elapsed.Seconds()
	e.lastBytes = o.Bytes
	e.intervals++
	e.foreign = append(e.foreign, o.Foreign)
	e.self = append(e.self, o.Self)

	d := Decision{Interval: e.intervals}
	// A drain interval: the credit handed to the transport for new flows is
	// a material part of what was counted, so nothing about it is trusted.
	if e.drainCredit > 0 && float64(e.drainCredit) > e.p.StdDevTolerance*float64(bytes) {
		d.Drain = true
	}
	e.drainCredit = 0
	d.Hold = !d.Drain && e.addedInto == 0
	e.addedInto = 0

	if !d.Drain {
		if goodput > e.peak {
			e.peak = goodput
		}
		// Change detection: a drop below the moving average beyond tolerance
		// restarts goodput tracking (the RPM series is left alone; its own
		// criterion catches a latency change).
		if d.Hold && e.tp.Intervals() >= e.p.MovingAverageDistance && goodput < (1-e.p.ChangeTolerance)*e.tp.Current() {
			e.tp = NewTracker(e.p.MovingAverageDistance, e.p.StdDevTolerance)
			e.goodputStable = false
			e.steadyFrom = 0
			e.holdG, e.holdIdx = nil, nil
			e.lb = Summary{}
		}
		e.tp.Push(goodput)
		e.holdG = append(e.holdG, goodput)
		e.holdIdx = append(e.holdIdx, e.intervals)
		e.updateBound()
	}
	d.ThroughputBPS = e.tp.Current()
	if !e.goodputStable && e.tp.Stable() {
		e.goodputStable = true
		if e.steadyFrom == 0 {
			e.steadyFrom = e.intervals
		}
	}
	d.ThroughputStable = e.goodputStable
	// Responsiveness is tracked under working conditions: once the ramp has
	// stopped (or goodput is stable, whichever comes first). The phase still
	// ends only when both series are stable.
	if e.goodputStable || e.rampDone {
		f, s := e.window(e.p.MovingAverageDistance)
		cur, _, _ := Responsiveness(f, s, e.p.TrimmedMeanPercent)
		d.RPM = cur
		if cur > 0 {
			e.rp.Push(cur)
		}
		d.ResponsivenessStable = e.rp.Stable()
		if e.goodputStable && d.ResponsivenessStable {
			d.Stop = true
			e.stopped = true
			return d
		}
	}

	// Ramp: double the flow count while a step still buys goodput. A drain
	// interval yields no measurement, so the decision waits for the next.
	if o.Flows >= e.maxFlows {
		e.rampDone = true
	}
	if d.Drain || e.rampDone {
		return d
	}
	if e.awaitGain {
		e.awaitGain = false
		if e.p.RampGainTolerance >= 0 && e.beforeAdd > 0 && (goodput-e.beforeAdd)/e.beforeAdd < e.p.RampGainTolerance {
			e.rampDone = true
			return d
		}
	}
	add := o.Flows
	if add < e.p.FlowIncrement {
		add = e.p.FlowIncrement
	}
	if o.Flows+add > e.maxFlows {
		add = e.maxFlows - o.Flows
	}
	d.AddFlows = add
	e.addedInto = add
	e.awaitGain = true
	e.beforeAdd = goodput
	e.drainCredit = int64(add) * e.p.SendBufferBytes
	return d
}

// updateBound refreshes the lower bound from the trailing measured
// intervals: the last MAD of them must be consecutive (no drain interval in
// between) with goodputs within StdDevTolerance of their mean; the bound is
// their minimum.
func (e *Engine) updateBound() {
	n := e.p.MovingAverageDistance
	if len(e.holdG) < n {
		return
	}
	g := e.holdG[len(e.holdG)-n:]
	idx := e.holdIdx[len(e.holdIdx)-n:]
	if idx[n-1]-idx[0] != n-1 {
		return
	}
	mean, low := 0.0, math.Inf(1)
	for _, v := range g {
		mean += v
		low = math.Min(low, v)
	}
	mean /= float64(n)
	if mean <= 0 || stddev(g) >= e.p.StdDevTolerance*mean {
		return
	}
	e.lb = Summary{LowerBoundBPS: low, LowerBoundStart: idx[0], LowerBoundIntervals: n}
	var f, s []LatencySample
	for i := idx[0] - 1; i < idx[n-1]; i++ {
		f = append(f, e.foreign[i]...)
		s = append(s, e.self[i]...)
	}
	e.lb.RPMUpperBound, _, _ = Responsiveness(f, s, e.p.TrimmedMeanPercent)
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
	// Working-conditions window: every sample since goodput became stable,
	// so a sparse series keeps all the samples that qualify instead of only
	// those of the last few intervals. Without stability there is no steady
	// state to point at, so the draft's trailing window stands.
	// It only ever widens the draft's window: the trailing MAD intervals are
	// the floor even when stability arrived on the final tick.
	from := max(e.intervals-e.p.MovingAverageDistance+1, 1)
	if e.goodputStable && e.steadyFrom > 0 && e.steadyFrom < from {
		from = e.steadyFrom
	}
	f, sl := e.window(e.intervals - from + 1)
	s.WindowFrom = from
	if len(f)+len(sl) == 0 {
		f, sl = e.window(len(e.foreign))
		f = append(f, currentForeign...)
		sl = append(sl, currentSelf...)
		s.WindowFrom = 1
	}
	s.RPM, s.ForeignRPM, s.SelfRPM = Responsiveness(f, sl, e.p.TrimmedMeanPercent)
	s.Foreign, s.Self = f, sl
	for i := range e.foreign {
		s.PhaseForeign += len(e.foreign[i])
		s.PhaseSelf += len(e.self[i])
	}
	s.PhaseForeign += len(currentForeign)
	s.PhaseSelf += len(currentSelf)
	s.LowerBoundBPS, s.LowerBoundStart, s.LowerBoundIntervals = e.lb.LowerBoundBPS, e.lb.LowerBoundStart, e.lb.LowerBoundIntervals
	s.RPMUpperBound = e.lb.RPMUpperBound
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
