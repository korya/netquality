package engine_test

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
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
	// expectTruncated marks a scenario whose budget is meant to bite (a
	// metered link): the oracle then asks for an honest truncation rather
	// than convergence.
	expectTruncated bool
}

// defaults mirrors the library defaults: time is the budget, no byte cap.
func defaults() linksim.Budget { return linksim.Budget{Duration: 12 * time.Second} }

// upload mirrors what the I/O layer sets for an upload phase: the HTTP/2
// stream window is credited per flow before the bytes reach the wire.
func upload() engine.StabilityParams { return engine.StabilityParams{SendBufferBytes: 4 * mib} }

// metered is a caller-set byte cap, as on a metered link.
func metered() linksim.Budget { return linksim.Budget{Duration: 12 * time.Second, Bytes: 250 * mib} }

func scenarios() []scenario {
	var out []scenario
	for _, c := range []float64{5 * mbps, 30 * mbps, 100 * mbps, 400 * mbps, 1 * gbps, 10 * gbps} {
		for _, rtt := range []time.Duration{10 * time.Millisecond, 50 * time.Millisecond, 150 * time.Millisecond} {
			out = append(out, scenario{fmt.Sprintf("clean %s rtt=%s", hb(c), rtt), linksim.Link{Capacity: c, RTT: rtt, Seed: 1}, defaults(), engine.StabilityParams{}, false})
		}
		// bufferbloat: a queue worth ~300 ms at capacity
		out = append(out, scenario{fmt.Sprintf("bloated %s", hb(c)), linksim.Link{Capacity: c, RTT: 30 * time.Millisecond, QueueBytes: c / 8 * 0.3, Seed: 2}, defaults(), engine.StabilityParams{}, false})
	}
	out = append(out,
		scenario{"cdn per-flow cap 200M on 1G", linksim.Link{Capacity: 1 * gbps, RTT: 20 * time.Millisecond, PerFlowCap: 200 * mbps, Seed: 3}, defaults(), engine.StabilityParams{}, false},
		scenario{"shaper burst 20MB on 50M", linksim.Link{Capacity: 50 * mbps, RTT: 30 * time.Millisecond, ShaperBurst: 20 * mib, Seed: 4}, defaults(), engine.StabilityParams{}, false},
		scenario{"upload send buffer 4MiB on 20M", linksim.Link{Capacity: 20 * mbps, RTT: 40 * time.Millisecond, SendBuffer: 4 * mib, Seed: 5}, defaults(), upload(), false},
		scenario{"upload send buffer 4MiB on 1G", linksim.Link{Capacity: 1 * gbps, RTT: 20 * time.Millisecond, SendBuffer: 4 * mib, Seed: 11}, defaults(), upload(), false},
		scenario{"tick jitter 200ms on 100M", linksim.Link{Capacity: 100 * mbps, RTT: 30 * time.Millisecond, TickJitter: 200 * time.Millisecond, Seed: 6}, defaults(), engine.StabilityParams{}, false},
		scenario{"capacity halves at 5s (200M→100M)", linksim.Link{Capacity: 200 * mbps, RTT: 30 * time.Millisecond, ChangeAt: 5 * time.Second, ChangeTo: 100 * mbps, Seed: 7}, defaults(), engine.StabilityParams{}, false},
		scenario{name: "metered 250MiB on 400M", link: linksim.Link{Capacity: 400 * mbps, RTT: 30 * time.Millisecond, Seed: 8}, budget: metered(), params: engine.StabilityParams{}, expectTruncated: true},
		scenario{name: "metered 250MiB on 1G", link: linksim.Link{Capacity: 1 * gbps, RTT: 20 * time.Millisecond, Seed: 9}, budget: metered(), params: engine.StabilityParams{}, expectTruncated: true},
		scenario{name: "metered 250MiB on 30M (never bites)", link: linksim.Link{Capacity: 30 * mbps, RTT: 30 * time.Millisecond, Seed: 10}, budget: metered(), params: engine.StabilityParams{}},
	)
	return out
}

// knownFailing lists scenarios whose oracles the current algorithm does not
// meet, with the cause. Each entry is a promise to fix, not an excuse: a new
// scenario that fails goes here with its reason and comes out when the
// algorithm change that clears it lands (see docs/product-specs/load.md).
var knownFailing = map[string]string{}

// cost is what a scenario spent; the ledger in testdata/cost_ledger.json
// pins it so an algorithm change that makes a run more expensive fails here
// instead of shipping. Regenerate deliberately with UPDATE_GOLDEN=1.
type cost struct {
	Bytes   int64   `json:"bytes"`
	Seconds float64 `json:"seconds"`
}

const costTolerance = 0.15 // fraction; simulator is deterministic, this absorbs jitter seeds

