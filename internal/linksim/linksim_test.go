package linksim

import (
	"math"
	"testing"
	"time"

	"github.com/korya/netquality/internal/engine"
)

// These tests validate the model itself, independent of the algorithm: a
// wrong simulator would make the scenario matrix worthless.

func run(l Link) Outcome {
	return Run(l, engine.StabilityParams{}, 16, Budget{Duration: 12 * time.Second})
}

func within(got, want, pct float64) bool { return math.Abs(got-want)/want*100 <= pct }

func TestModelDeliversCapacity(t *testing.T) {
	for _, c := range []float64{5e6, 100e6, 1e9, 10e9} {
		o := run(Link{Capacity: c, RTT: 30 * time.Millisecond})
		rate := float64(o.Bytes) * 8 / o.Elapsed.Seconds()
		if o.Summary.ThroughputBPS > c*1.001 {
			t.Errorf("%.0f: estimate %.0f exceeds capacity", c, o.Summary.ThroughputBPS)
		}
		if !within(o.Summary.ThroughputBPS, c, 1) {
			t.Errorf("%.0f: converged estimate %.0f not at capacity", c, o.Summary.ThroughputBPS)
		}
		if rate > c*1.001 {
			t.Errorf("%.0f: mean delivered rate %.0f exceeds capacity", c, rate)
		}
	}
}

func TestModelQueueAddsLatency(t *testing.T) {
	c := 100e6
	o := run(Link{Capacity: c, RTT: 30 * time.Millisecond, QueueBytes: c / 8 * 0.3})
	if got := o.TrueLoadedRTT; !within(float64(got), float64(330*time.Millisecond), 2) {
		t.Errorf("loaded RTT = %v, want ~330ms", got)
	}
	o = run(Link{Capacity: c, RTT: 30 * time.Millisecond})
	if o.TrueLoadedRTT != 30*time.Millisecond {
		t.Errorf("no queue: loaded RTT = %v", o.TrueLoadedRTT)
	}
}

func TestModelShaperSustainsCapacity(t *testing.T) {
	c := 50e6
	o := run(Link{Capacity: c, RTT: 30 * time.Millisecond, ShaperBurst: 20 << 20})
	// Overall delivery may exceed capacity by at most the burst.
	excess := float64(o.Bytes) - c/8*o.Elapsed.Seconds()
	if excess > 20<<20+c/8*0.1 || excess < 0 {
		t.Errorf("shaper: excess bytes %.0f (burst 20 MiB)", excess)
	}
	var peak float64
	for _, ma := range o.MovingAverages {
		peak = math.Max(peak, ma)
	}
	if peak <= c*1.2 {
		t.Errorf("burst not visible: peak MA %.0f on a %.0f link", peak, c)
	}
	if last := o.MovingAverages[len(o.MovingAverages)-1]; !within(last, c, 5) {
		t.Errorf("burst did not settle to capacity: last MA %.0f", last)
	}
}

func TestModelSendBufferCredit(t *testing.T) {
	c := 20e6
	o := run(Link{Capacity: c, RTT: 40 * time.Millisecond, SendBuffer: 4 << 20})
	wire := c / 8 * o.Elapsed.Seconds()
	credited := float64(o.Bytes) - wire
	if credited < float64(o.Flows)*(4<<20)*0.9 {
		t.Errorf("credited %.0f bytes for %d flows, want ≈ flows × 4 MiB", credited, o.Flows)
	}
}

func TestModelCapacityChangeAndBudgets(t *testing.T) {
	o := run(Link{Capacity: 200e6, RTT: 30 * time.Millisecond, ChangeAt: 5 * time.Second, ChangeTo: 100e6})
	if o.TrueCapacity != 100e6 {
		t.Errorf("capacity after change = %.0f", o.TrueCapacity)
	}
	o = Run(Link{Capacity: 1e9, RTT: 20 * time.Millisecond}, engine.StabilityParams{}, 16, Budget{Duration: 12 * time.Second, Bytes: 250 << 20})
	if !o.Truncated || o.Reason != "bytes" || o.Bytes < 250<<20 {
		t.Errorf("byte budget: %+v", o)
	}
	o = Run(Link{Capacity: 1e9, RTT: 20 * time.Millisecond}, engine.StabilityParams{StdDevTolerance: 1e-9}, 16, Budget{Duration: 3 * time.Second})
	if !o.Truncated || o.Reason != "duration" || o.Elapsed > 3*time.Second+10*time.Millisecond {
		t.Errorf("duration budget: %+v", o)
	}
	seeded := Link{Capacity: 1e9, RTT: 20 * time.Millisecond, TickJitter: 200 * time.Millisecond, Seed: 1}
	a, b := run(seeded), run(seeded)
	if a.Bytes != b.Bytes || a.Elapsed != b.Elapsed {
		t.Error("same seed must be deterministic")
	}
}
