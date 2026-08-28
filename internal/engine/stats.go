package engine

import (
	"math"
	"sort"
	"time"
)

// LatencyStats summarises a set of latency samples.
//
// Percentiles use the nearest-rank method, which returns the maximum for any
// percentile above 100·(n-1)/n. A percentile field is therefore present only
// when the sample count makes it a real order statistic distinct from the
// maximum: P80 from 5 samples, P90 from 10, P95 from 20, P99 from 100. A
// field never holds a lower percentile than its name says; absent means "not
// enough samples", never zero.
//
// Jitter is the mean absolute deviation of the samples from their mean.
// Stages holds per-stage medians (dns, connect, tls, ttfb) when the samples
// carry stage timings (foreign probes and idle probes do; self probes do not).
type LatencyStats struct {
	Samples int           `json:"samples"`
	Min     time.Duration `json:"min_ns"`
	Median  time.Duration `json:"median_ns"`
	Mean    time.Duration `json:"mean_ns"`
	P80     time.Duration `json:"p80_ns,omitempty"`
	P90     time.Duration `json:"p90_ns,omitempty"`
	P95     time.Duration `json:"p95_ns,omitempty"`
	P99     time.Duration `json:"p99_ns,omitempty"`
	Max     time.Duration `json:"max_ns"`
	Jitter  time.Duration `json:"jitter_ns"`
	Stages  *StageMedians `json:"stages,omitempty"`
}

// percentileMinSamples is the smallest n at which nearest-rank percentile p
// is not simply the maximum: the least n with ceil(p/100·n) < n.
var percentileMinSamples = map[float64]int{80: 5, 90: 10, 95: 20, 99: 100}

// HighestPercentile returns the largest percentile present and its value,
// or (0, 0) when the set is too small for any.
func (s LatencyStats) HighestPercentile() (float64, time.Duration) {
	switch {
	case s.P99 > 0:
		return 99, s.P99
	case s.P95 > 0:
		return 95, s.P95
	case s.P90 > 0:
		return 90, s.P90
	case s.P80 > 0:
		return 80, s.P80
	}
	return 0, 0
}

// StageMedians holds median per-stage timings from net/http/httptrace.
// TLS is the raw handshake time; TLSPerRTT is the same value normalised to a
// single round trip (TLS 1.3 = 1 RTT, TLS 1.2 = 2 RTTs) as the draft requires.
type StageMedians struct {
	DNS       time.Duration `json:"dns_ns"`
	Connect   time.Duration `json:"connect_ns"`
	TLS       time.Duration `json:"tls_ns"`
	TLSPerRTT time.Duration `json:"tls_per_rtt_ns"`
	TTFB      time.Duration `json:"ttfb_ns"`
}

// LatencySample is one probe measurement.
type LatencySample struct {
	Total   time.Duration // full wall time of the probe (foreign: incl. connect+TLS; self: request only)
	DNS     time.Duration
	Connect time.Duration // TCP handshake
	TLS     time.Duration // raw TLS handshake time
	TLSRTTs int           // number of round trips the negotiated TLS version needs (0 = no TLS)
	TTFB    time.Duration // request sent -> first response byte
	HTTP    time.Duration // request sent -> full body read (http_f / http_l in the draft)
	Staged  bool          // true when DNS/Connect/TLS/TTFB are populated (fresh connection)
}

// TLSPerRTT returns the TLS handshake time normalised to one round trip.
func (s LatencySample) TLSPerRTT() time.Duration {
	if s.TLSRTTs <= 0 {
		return 0
	}
	return s.TLS / time.Duration(s.TLSRTTs)
}

func durationsOf(samples []LatencySample, f func(LatencySample) time.Duration) []time.Duration {
	out := make([]time.Duration, 0, len(samples))
	for _, s := range samples {
		out = append(out, f(s))
	}
	return out
}

func sortedCopy(d []time.Duration) []time.Duration {
	c := append([]time.Duration(nil), d...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return c
}

// percentile returns the p-th percentile (0..100) using nearest-rank on sorted data.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	rank := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func meanDuration(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	var sum float64
	for _, v := range d {
		sum += float64(v)
	}
	return time.Duration(sum / float64(len(d)))
}

