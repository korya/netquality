package netquality

import (
	"context"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

// observedResolution returns the smallest positive gap two successive readings
// of read can show — the granularity actually available to a caller. It gives
// up after budget so a pathologically coarse clock cannot hang the test.
func observedResolution(read func() time.Duration, budget time.Duration) (time.Duration, int) {
	best, samples := time.Duration(0), 0
	deadline := time.Now().Add(budget)
	for i := 0; i < 2_000_000 && time.Now().Before(deadline); i++ {
		if d := read(); d > 0 {
			samples++
			if best == 0 || d < best {
				best = d
			}
		}
	}
	return best, samples
}

// TestProbeClockResolves guards LAT-10: the clock probe timings are measured
// with must resolve finely enough to see a probe.
//
// The assertion is deliberately relative rather than an absolute bound in
// nanoseconds. An absolute bound would only swap one environment-sensitive
// implementation for an environment-sensitive test — the Windows tick is 0.5 to
// 15.625 ms depending on what else is running on the machine, and CI runners are
// not obliged to hold any of that still. What must hold on every platform is
// that the probe clock is no coarser than time.Now, and on Windows that it is
// strictly finer, which is exactly what fails if QueryPerformanceCounter is ever
// silently lost.
func TestProbeClockResolves(t *testing.T) {
	mono, monoN := observedResolution(func() time.Duration {
		a, b := monoNow(), monoNow()
		return b.sub(a)
	}, 200*time.Millisecond)
	wall, wallN := observedResolution(func() time.Duration {
		a, b := time.Now(), time.Now()
		return b.Sub(a)
	}, 200*time.Millisecond)

	// Telemetry: the numbers this platform actually delivers, recorded on every
	// CI run rather than frozen into a doc that would rot.
	t.Logf("probe clock: %v over %d positive samples (high resolution: %v)", mono, monoN, monoHighResolution())
	t.Logf("time.Now:    %v over %d positive samples", wall, wallN)

	if !monoHighResolution() {
		t.Skip("high-resolution timer unavailable; the fallback is time.Now by design")
	}
	if mono == 0 {
		t.Fatal("probe clock never advanced: no two readings differed")
	}
	if wall > 0 && mono > wall {
		t.Errorf("probe clock (%v) is coarser than time.Now (%v)", mono, wall)
	}
	// A probe is tens of microseconds; a clock that cannot resolve a
	// millisecond cannot measure one honestly.
	if mono >= time.Millisecond {
		t.Errorf("probe clock resolution %v cannot time a sub-millisecond probe", mono)
	}
}

// TestProbeClockMonotonic: readings never go backwards, so sub can never
// produce a negative duration.
func TestProbeClockMonotonic(t *testing.T) {
	prev := monoNow()
	for i := 0; i < 100_000; i++ {
		now := monoNow()
		if d := now.sub(prev); d < 0 {
			t.Fatalf("clock went backwards by %v at reading %d", d, i)
		}
		prev = now
	}
	if (instant{}).isZero() != true {
		t.Error("the zero instant must report itself zero")
	}
}

// TestCoarseClockWarns: when the high-resolution timer is unavailable the run
// still reports its numbers, and says how they were obtained (INV-3). The
// fallback is otherwise reachable only on a Windows box without QPC, so the
// clock seam stands in for one.
func TestCoarseClockWarns(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	fc := newFakeClock()
	fc.coarse = true
	go func() { // the phase loop is ticker-driven; keep it moving
		for i := 0; i < 8; i++ {
			time.Sleep(60 * time.Millisecond)
			fc.tick()
		}
	}()
	res, err := Run(context.Background(), target, Options{
		HTTPClient: client, Directions: Download, IdleProbes: 2,
		MaxDuration: 500 * time.Millisecond, MaxBytes: 1 << 40,
		Stability: fastStability(), clock: fc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(res, "high-resolution timer unavailable") {
		t.Errorf("a coarse clock must be reported: %v", res.Warnings)
	}
	if res.Idle == nil || res.Download == nil {
		t.Error("a coarse clock degrades the numbers; it must not remove them")
	}
}
