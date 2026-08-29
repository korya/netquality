package engine

import (
	"math"
	"testing"
	"time"
)

// linkModel feeds the engine a synthetic phase: goodput per interval is a
// function of the open flow count, and every interval carries probeSet
// samples. It returns the decisions in order.
func linkModel(t *testing.T, p StabilityParams, maxFlows, intervals int, goodput func(flows int) float64, credit int64) ([]Decision, *Engine) {
	t.Helper()
	e := New(p, maxFlows)
	flows := e.InitialFlows()
	bytes := int64(flows) * credit
	var out []Decision
	for i := 0; i < intervals; i++ {
		bytes += int64(goodput(flows) / 8)
		f, s := probeSet(i)
		d := e.Interval(Observation{Elapsed: time.Second, Bytes: bytes, Flows: flows, Foreign: f, Self: s})
		out = append(out, d)
		if d.Stop {
			break
		}
		flows += d.AddFlows
		bytes += int64(d.AddFlows) * credit
	}
	return out, e
}

func adds(ds []Decision) []int {
	var a []int
	for _, d := range ds {
		a = append(a, d.AddFlows)
	}
	return a
}

func TestRampDoublesUntilNoGain(t *testing.T) {
	// 100 Mbps per flow up to a 1 Gbps link: 1→2→4→8→16 then the cap.
	perFlow := func(flows int) float64 { return math.Min(float64(flows)*100e6, 1e9) }
	ds, _ := linkModel(t, StabilityParams{}, 16, 6, perFlow, 0)
	if got, want := adds(ds), []int{1, 2, 4, 8, 0, 0}; !ieq(got, want) {
		t.Errorf("adds=%v want %v", got, want)
	}
	// A link one flow saturates: one exploratory add, no gain, ramp done.
	flat := func(int) float64 { return 100e6 }
	ds, _ = linkModel(t, StabilityParams{}, 16, 4, flat, 0)
	if got, want := adds(ds), []int{1, 0, 0, 0}; !ieq(got, want) {
		t.Errorf("flat: adds=%v want %v", got, want)
	}
	if ds[1].Hold || !ds[2].Hold {
		t.Errorf("hold flags: interval 2 (after the add) %v, interval 3 %v", ds[1].Hold, ds[2].Hold)
	}
	// Negative tolerance: ramp to the cap regardless.
	ds, _ = linkModel(t, StabilityParams{RampGainTolerance: -1, StdDevTolerance: 1e-9}, 16, 6, flat, 0)
	if got, want := adds(ds), []int{1, 2, 4, 8, 0, 0}; !ieq(got, want) {
		t.Errorf("forced: adds=%v want %v", got, want)
	}
	// FlowIncrement is the floor of a step.
	ds, _ = linkModel(t, StabilityParams{FlowIncrement: 3, RampGainTolerance: -1, StdDevTolerance: 1e-9}, 16, 6, flat, 0)
	if got, want := adds(ds), []int{3, 4, 8, 0, 0, 0}; !ieq(got, want) {
		t.Errorf("increment floor: adds=%v want %v", got, want)
	}
}

func TestDrainIntervalExcludesSendBufferCredit(t *testing.T) {
	// 20 Mbps link, 4 MiB credited per flow when it opens.
	const credit = 4 << 20
	flat := func(int) float64 { return 20e6 }
	ds, e := linkModel(t, StabilityParams{SendBufferBytes: credit}, 16, 8, flat, credit)
	if !ds[0].Drain || ds[0].ThroughputBPS != 0 || ds[0].Hold {
		t.Errorf("interval 1 must be a drain interval: %+v", ds[0])
	}
	if !ds[2].Drain {
		t.Errorf("interval 3 (after the add) must drain: %+v", ds[2])
	}
	s := e.Summary(nil, nil)
	if s.ThroughputBPS != 20e6 || s.PeakThroughputBPS != 20e6 {
		t.Errorf("credit leaked into the figures: avg=%.0f peak=%.0f", s.ThroughputBPS, s.PeakThroughputBPS)
	}
	// Credit that is immaterial (1 Gbps link) does not cost an interval.
	fast := func(int) float64 { return 1e9 }
	ds, _ = linkModel(t, StabilityParams{SendBufferBytes: credit}, 16, 3, fast, credit)
	for i, d := range ds {
		if d.Drain {
			t.Errorf("interval %d drained on a fast link", i+1)
		}
	}
}

