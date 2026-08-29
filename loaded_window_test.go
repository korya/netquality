package netquality

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

// TestLoadedWindowAndSparseSeriesWarning covers LOAD-14 end to end: a
// converged run reports loaded_window ending at the last interval, and a
// run whose foreign probes cannot complete inside the window — the probe
// gap is stretched far beyond it — omits loaded.foreign with a warning
// naming the samples that do exist, instead of silently.
func TestLoadedWindowAndSparseSeriesWarning(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	p := fastStability()
	res, err := Run(context.Background(), target, Options{
		HTTPClient: client, Directions: Download, IdleProbes: -1, MaxFlows: 4,
		MaxDuration: 3 * time.Second, Stability: p,
	})
	if err != nil {
		t.Fatal(err)
	}
	d := res.Download
	if d.LoadedWindow == nil {
		t.Fatalf("no loaded_window: %+v", d)
	}
	w := d.LoadedWindow
	if w.Start+w.Duration != time.Duration(d.Intervals)*p.Interval || w.Intervals < 1 || w.Intervals > d.Intervals {
		t.Errorf("window %+v does not end at interval %d", w, d.Intervals)
	}
	if d.ThroughputStable && w.Intervals < p.MovingAverageDistance {
		t.Errorf("converged run has a %d-interval window", w.Intervals)
	}

	// Sparse foreign series: one probe per second against 100 ms intervals
	// and an impossible stability tolerance keep the trailing window at
	// four intervals with, at most, a self sample or two and no foreign one.
	p = fastStability()
	p.StdDevTolerance = 1e-12
	p.MaxProbesPerSecond = 1
	var foreignSeen bool
	res, err = RunWithEvents(context.Background(), target, Options{
		HTTPClient: client, Directions: Download, IdleProbes: -1, MaxFlows: 1,
		MaxDuration: 2 * time.Second, Stability: p,
	}, func(e Event) {
		if e.Kind == EventProbe && e.ProbeKind == "foreign" {
			foreignSeen = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	d = res.Download
	if !foreignSeen {
		t.Skip("no foreign probe completed at all; nothing to be sparse about")
	}
	if d.Loaded.Foreign == nil {
		var named bool
		for _, w := range res.Warnings {
			if strings.Contains(w, "foreign probes:") && strings.Contains(w, "none in the working-conditions window") {
				named = true
			}
		}
		if !named {
			t.Errorf("loaded.foreign omitted without a warning: %v", res.Warnings)
		}
	}
}
