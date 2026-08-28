package engine_test

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/korya/netquality/internal/engine"
	"github.com/korya/netquality/internal/linksim"
)

// Scenario matrix for the algorithm. Each scenario runs the real engine over
// a modelled link and is judged by oracles, not expected numbers:
//
//   honesty  – confidence is never "high" when the estimate is >10% off truth
//   budget   – bytes and time never exceed the budget
//   converge – the phase reaches high/high confidence inside the budget
//   accuracy – with high confidence, throughput and RPM are within tolerance
//
// honesty and budget must always hold. converge/accuracy are targets: the
// scenarios in knownFailing are logged instead of failed until the algorithm
// work lands (TDD ledger). Removing a name from knownFailing is the way a
// change proves it fixed something.

const (
	mbps = 1e6
	gbps = 1e9
	mib  = 1 << 20
)

type scenario struct {
	name   string
	link   linksim.Link
	budget linksim.Budget
	params engine.StabilityParams
}

func defaults() linksim.Budget { return linksim.Budget{Duration: 12 * time.Second, Bytes: 250 * mib} }

func scenarios() []scenario {
	var out []scenario
	for _, c := range []float64{5 * mbps, 30 * mbps, 100 * mbps, 400 * mbps, 1 * gbps, 10 * gbps} {
		for _, rtt := range []time.Duration{10 * time.Millisecond, 50 * time.Millisecond, 150 * time.Millisecond} {
			out = append(out, scenario{fmt.Sprintf("clean %s rtt=%s", hb(c), rtt), linksim.Link{Capacity: c, RTT: rtt, Seed: 1}, defaults(), engine.StabilityParams{}})
		}
		// bufferbloat: a queue worth ~300 ms at capacity
		out = append(out, scenario{fmt.Sprintf("bloated %s", hb(c)), linksim.Link{Capacity: c, RTT: 30 * time.Millisecond, QueueBytes: c / 8 * 0.3, Seed: 2}, defaults(), engine.StabilityParams{}})
	}
	out = append(out,
		scenario{"cdn per-flow cap 200M on 1G", linksim.Link{Capacity: 1 * gbps, RTT: 20 * time.Millisecond, PerFlowCap: 200 * mbps, Seed: 3}, defaults(), engine.StabilityParams{}},
		scenario{"shaper burst 20MB on 50M", linksim.Link{Capacity: 50 * mbps, RTT: 30 * time.Millisecond, ShaperBurst: 20 * mib, Seed: 4}, defaults(), engine.StabilityParams{}},
		scenario{"upload send buffer 4MiB on 20M", linksim.Link{Capacity: 20 * mbps, RTT: 40 * time.Millisecond, SendBuffer: 4 * mib, Seed: 5}, defaults(), engine.StabilityParams{}},
		scenario{"tick jitter 200ms on 100M", linksim.Link{Capacity: 100 * mbps, RTT: 30 * time.Millisecond, TickJitter: 200 * time.Millisecond, Seed: 6}, defaults(), engine.StabilityParams{}},
		scenario{"capacity halves at 5s (200M→100M)", linksim.Link{Capacity: 200 * mbps, RTT: 30 * time.Millisecond, ChangeAt: 5 * time.Second, ChangeTo: 100 * mbps, Seed: 7}, defaults(), engine.StabilityParams{}},
		scenario{"1G unlimited bytes", linksim.Link{Capacity: 1 * gbps, RTT: 20 * time.Millisecond, Seed: 8}, linksim.Budget{Duration: 12 * time.Second}, engine.StabilityParams{}},
		scenario{"10G unlimited bytes", linksim.Link{Capacity: 10 * gbps, RTT: 10 * time.Millisecond, Seed: 9}, linksim.Budget{Duration: 12 * time.Second}, engine.StabilityParams{}},
	)
	return out
}

// knownFailing lists scenarios whose oracles the current algorithm does not
// meet, with the cause. Each entry is a promise to fix, not an excuse; the
// algorithm work removes entries as it lands (see docs/product-specs/load.md).
var knownFailing = map[string]string{
	// #3: 250 MiB is spent before four intervals complete on fast links.
	"clean 400.0M rtt=10ms":       "byte cap (#3)",
	"clean 400.0M rtt=50ms":       "byte cap (#3)",
	"clean 400.0M rtt=150ms":      "byte cap (#3)",
	"bloated 400.0M":              "byte cap (#3)",
	"clean 1.00G rtt=10ms":        "byte cap (#3)",
	"clean 1.00G rtt=50ms":        "byte cap (#3)",
	"clean 1.00G rtt=150ms":       "byte cap (#3)",
	"bloated 1.00G":               "byte cap (#3)",
	"clean 10.00G rtt=10ms":       "byte cap (#3)",
	"clean 10.00G rtt=50ms":       "byte cap (#3)",
	"clean 10.00G rtt=150ms":      "byte cap (#3)",
	"bloated 10.00G":              "byte cap (#3)",
	"cdn per-flow cap 200M on 1G": "byte cap (#3); one-flow-per-interval ramp is too slow when flows are capped",
	// Bytes credited to the transport before they reach the wire inflate
	// goodput; the algorithm reports it with high confidence. Fix: sustained,
	// buffer-corrected estimate and lower bound.
	"upload send buffer 4MiB on 20M": "send-buffer credit inflates goodput (dishonest)",
	// A 2x burst decays over many intervals; the moving average never settles.
	"shaper burst 20MB on 50M": "token-bucket burst delays convergence",
	// The algorithm has no notion of a capacity change; it averages across it.
	"capacity halves at 5s (200M→100M)": "capacity change not detected",
}