func TestLowerBoundFromSustainedWindow(t *testing.T) {
	flat := func(int) float64 { return 100e6 }
	ds, e := linkModel(t, StabilityParams{}, 16, 16, flat, 0) // probeSet settles by interval 9
	s := e.Summary(nil, nil)
	if s.LowerBoundBPS != 100e6 || s.LowerBoundIntervals != 4 {
		t.Fatalf("bound=%.0f over %d intervals from %d", s.LowerBoundBPS, s.LowerBoundIntervals, s.LowerBoundStart)
	}
	// The latest window ends at the last interval; a converged flat run has
	// one even though it stops MAD intervals after the ramp.
	if s.LowerBoundStart != len(ds)-3 {
		t.Errorf("window start %d, want %d (latest window)", s.LowerBoundStart, len(ds)-3)
	}
	if !ds[len(ds)-1].Stop {
		t.Errorf("flat run did not converge in %d intervals", len(ds))
	}
	// A drain interval breaks the window: 20 Mbps upload with 4 MiB credit
	// drains at 1 and 3, so the first window is 4..7.
	const credit = 4 << 20
	slow := func(int) float64 { return 20e6 }
	ds, e = linkModel(t, StabilityParams{SendBufferBytes: credit}, 16, 7, slow, credit)
	if s := e.Summary(nil, nil); s.LowerBoundStart != 4 || s.LowerBoundBPS != 20e6 {
		t.Errorf("upload window from %d (%.0f), want 4 (20e6); %d intervals", s.LowerBoundStart, s.LowerBoundBPS, len(ds))
	}
	if s.RPMUpperBound <= 0 || s.LowerBoundBPS > s.ThroughputBPS {
		t.Errorf("rpm upper bound %.0f, bound %.0f > estimate %.0f", s.RPMUpperBound, s.LowerBoundBPS, s.ThroughputBPS)
	}
	// A jittery series never sustains: no bound.
	i := 0
	jitter := func(int) float64 { i++; return 100e6 * (1 + 0.2*float64(i%2)) }
	_, e = linkModel(t, StabilityParams{StdDevTolerance: 0.01}, 16, 10, jitter, 0)
	if s := e.Summary(nil, nil); s.LowerBoundBPS != 0 {
		t.Errorf("bound %.0f from an unstable series", s.LowerBoundBPS)
	}
}

func TestCapacityDropRestartsGoodputTracking(t *testing.T) {
	n := 0
	step := func(int) float64 {
		n++
		if n <= 6 {
			return 200e6
		}
		return 100e6
	}
	ds, e := linkModel(t, StabilityParams{}, 16, 12, step, 0)
	if !ds[5].ThroughputStable {
		t.Fatalf("not stable before the drop: %+v", ds[5])
	}
	if ds[6].ThroughputStable || ds[6].ThroughputBPS != 100e6 {
		t.Errorf("drop not detected: %+v", ds[6])
	}
	s := e.Summary(nil, nil)
	if s.ThroughputBPS != 100e6 || s.ThroughputConfidence != ConfidenceHigh {
		t.Errorf("after the drop: %.0f %s", s.ThroughputBPS, s.ThroughputConfidence)
	}
	if s.LowerBoundBPS != 100e6 || s.LowerBoundStart < 7 {
		t.Errorf("bound %.0f from %d spans the drop", s.LowerBoundBPS, s.LowerBoundStart)
	}
}

func TestStopNeedsBothSeriesStable(t *testing.T) {
	// Goodput never stable (a 100/150/200 cycle the moving average cannot
	// flatten) while responsiveness settles: the phase must not stop.
	i := 0
	swing := func(int) float64 { i++; return 100e6 * (1 + 0.5*float64(i%3)) }
	ds, _ := linkModel(t, StabilityParams{}, 16, 15, swing, 0)
	for _, d := range ds {
		if d.Stop {
			t.Fatalf("stopped at %d with goodput unstable", d.Interval)
		}
	}
	if !ds[len(ds)-1].ResponsivenessStable {
		t.Errorf("responsiveness should have settled on its own")
	}
}

func ieq(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
