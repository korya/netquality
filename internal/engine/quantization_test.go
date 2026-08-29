package engine

import (
	"math/rand"
	"testing"
	"time"
)

// quantize models measuring a probe of true duration d on a clock whose
// readings only advance every tick. Start and end are quantised independently,
// so a probe shorter than a tick measures either 0 or a whole tick.
func quantize(rng *rand.Rand, d, tick time.Duration, n int) []time.Duration {
	out := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		phase := time.Duration(rng.Int63n(int64(tick)))
		out = append(out, ((phase+d)/tick)*tick)
	}
	return out
}

// TestCoarseClockDistortsStatistics is the reason clock_windows.go exists.
//
// Go's time.Now on Windows advances only at the system timer tick (0.5 to
// 15.625 ms). This pins what that does to a latency set, so that anyone
// tempted to delete the platform-specific clock as needless complexity can see
// the bill first. It is pure arithmetic: no clock, no network, no Windows.
func TestCoarseClockDistortsStatistics(t *testing.T) {
	const tick = 15625 * time.Microsecond // Windows default tick
	const n = 4000
	rng := rand.New(rand.NewSource(1))

	// A realistic broadband probe: 18.7ms with sub-millisecond jitter.
	base, spread := 18700*time.Microsecond, 900*time.Microsecond
	trueSamples := make([]LatencySample, 0, n)
	coarseSamples := make([]LatencySample, 0, n)
	for i := 0; i < n; i++ {
		d := base + time.Duration(rng.NormFloat64()*float64(spread))
		if d < 0 {
			d = 0
		}
		phase := time.Duration(rng.Int63n(int64(tick)))
		trueSamples = append(trueSamples, LatencySample{Total: d, HTTP: d})
		coarseSamples = append(coarseSamples, LatencySample{Total: ((phase + d) / tick) * tick, HTTP: ((phase + d) / tick) * tick})
	}
	exact, coarse := ComputeLatencyStats(trueSamples), ComputeLatencyStats(coarseSamples)

	// The mean survives: quantisation error is zero-mean, so averaging recovers
	// it. This is why RPM, which is built from means, is not the casualty.
	if ratio := float64(coarse.Mean) / float64(exact.Mean); ratio < 0.95 || ratio > 1.05 {
		t.Errorf("mean should survive quantisation: %v vs %v", coarse.Mean, exact.Mean)
	}
	// Jitter does not survive: it is built from deviations, and every deviation
	// is now a multiple of the tick. This is the headline number in nq's output.
	if coarse.Jitter < 4*exact.Jitter {
		t.Errorf("expected a coarse clock to inflate jitter far beyond %v, got %v", exact.Jitter, coarse.Jitter)
	}
	// Percentiles and the median snap to tick boundaries.
	if coarse.Median == exact.Median {
		t.Error("median should be quantised to a tick boundary")
	}
	if coarse.P95 <= exact.P95 {
		t.Errorf("p95 should be pushed to the next tick: %v vs %v", coarse.P95, exact.P95)
	}
}

// TestCoarseClockCollapsesSubTickRPM pins the second failure mode: a probe far
// shorter than the tick. trimmedMean discards everything above the 95th
// percentile, so when fewer than 5% of samples cross a tick boundary the cut
// lands on zero and the whole trimmed mean — and the RPM built from it —
// collapses. A self probe on a fast path is exactly this case.
func TestCoarseClockCollapsesSubTickRPM(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	const tick = time.Millisecond
	for _, tc := range []struct {
		d         time.Duration
		wantZero  bool
		tolerance float64 // when not zero, how close the trimmed mean must be
	}{
		{d: 23 * time.Microsecond, wantZero: true},   // 2% of a tick: collapses
		{d: 640 * time.Microsecond, tolerance: 0.05}, // 64% of a tick: fine
		{d: 18700 * time.Microsecond, tolerance: 0.02},
	} {
		got := trimmedMean(quantize(rng, tc.d, tick, 4000), 95)
		switch {
		case tc.wantZero && got != 0:
			t.Errorf("d=%v: expected the trimmed mean to collapse to 0, got %v", tc.d, got)
		case tc.wantZero && rpm(got) != 0:
			t.Errorf("d=%v: a collapsed mean must yield rpm 0, not infinity", tc.d)
		case !tc.wantZero:
			if ratio := float64(got) / float64(tc.d); ratio < 1-tc.tolerance || ratio > 1+tc.tolerance {
				t.Errorf("d=%v: trimmed mean %v is off by more than %.0f%%", tc.d, got, tc.tolerance*100)
			}
		}
	}
}