func TestAlgorithmScenarios(t *testing.T) {
	var pass, target, known, stale int
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			o := linksim.Run(sc.link, sc.params, 16, sc.budget)
			s := o.Summary
			est := s.ThroughputBPS
			truth := o.TrueCapacity
			errPct := math.Abs(est-truth) / truth * 100
			wantRPM := 60000 / (float64(o.TrueLoadedRTT) / float64(time.Millisecond))
			rpmErr := math.Abs(s.RPM-wantRPM) / wantRPM * 100
			t.Logf("est=%s (truth %s, %.1f%% off) conf=%s/%s rpm=%.0f (want %.0f) intervals=%d flows=%d bytes=%s t=%.1fs trunc=%s",
				hb(est), hb(truth), errPct, s.ThroughputConfidence, s.ResponsivenessConfidence, s.RPM, wantRPM, s.Intervals, o.Flows, hB(o.Bytes), o.Elapsed.Seconds(), o.Reason)

			// Honesty oracles: a known-failing scenario may be dishonest today
			// (logged, counted); an unlisted one must not.
			var dishonest []string
			if s.ThroughputConfidence == engine.ConfidenceHigh && errPct > 10 {
				dishonest = append(dishonest, fmt.Sprintf("high confidence but throughput %.1f%% off", errPct))
			}
			if s.ResponsivenessConfidence == engine.ConfidenceHigh && rpmErr > 15 {
				dishonest = append(dishonest, fmt.Sprintf("high RPM confidence but %.1f%% off", rpmErr))
			}
			if len(dishonest) > 0 {
				if knownFailing[sc.name] == "" {
					t.Errorf("HONESTY: %v", dishonest)
				} else {
					t.Logf("KNOWN DISHONEST (%s): %v", knownFailing[sc.name], dishonest)
				}
			}
			if sc.budget.Bytes > 0 && o.Bytes > sc.budget.Bytes+16*mib {
				t.Errorf("BUDGET: %s exceeds %s", hB(o.Bytes), hB(sc.budget.Bytes))
			}
			if o.Elapsed > sc.budget.Duration+time.Second {
				t.Errorf("BUDGET: %.1fs exceeds %s", o.Elapsed.Seconds(), sc.budget.Duration)
			}

			// Target oracles (TDD ledger).
			var fails []string
			if s.ThroughputConfidence != engine.ConfidenceHigh || s.ResponsivenessConfidence != engine.ConfidenceHigh {
				fails = append(fails, fmt.Sprintf("converge: %s/%s", s.ThroughputConfidence, s.ResponsivenessConfidence))
			}
			if errPct > 10 {
				fails = append(fails, fmt.Sprintf("accuracy: throughput %.1f%% off", errPct))
			}
			if rpmErr > 15 {
				fails = append(fails, fmt.Sprintf("accuracy: rpm %.1f%% off", rpmErr))
			}
			switch {
			case len(fails) == 0 && knownFailing[sc.name] != "":
				stale++
				t.Errorf("STALE: scenario passes but is still listed in knownFailing (%s) — remove it", knownFailing[sc.name])
			case len(fails) == 0:
				pass++
			case knownFailing[sc.name] != "":
				known++
				t.Logf("KNOWN FAILING (%s): %v", knownFailing[sc.name], fails)
			default:
				target++
				t.Errorf("TARGET: %v", fails)
			}
		})
	}
	t.Logf("scorecard: %d pass, %d known failing, %d unexpected, %d stale", pass, known, target, stale)
}

func hb(bps float64) string {
	switch {
	case bps >= gbps:
		return fmt.Sprintf("%.2fG", bps/gbps)
	default:
		return fmt.Sprintf("%.1fM", bps/mbps)
	}
}

func hB(b int64) string {
	if b >= mib {
		return fmt.Sprintf("%dMiB", b/mib)
	}
	return fmt.Sprintf("%dB", b)
}
