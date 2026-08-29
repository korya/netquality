package netquality

import (
	"context"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

// TestLowerBoundInResult covers LOAD-13: both directions carry a lower bound
// with its window and the RPM upper bound, and the bound never exceeds the
// peak. A very wide tolerance keeps the window forming on a loopback whose
// goodput jitters wildly under -race; the algorithm itself is tested in
// internal/engine.
func TestLowerBoundInResult(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	p := fastStability()
	p.StdDevTolerance = 0.9
	var holds int
	res, err := RunWithEvents(context.Background(), target, Options{
		HTTPClient: client, Directions: Both, IdleProbes: -1, MaxFlows: 4,
		MaxDuration: 3 * time.Second, Stability: p,
	}, func(e Event) {
		if e.Kind == EventInterval && e.Hold {
			holds++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []*DirectionResult{res.Download, res.Upload} {
		if d.LowerBoundWindow == nil || d.ThroughputLowerBoundBPS <= 0 || d.RPMUpperBound <= 0 {
			t.Fatalf("%s: no bound: %+v", d.Direction, d)
		}
		w := d.LowerBoundWindow
		if w.Intervals != p.MovingAverageDistance || w.Duration != time.Duration(w.Intervals)*p.Interval || w.Start < 0 || w.Start+w.Duration > time.Duration(d.Intervals)*p.Interval {
			t.Errorf("%s: window %+v does not fit %d intervals of %s", d.Direction, w, d.Intervals, p.Interval)
		}
		if d.ThroughputLowerBoundBPS > d.PeakThroughputBPS {
			t.Errorf("%s: bound %.0f above the peak %.0f", d.Direction, d.ThroughputLowerBoundBPS, d.PeakThroughputBPS)
		}
	}
	if holds == 0 {
		t.Error("no interval event carried hold=true")
	}
}

// TestNoLowerBoundWhenCutShort: a run that ends before four hold intervals
// omits the bound rather than inventing one.
func TestNoLowerBoundWhenCutShort(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	p := fastStability()
	p.Interval = 300 * time.Millisecond
	res, err := Run(context.Background(), target, Options{
		HTTPClient: client, Directions: Download, IdleProbes: -1, MaxFlows: 2,
		MaxDuration: 700 * time.Millisecond, Stability: p,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := res.Download; d.LowerBoundWindow != nil || d.ThroughputLowerBoundBPS != 0 || d.RPMUpperBound != 0 {
		t.Errorf("bound reported from %d intervals: %+v", d.Intervals, d)
	}
}
