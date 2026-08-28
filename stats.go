package netquality

import (
	"math"
	"sort"
	"time"
)

// LatencyStats summarises a set of latency samples.
//
// Jitter is the mean absolute deviation of the samples from their mean.
// Stages holds per-stage medians (dns, connect, tls, ttfb) when the samples
// carry stage timings (foreign probes and idle probes do; self probes do not).
type LatencyStats struct {
	Samples int           `json:"samples"`
	Min     time.Duration `json:"min_ns"`
	Median  time.Duration `json:"median_ns"`
	Mean    time.Duration `json:"mean_ns"`
	P95     time.Duration `json:"p95_ns"`
	Max     time.Duration `json:"max_ns"`
	Jitter  time.Duration `json:"jitter_ns"`
	Stages  *StageMedians `json:"stages,omitempty"`
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

// tlsPerRTT returns the TLS handshake time normalised to one round trip.
func (s LatencySample) tlsPerRTT() time.Duration {
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

// computeLatencyStats builds LatencyStats from samples, using Total as the headline
// value. Stage medians are computed only over staged samples.
func computeLatencyStats(samples []LatencySample) LatencyStats {
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
			TLSPerRTT: med(func(s LatencySample) time.Duration { return s.tlsPerRTT() }),
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
	return LatencyStats{
		Samples: len(sorted),
		Min:     sorted[0],
		Median:  medianDuration(sorted),
		Mean:    mean,
		P95:     percentile(sorted, 95),
		Max:     sorted[len(sorted)-1],
		Jitter:  time.Duration(dev / float64(len(sorted))),
	}
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
