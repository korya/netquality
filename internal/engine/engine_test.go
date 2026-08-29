package engine

import (
	"math"
	"testing"
	"time"
)

// cloudflareDownload is the per-interval cumulative byte series of a real
// download phase against Cloudflare on 2026-08-28 (1 s intervals, one flow
// added per interval). It anchors the engine to observed behaviour.
var cloudflareDownload = []int64{
	562500, 1462500, 3862500, 6587500, 8712500, 13737500, 16875000, 23912500,
	40412500, 61012500, 77912500, 97400000,
}

// probeSet returns deterministic probe samples for interval i: latency rises
// with the flow count, then plateaus, so responsiveness converges.
func probeSet(i int) (foreign, self []LatencySample) {
	base := 20 * time.Millisecond
	if i < 8 {
		base += time.Duration(i) * 5 * time.Millisecond
	} else {
		base += 40 * time.Millisecond
	}
	for k := 0; k < 10; k++ {
		j := time.Duration(k%3) * time.Millisecond
		foreign = append(foreign, LatencySample{Connect: base + j, TLS: base + j, TLSRTTs: 1, HTTP: base + j, Total: 3 * (base + j), Staged: true})
		self = append(self, LatencySample{HTTP: base/2 + j, Total: base/2 + j})
	}
	return
}

// referenceLoop is the draft's plain interval loop (one flow per interval,
// responsiveness after goodput stability), kept as the oracle for what the
// Engine still shares with it: the goodput moving average, peak and
// confidence. Ramp shape and responsiveness timing are tested separately.
type refResult struct {
	intervals        int
	flows            int
	stoppedAt        int
	mas, rpms        []float64
	tStable, rStable bool
	tConf, rConf     Confidence
	peak, finalMA    float64
	rpm, frpm, srpm  float64
}

func referenceLoop(bytes []int64, sp StabilityParams, maxFlows int) refResult {
	sp = sp.WithDefaults()
	tp := NewTracker(sp.MovingAverageDistance, sp.StdDevTolerance)
	rp := NewTracker(sp.MovingAverageDistance, sp.StdDevTolerance)
	var foreignB, selfB [][]LatencySample
	window := func(n int) (f, s []LatencySample) {
		start := len(foreignB) - n
		if start < 0 {
			start = 0
		}
		for i := start; i < len(foreignB); i++ {
			f = append(f, foreignB[i]...)
			s = append(s, selfB[i]...)
		}
		return
	}
	r := refResult{flows: sp.InitialFlows}
	var lastBytes int64
	goodputStable := false
	for i, cur := range bytes {
		goodput := float64(cur-lastBytes) * 8 / sp.Interval.Seconds()
		lastBytes = cur
		if goodput > r.peak {
			r.peak = goodput
		}
		avg := tp.Push(goodput)
		r.intervals++
		f, s := probeSet(i)
		foreignB, selfB = append(foreignB, f), append(selfB, s)
		r.mas = append(r.mas, avg)
		if !goodputStable && tp.Stable() {
			goodputStable = true
		}
		if goodputStable {
			f, s := window(sp.MovingAverageDistance)
			cur, _, _ := Responsiveness(f, s, sp.TrimmedMeanPercent)
			r.rpms = append(r.rpms, cur)
			if cur > 0 {
				rp.Push(cur)
			}
			if rp.Stable() {
				r.stoppedAt = r.intervals
				break
			}
		}
		if r.flows < maxFlows {
			for k := 0; k < sp.FlowIncrement && r.flows < maxFlows; k++ {
				r.flows++
			}
		}
	}
	r.finalMA = tp.Current()
	r.tStable, r.tConf = tp.Stable(), tp.Confidence()
	r.rStable, r.rConf = rp.Stable(), rp.Confidence()
	if !goodputStable {
		r.rConf = ConfidenceLow
	}
	f, s := window(sp.MovingAverageDistance)
	r.rpm, r.frpm, r.srpm = Responsiveness(f, s, sp.TrimmedMeanPercent)
	return r
}

func runEngine(bytes []int64, sp StabilityParams, maxFlows int) refResult {
	e := New(sp, maxFlows)
	r := refResult{flows: e.InitialFlows()}
	for i, cur := range bytes {
		f, s := probeSet(i)
		d := e.Interval(Observation{Elapsed: sp.WithDefaults().Interval, Bytes: cur, Flows: r.flows, Foreign: f, Self: s})
		r.intervals = d.Interval
		r.mas = append(r.mas, d.ThroughputBPS)
		if d.ThroughputStable {
			r.rpms = append(r.rpms, d.RPM)
		}
		if d.Stop {
			r.stoppedAt = d.Interval
			break
		}
		r.flows += d.AddFlows
	}
	sum := e.Summary(nil, nil)
	r.finalMA, r.peak = sum.ThroughputBPS, sum.PeakThroughputBPS
	r.tStable, r.tConf = sum.ThroughputStable, sum.ThroughputConfidence
	r.rStable, r.rConf = sum.ResponsivenessStable, sum.ResponsivenessConfidence
	r.rpm, r.frpm, r.srpm = sum.RPM, sum.ForeignRPM, sum.SelfRPM
	return r
}

