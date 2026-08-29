// Package linksim is a deliberately simple fluid model of a network path used
// to drive the measurement engine in tests. It does not model TCP; flows are
// fluid rates with a slow-start ramp and fair sharing of a bottleneck. It
// models the effects that matter to the engine's decisions: capacity, RTT, a
// bufferbloated queue, per-flow ceilings, token-bucket shaping, send-buffer
// credit, tick jitter, and capacity changes mid-run.
package linksim

import (
	"math"
	"math/rand"
	"time"

	"github.com/korya/netquality/internal/engine"
)

// Link describes the modelled path for one direction.
type Link struct {
	// Capacity of the bottleneck in bits per second (ground truth).
	Capacity float64
	// RTT is the propagation round trip with empty queues.
	RTT time.Duration
	// QueueBytes is the bottleneck buffer. Under saturation it fills and adds
	// QueueBytes*8/Capacity of delay (bufferbloat). 0 = no queue.
	QueueBytes float64
	// PerFlowCap bounds a single flow (CDN per-connection ceiling). 0 = none.
	PerFlowCap float64
	// ShaperBurst, if > 0, makes the bottleneck a token bucket of that many
	// bytes refilled at Capacity: a fresh link passes a burst faster than the
	// sustained rate (2x here), then settles to Capacity.
	ShaperBurst float64
	// SendBuffer is credited to the byte count immediately when a flow starts
	// (bytes accepted by the transport before they reach the wire); models
	// the HTTP/2 window + socket buffer on upload. 0 for download.
	SendBuffer float64
	// TickJitter is the maximum random lateness of an interval tick.
	TickJitter time.Duration
	// ChangeAt / ChangeTo apply a capacity change mid-run. Zero = none.
	ChangeAt time.Duration
	ChangeTo float64
	// Seed for the deterministic random source.
	Seed int64
}

// Budget bounds a simulated phase like Options.MaxDuration / MaxBytes.
type Budget struct {
	Duration time.Duration
	Bytes    int64 // 0 = unlimited
}

// Outcome is what a simulated phase produced.
type Outcome struct {
	Summary       engine.Summary
	Bytes         int64
	Elapsed       time.Duration
	Flows         int
	Truncated     bool
	Reason        string  // "" | "duration" | "bytes"
	TrueCapacity  float64 // capacity in force at the end
	TrueLoadedRTT time.Duration
	// MovingAverages is the engine's goodput estimate after each interval.
	MovingAverages []float64
	// Decisions is the engine's decision after each interval.
	Decisions []engine.Decision
	// Capacities is the capacity in force at the end of each interval, so an
	// oracle can compare a windowed figure with the truth of its window.
	Capacities []float64
}

const dt = 10 * time.Millisecond

// Run drives the engine over the link until it stops or the budget ends.
func Run(link Link, p engine.StabilityParams, maxFlows int, budget Budget) Outcome {
	p = p.WithDefaults()
	rng := rand.New(rand.NewSource(link.Seed)) //nolint:gosec // deterministic simulation
	eng := engine.New(p, maxFlows)
	capacity := link.Capacity
	var (
		now        time.Duration
		bytes      float64 // counted bytes (what the client sees)
		queue      float64 // bytes in the bottleneck queue
		tokens     = link.ShaperBurst
		flows      []float64 // current rate per flow (bps)
		flowStart  []time.Duration
		lastTick   time.Duration
		nextTick   = p.Interval + jitter(rng, link.TickJitter)
		curF, curS []engine.LatencySample
		nextProbe  time.Duration
		out        Outcome
	)
	addFlows := func(n int) {
		for i := 0; i < n && len(flows) < maxFlows; i++ {
			flows = append(flows, 0)
			flowStart = append(flowStart, now)
			bytes += link.SendBuffer
		}
	}
	addFlows(eng.InitialFlows())
	loadedRTT := func() time.Duration {
		if capacity <= 0 {
			return link.RTT
		}
		return link.RTT + time.Duration(queue*8/capacity*float64(time.Second))
	}
	for {
		if link.ChangeAt > 0 && now >= link.ChangeAt {
			capacity = link.ChangeTo
		}
		// Offered load: each flow ramps by doubling per RTT (slow start) toward
		// its fair share, capped by PerFlowCap.
		fair := capacity
		if len(flows) > 0 {
			fair = capacity / float64(len(flows))
		}
		var offered float64
		for i := range flows {
			target := capacity
			if link.PerFlowCap > 0 && target > link.PerFlowCap {
				target = link.PerFlowCap
			}
			age := now - flowStart[i]
			rtt := link.RTT
			if rtt <= 0 {
				rtt = time.Millisecond
			}
			ramp := 1e6 * math.Pow(2, float64(age)/float64(rtt)) // 1 Mbps doubling per RTT
			if ramp < target {
				target = ramp
			}
			// fair share only binds when the link is saturated
			if target > fair && offered+target > capacity {
				target = math.Max(fair, 0)
			}
			flows[i] = target
			offered += target
		}
		// Bottleneck: a token bucket (bucket = ShaperBurst, refill = capacity,
		// drain up to 2x capacity while tokens last) or a plain rate limiter.
		in := offered / 8 * dt.Seconds() // bytes arriving this step
		queue += in
		var delivered float64
		if link.ShaperBurst > 0 {
			tokens = math.Min(link.ShaperBurst, tokens+capacity/8*dt.Seconds())
			delivered = math.Min(queue, math.Min(tokens, 2*capacity/8*dt.Seconds()))
			tokens -= delivered
		} else {
			delivered = math.Min(queue, capacity/8*dt.Seconds())
		}
		queue -= delivered
		if link.QueueBytes > 0 && queue > link.QueueBytes {
			queue = link.QueueBytes // drop the excess
		} else if link.QueueBytes == 0 {
			queue = 0
		}
		bytes += delivered
		now += dt

		// Probes at the engine's spacing; latency = loaded RTT + small noise.
		if now >= nextProbe {
			nextProbe = now + eng.ProbeGap(5000, 1000)
			l := loadedRTT() + time.Duration(rng.Intn(2000))*time.Microsecond
			curF = append(curF, engine.LatencySample{Connect: l, TLS: l, TLSRTTs: 1, HTTP: l, TTFB: l, Total: 3 * l, Staged: true})
			curS = append(curS, engine.LatencySample{HTTP: l, Total: l})
		}

		if budget.Bytes > 0 && int64(bytes) >= budget.Bytes {
			out.Truncated, out.Reason = true, "bytes"
			break
		}
		if now >= budget.Duration {
			out.Truncated, out.Reason = true, "duration"
			break
		}
		if now >= nextTick {
			d := eng.Interval(engine.Observation{Elapsed: now - lastTick, Bytes: int64(bytes), Flows: len(flows), Foreign: curF, Self: curS})
			curF, curS = nil, nil
			out.MovingAverages = append(out.MovingAverages, d.ThroughputBPS)
			out.Capacities = append(out.Capacities, capacity)
			out.Decisions = append(out.Decisions, d)
			lastTick = now
			nextTick = now + p.Interval + jitter(rng, link.TickJitter)
			if d.Stop {
				break
			}
			addFlows(d.AddFlows)
		}
	}
	out.Summary = eng.Summary(curF, curS)
	out.Bytes = int64(bytes)
	out.Elapsed = now
	out.Flows = len(flows)
	out.TrueCapacity = capacity
	out.TrueLoadedRTT = loadedRTT()
	if out.Truncated && eng.Stopped() {
		out.Truncated, out.Reason = false, ""
	}
	return out
}

func jitter(rng *rand.Rand, max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rng.Int63n(int64(max)))
}
