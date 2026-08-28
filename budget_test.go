package netquality

import (
	"context"
	"fmt"
	"strings"
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

// TestNoByteCapByDefault: a default-options run on loopback (~15 Gbps) is
// bounded by time only and never reports bytes_cap.
func TestNoByteCapByDefault(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	res, err := Run(context.Background(), target, Options{HTTPClient: client, Directions: Download, IdleProbes: -1,
		MaxDuration: 1500 * time.Millisecond, Stability: fastStability()})
	if err != nil {
		t.Fatal(err)
	}
	d := res.Download
	if d.Reason == ReasonBytesCap || hasWarning(res, "byte cap") {
		t.Errorf("default run must not be byte-capped: %+v %v", d, res.Warnings)
	}
	if d.Bytes < 1<<30 {
		t.Logf("note: only %s moved in 1.5s on loopback", strings.TrimSpace(hB(d.Bytes)))
	}
	if d.Reason != ReasonNone && d.Reason != ReasonDurationCap {
		t.Errorf("reason = %q", d.Reason)
	}
}

func hB(b int64) string { return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20)) }