func TestEngineMatchesReferenceLoop(t *testing.T) {
	cases := []struct {
		name     string
		bytes    []int64
		sp       StabilityParams
		maxFlows int
	}{
		{"cloudflare download, defaults", cloudflareDownload, StabilityParams{}, 16},
		{"cloudflare download, max 3 flows", cloudflareDownload, StabilityParams{}, 3},
		{"flat link converges", flat(30, 100e6), StabilityParams{}, 16},
		{"flat link, MAD 2, quick stop", flat(30, 100e6), StabilityParams{MovingAverageDistance: 2}, 16},
		{"never stable", ramp(30), StabilityParams{StdDevTolerance: 1e-9}, 16},
		{"single interval", cloudflareDownload[:1], StabilityParams{}, 16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := referenceLoop(tc.bytes, tc.sp, tc.maxFlows)
			got := runEngine(tc.bytes, tc.sp, tc.maxFlows)
			// The engine may stop earlier or later (responsiveness is tracked
			// from the end of the ramp and judged without double smoothing);
			// while both run, the goodput series is the draft's.
			n := got.intervals
			if want.intervals < n {
				n = want.intervals
			}
			if !feq(got.mas[:n], want.mas[:n]) {
				t.Errorf("series: got mas=%v want mas=%v", got.mas[:n], want.mas[:n])
			}
			if got.intervals == want.intervals && got.stoppedAt == 0 && want.stoppedAt == 0 {
				if got.tStable != want.tStable || got.tConf != want.tConf {
					t.Errorf("stability: got %v %s want %v %s", got.tStable, got.tConf, want.tStable, want.tConf)
				}
				if got.peak != want.peak || got.finalMA != want.finalMA || got.rpm != want.rpm || got.frpm != want.frpm || got.srpm != want.srpm {
					t.Errorf("figures: got %v %v %v/%v/%v want %v %v %v/%v/%v", got.peak, got.finalMA, got.rpm, got.frpm, got.srpm, want.peak, want.finalMA, want.rpm, want.frpm, want.srpm)
				}
			}
			if got.stoppedAt != 0 && (!got.tStable || !got.rStable || got.tConf != ConfidenceHigh || got.rConf != ConfidenceHigh) {
				t.Errorf("stopped at %d without both series stable: %v/%v %s/%s", got.stoppedAt, got.tStable, got.rStable, got.tConf, got.rConf)
			}
			t.Logf("intervals=%d stoppedAt=%d flows=%d MA=%.0f conf=%s/%s rpm=%.0f", got.intervals, got.stoppedAt, got.flows, got.finalMA, got.tConf, got.rConf, got.rpm)
		})
	}
}

func TestEngineProbeGap(t *testing.T) {
	e := New(StabilityParams{}, 16)
	if got := e.ProbeGap(5000, 1000); got != 10*time.Millisecond {
		t.Errorf("no goodput yet: gap = %v, want 1/MPS", got)
	}
	// 1 Mbps goodput: 5% = 50 kbps; a probe pair averages 3000 B = 24 kbit,
	// so probes may go out every 0.48 s.
	e.Interval(Observation{Elapsed: time.Second, Bytes: 125000})
	if got := e.ProbeGap(5000, 1000); math.Abs(float64(got-480*time.Millisecond)) > float64(time.Millisecond) {
		t.Errorf("throttled gap = %v, want ~480ms", got)
	}
	// Fast link: never below 1/MPS.
	e.Interval(Observation{Elapsed: time.Second, Bytes: 125000 + 125e6})
	if got := e.ProbeGap(5000, 1000); got != 10*time.Millisecond {
		t.Errorf("fast link gap = %v", got)
	}
}

func TestEngineSummaryFallbacks(t *testing.T) {
	e := New(StabilityParams{}, 16)
	// No interval completed: samples in progress are used, MA is 0.
	f, s := probeSet(0)
	sum := e.Summary(f, s)
	if sum.Intervals != 0 || sum.ThroughputBPS != 0 || sum.RPM == 0 || len(sum.Foreign) != len(f) {
		t.Errorf("%+v", sum)
	}
	if sum.ThroughputConfidence != ConfidenceLow || sum.ResponsivenessConfidence != ConfidenceLow {
		t.Errorf("confidence %s/%s", sum.ThroughputConfidence, sum.ResponsivenessConfidence)
	}
	// InitialFlows never exceeds maxFlows.
	if New(StabilityParams{InitialFlows: 8}, 2).InitialFlows() != 2 {
		t.Error("InitialFlows must be capped")
	}
}

func flat(n int, bps float64) []int64 {
	out := make([]int64, n)
	var cum int64
	for i := range out {
		cum += int64(bps / 8)
		out[i] = cum
	}
	return out
}

func ramp(n int) []int64 {
	out := make([]int64, n)
	var cum int64
	for i := range out {
		cum += int64(1e6 * float64(i+1))
		out[i] = cum
	}
	return out
}

func feq(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-9*math.Max(1, math.Abs(b[i])) {
			return false
		}
	}
	return true
}
