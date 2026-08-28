package netquality

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

func TestByteCounterLimits(t *testing.T) {
	var trips int
	c := &byteCounter{limit: 0, onLimit: func() { trips++ }}
	for i := 0; i < 1000; i++ {
		c.add(1 << 20)
	}
	if trips != 0 || c.get() != 1000<<20 {
		t.Errorf("unlimited counter tripped %d times, total %d", trips, c.get())
	}
	c = &byteCounter{limit: 10, onLimit: func() { trips++ }}
	c.add(4)
	c.add(5)
	if trips != 0 {
		t.Error("tripped below the limit")
	}
	c.add(1)
	c.add(100)
	if trips != 1 {
		t.Errorf("limit must trip exactly once, got %d", trips)
	}
}

// TestNoByteCapByDefault: a default-options run on loopback is bounded by
// time only, never reports bytes_cap in either direction, and moves more
// than the old 250 MiB cap — proving the cap is gone, not merely unreported.
func TestNoByteCapByDefault(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	for _, dir := range []Directions{Download, Upload} {
		t.Run(dir.String(), func(t *testing.T) {
			res, err := Run(context.Background(), target, Options{HTTPClient: client, Directions: dir, IdleProbes: -1,
				MaxDuration: 3 * time.Second, Stability: fastStability()})
			if err != nil {
				t.Fatal(err)
			}
			d := res.Download
			if dir == Upload {
				d = res.Upload
			}
			if d.Reason == ReasonBytesCap || hasWarning(res, "byte cap") {
				t.Errorf("default run must not be byte-capped: %+v %v", d, res.Warnings)
			}
			if d.Reason != ReasonNone && d.Reason != ReasonDurationCap {
				t.Errorf("reason = %q", d.Reason)
			}
			const oldCap = 250 << 20
			if d.Bytes <= oldCap {
				t.Errorf("%s moved only %s in 3 s; the old 250 MiB cap would not have been exceeded", dir, hB(d.Bytes))
			}
		})
	}
}

func hB(b int64) string { return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20)) }
