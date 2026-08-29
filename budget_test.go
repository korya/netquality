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
// time only and never reports bytes_cap in either direction. Proving the cap
// is really gone cannot use an absolute byte floor (CI loopback under -race
// runs at a few hundred Mbps), so it is relative: the same run with a small
// explicit cap must move far less than the uncapped one. The uncapped run is
// kept from converging so that it spends the whole budget; otherwise a
// loopback that stabilises in a few intervals moves too little to compare.
func TestNoByteCapByDefault(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	const smallCap = 16 << 20 // small enough that even a slow CI loopback dwarfs it
	for _, dir := range []Directions{Download, Upload} {
		t.Run(dir.String(), func(t *testing.T) {
			run := func(maxBytes int64) *DirectionResult {
				p := fastStability()
				p.StdDevTolerance = 1e-9 // never stable: time is the only limit
				res, err := Run(context.Background(), target, Options{HTTPClient: client, Directions: dir, IdleProbes: -1,
					MaxDuration: 2 * time.Second, MaxBytes: maxBytes, Stability: p})
				if err != nil {
					t.Fatal(err)
				}
				if dir == Upload {
					return res.Upload
				}
				return res.Download
			}
			free := run(0)
			if free.Reason == ReasonBytesCap {
				t.Errorf("default run must not be byte-capped: %+v", free)
			}
			if free.Reason != ReasonNone && free.Reason != ReasonDurationCap {
				t.Errorf("reason = %q", free.Reason)
			}
			capped := run(smallCap)
			if capped.Reason != ReasonBytesCap {
				t.Errorf("explicit cap must still bite: %+v", capped)
			}
			if free.Bytes < 2*capped.Bytes {
				t.Errorf("%s: uncapped run moved %s, capped %s — the default is not unlimited", dir, hB(free.Bytes), hB(capped.Bytes))
			}
		})
	}
}

func hB(b int64) string { return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20)) }