func TestAlgorithmScenarios(t *testing.T) {
	var pass, target, known, stale int
	ledgerPath := filepath.Join("testdata", "cost_ledger.json")
	ledger := map[string]cost{}
	if b, err := os.ReadFile(ledgerPath); err == nil {
		if err := json.Unmarshal(b, &ledger); err != nil {
			t.Fatal(err)
		}
	}
	observed := map[string]cost{}
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			o := linksim.Run(sc.link, sc.params, 16, sc.budget)
			observed[sc.name] = cost{Bytes: o.Bytes, Seconds: math.Round(o.Elapsed.Seconds()*100) / 100}
			if want, ok := ledger[sc.name]; ok && os.Getenv("UPDATE_GOLDEN") == "" {
				if float64(o.Bytes) > float64(want.Bytes)*(1+costTolerance) || o.Elapsed.Seconds() > want.Seconds*(1+costTolerance)+0.05 {
					t.Errorf("COST: %s / %.1fs exceeds ledger %s / %.1fs by more than %.0f%%", hB(o.Bytes), o.Elapsed.Seconds(), hB(want.Bytes), want.Seconds, costTolerance*100)
				}
				if float64(o.Bytes) < float64(want.Bytes)*(1-costTolerance) && o.Elapsed.Seconds() < want.Seconds*(1-costTolerance) {
					t.Errorf("COST: cheaper than the ledger (%s / %.1fs vs %s / %.1fs) — good, now run UPDATE_GOLDEN=1 to record it", hB(o.Bytes), o.Elapsed.Seconds(), hB(want.Bytes), want.Seconds)
				}
			}
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
			// Bound oracle: never known-failing. The bound must not exceed
			// the lowest capacity in force during its window, and must be
			// present whenever the estimate is.
			if s.LowerBoundBPS > 0 {
				truthLow := math.Inf(1)
				for i := s.LowerBoundStart - 1; i < s.LowerBoundStart-1+s.LowerBoundIntervals && i < len(o.Capacities); i++ {
					truthLow = math.Min(truthLow, o.Capacities[i])
				}
				if s.LowerBoundBPS > truthLow {
					t.Errorf("BOUND: lower bound %s exceeds capacity %s over intervals %d..%d", hb(s.LowerBoundBPS), hb(truthLow), s.LowerBoundStart, s.LowerBoundStart+s.LowerBoundIntervals-1)
				}
				if s.LowerBoundBPS > est*1.001 {
					t.Errorf("BOUND: lower bound %s above the estimate %s", hb(s.LowerBoundBPS), hb(est))
				}
				t.Logf("bound=%s over intervals %d..%d (truth there %s) rpm<=%.0f", hb(s.LowerBoundBPS), s.LowerBoundStart, s.LowerBoundStart+s.LowerBoundIntervals-1, hb(truthLow), s.RPMUpperBound)
			} else if !o.Truncated && s.ThroughputConfidence == engine.ConfidenceHigh && s.ResponsivenessConfidence == engine.ConfidenceHigh {
				t.Errorf("BOUND: converged but no lower bound")
			}
			if sc.budget.Bytes > 0 && o.Bytes > sc.budget.Bytes+16*mib {
				t.Errorf("BUDGET: %s exceeds %s", hB(o.Bytes), hB(sc.budget.Bytes))
			}
			if o.Elapsed > sc.budget.Duration+time.Second {
				t.Errorf("BUDGET: %.1fs exceeds %s", o.Elapsed.Seconds(), sc.budget.Duration)
			}

			// Target oracles (TDD ledger).
			var fails []string
			if sc.expectTruncated {
				if !o.Truncated || o.Reason != "bytes" || s.ResponsivenessConfidence == engine.ConfidenceHigh {
					fails = append(fails, fmt.Sprintf("metered: want honest bytes truncation, got trunc=%v reason=%q conf=%s/%s", o.Truncated, o.Reason, s.ThroughputConfidence, s.ResponsivenessConfidence))
				}
			} else if s.ThroughputConfidence != engine.ConfidenceHigh || s.ResponsivenessConfidence != engine.ConfidenceHigh {
				fails = append(fails, fmt.Sprintf("converge: %s/%s", s.ThroughputConfidence, s.ResponsivenessConfidence))
			}
			if !sc.expectTruncated {
				if errPct > 10 {
					fails = append(fails, fmt.Sprintf("accuracy: throughput %.1f%% off", errPct))
				}
				if rpmErr > 15 {
					fails = append(fails, fmt.Sprintf("accuracy: rpm %.1f%% off", rpmErr))
				}
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
	if os.Getenv("UPDATE_GOLDEN") != "" {
		names := make([]string, 0, len(observed))
		for n := range observed {
			names = append(names, n)
		}
		sort.Strings(names)
		ordered := make(map[string]cost, len(observed))
		for _, n := range names {
			ordered[n] = observed[n]
		}
		b, _ := json.MarshalIndent(ordered, "", "  ")
		if err := os.WriteFile(ledgerPath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", ledgerPath)
		return
	}
	for name := range observed {
		if _, ok := ledger[name]; !ok {
			t.Errorf("COST: %q is not in the ledger — run UPDATE_GOLDEN=1 go test ./internal/engine", name)
		}
	}
	for name := range ledger {
		if _, ok := observed[name]; !ok {
			t.Errorf("COST: ledger has %q but no such scenario exists — regenerate", name)
		}
	}
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