func medianDuration(sorted []time.Duration) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// trimmedMean is the single-sided trimmed mean at percentile p: the mean of all
// samples at or below the p-th percentile. This is the draft's TM(x) with TMP=p.
func trimmedMean(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sorted := sortedCopy(d)
	cut := percentile(sorted, p)
	var sum float64
	var n int
	for _, v := range sorted {
		if v > cut {
			break
		}
		sum += float64(v)
		n++
	}
	if n == 0 {
		return 0
	}
	return time.Duration(sum / float64(n))
}

// ComputeLatencyStats builds LatencyStats from samples, using Total as the headline
// value. Stage medians are computed only over staged samples.
func ComputeLatencyStats(samples []LatencySample) LatencyStats {
	totals := durationsOf(samples, func(s LatencySample) time.Duration { return s.Total })
	st := statsOf(totals)
	var staged []LatencySample
	for _, s := range samples {
		if s.Staged {
			staged = append(staged, s)
		}
	}
	if len(staged) > 0 {
		med := func(f func(LatencySample) time.Duration) time.Duration {
			return medianDuration(sortedCopy(durationsOf(staged, f)))
		}
		st.Stages = &StageMedians{
			DNS:       med(func(s LatencySample) time.Duration { return s.DNS }),
			Connect:   med(func(s LatencySample) time.Duration { return s.Connect }),
			TLS:       med(func(s LatencySample) time.Duration { return s.TLS }),
			TLSPerRTT: med(func(s LatencySample) time.Duration { return s.TLSPerRTT() }),
			TTFB:      med(func(s LatencySample) time.Duration { return s.TTFB }),
		}
	}
	return st
}

func statsOf(d []time.Duration) LatencyStats {
	if len(d) == 0 {
		return LatencyStats{}
	}
	sorted := sortedCopy(d)
	mean := meanDuration(sorted)
	var dev float64
	for _, v := range sorted {
		dev += math.Abs(float64(v - mean))
	}
	st := LatencyStats{
		Samples: len(sorted),
		Min:     sorted[0],
		Median:  medianDuration(sorted),
		Mean:    mean,
		Max:     sorted[len(sorted)-1],
		Jitter:  time.Duration(dev / float64(len(sorted))),
	}
	n := len(sorted)
	if n >= percentileMinSamples[80] {
		st.P80 = percentile(sorted, 80)
	}
	if n >= percentileMinSamples[90] {
		st.P90 = percentile(sorted, 90)
	}
	if n >= percentileMinSamples[95] {
		st.P95 = percentile(sorted, 95)
	}
	if n >= percentileMinSamples[99] {
		st.P99 = percentile(sorted, 99)
	}
	return st
}

// stddev of a float slice (population).
func stddev(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(len(xs))
	var v float64
	for _, x := range xs {
		v += (x - mean) * (x - mean)
	}
	return math.Sqrt(v / float64(len(xs)))
}

// rpm converts a round-trip time into round-trips per minute.
func rpm(rtt time.Duration) float64 {
	if rtt <= 0 {
		return 0
	}
	return 60000 / (float64(rtt) / float64(time.Millisecond))
}

// Responsiveness computes the draft's RPM figures over a sample set.
func Responsiveness(foreign, self []LatencySample, tmp float64) (total, foreignRPM, selfRPM float64) {
	if len(foreign) > 0 {
		tcp := trimmedMean(durationsOf(foreign, func(s LatencySample) time.Duration { return s.Connect }), tmp)
		tlsd := trimmedMean(durationsOf(foreign, func(s LatencySample) time.Duration { return s.TLSPerRTT() }), tmp)
		httpf := trimmedMean(durationsOf(foreign, func(s LatencySample) time.Duration { return s.HTTP }), tmp)
		var rtt time.Duration
		if tlsd > 0 {
			rtt = (tcp + tlsd + httpf) / 3
		} else {
			rtt = (tcp + httpf) / 2 // TCP-only case, draft 5.3.1.2
		}
		foreignRPM = rpm(rtt)
	}
	if len(self) > 0 {
		selfRPM = rpm(trimmedMean(durationsOf(self, func(s LatencySample) time.Duration { return s.HTTP }), tmp))
	}
	switch {
	case foreignRPM > 0 && selfRPM > 0:
		total = (foreignRPM + selfRPM) / 2
	case foreignRPM > 0:
		total = foreignRPM
	default:
		total = selfRPM
	}
	return
}
