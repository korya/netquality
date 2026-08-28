package netquality

import (
	"context"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

// TestPercentilesEndToEnd proves the presence rule through a real run, not
// just the stats function: the default five idle probes yield p80 only,
// twenty yield p95, and loaded sets probed at the default rate carry p99.
func TestPercentilesEndToEnd(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	// 200 probes/s split across two kinds and a 500 ms interval put ~200
	// samples of each kind in the four-interval window the final figures use
	// (the default 100/s lands just under the 100 needed for p99).
	p := DefaultStabilityParams()
	p.Interval = 500 * time.Millisecond
	p.MaxProbesPerSecond = 200
	p.StdDevTolerance = 1e-9 // never stop early: keep probing for the whole budget
	res, err := Run(context.Background(), target, Options{
		HTTPClient: client, Directions: Download, IdleProbes: DefaultIdleProbes, MaxFlows: 4,
		MaxDuration: 2500 * time.Millisecond, MaxBytes: 1 << 40, Stability: p,
	})
	if err != nil {
		t.Fatal(err)
	}
	idle := res.Idle
	if idle.Samples != 5 || idle.P80 == 0 || idle.P90 != 0 || idle.P95 != 0 || idle.P99 != 0 {
		t.Errorf("5 idle probes must yield p80 only: %+v", idle)
	}
	if idle.P80 > idle.Max || idle.P80 < idle.Median {
		t.Errorf("p80 out of order: median=%v p80=%v max=%v", idle.Median, idle.P80, idle.Max)
	}
	for name, st := range map[string]*LatencyStats{"foreign": res.Download.Loaded.Foreign, "self": res.Download.Loaded.Self, "combined": res.Download.Loaded.Combined} {
		if st == nil {
			t.Fatalf("%s: no samples", name)
		}
		if st.Samples < 100 {
			t.Errorf("%s: only %d samples in the window; the test needs ≥100 for p99", name, st.Samples)
			continue
		}
		if st.P80 == 0 || st.P90 == 0 || st.P95 == 0 || st.P99 == 0 {
			t.Errorf("%s (%d samples): all four percentiles must be present: %+v", name, st.Samples, st)
		}
		ordered := st.Min <= st.P80 && st.P80 <= st.P90 && st.P90 <= st.P95 && st.P95 <= st.P99 && st.P99 <= st.Max
		if !ordered {
			t.Errorf("%s: percentiles not monotonic: %+v", name, st)
		}
	}

	// Twenty idle probes: p95 appears, p99 does not.
	res, err = Run(context.Background(), target, Options{
		HTTPClient: client, Directions: Download, IdleProbes: 20,
		MaxDuration: 300 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if idle := res.Idle; idle.Samples != 20 || idle.P95 == 0 || idle.P99 != 0 {
		t.Errorf("20 idle probes must yield p95 but not p99: %+v", idle)
	}
}
