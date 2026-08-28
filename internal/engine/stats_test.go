package engine

import (
	"testing"
	"time"
)

func ms(n float64) time.Duration { return time.Duration(n * float64(time.Millisecond)) }

func TestPercentileAndTrimmedMean(t *testing.T) {
	var d []time.Duration
	for i := 1; i <= 100; i++ {
		d = append(d, ms(float64(i)))
	}
	if got := percentile(sortedCopy(d), 95); got != ms(95) {
		t.Errorf("p95 = %v", got)
	}
	if got := percentile(sortedCopy(d), 50); got != ms(50) {
		t.Errorf("p50 = %v", got)
	}
	// Mean of 1..95 = 48ms.
	if got := trimmedMean(d, 95); got != ms(48) {
		t.Errorf("TM95 = %v", got)
	}
	if got := trimmedMean(nil, 95); got != 0 {
		t.Errorf("TM95(nil) = %v", got)
	}
	if got := trimmedMean([]time.Duration{ms(7)}, 95); got != ms(7) {
		t.Errorf("TM95(single) = %v", got)
	}
}

func TestStatsOf(t *testing.T) {
	st := statsOf([]time.Duration{ms(10), ms(20), ms(30), ms(40)})
	if st.Samples != 4 || st.Min != ms(10) || st.Max != ms(40) || st.Mean != ms(25) || st.Median != ms(25) {
		t.Errorf("%+v", st)
	}
	if st.Jitter != ms(10) { // mean |x-25| = (15+5+5+15)/4 = 10
		t.Errorf("jitter = %v", st.Jitter)
	}
	if st.P80 != 0 || st.P90 != 0 || st.P95 != 0 || st.P99 != 0 {
		t.Errorf("4 samples support no percentile: %+v", st)
	}
	if z := statsOf(nil); z.Samples != 0 {
		t.Error("empty")
	}
}

func TestComputeLatencyStatsStages(t *testing.T) {
	samples := []LatencySample{
		{Total: ms(30), Connect: ms(10), TLS: ms(20), TLSRTTs: 2, TTFB: ms(5), Staged: true},
		{Total: ms(40), Connect: ms(12), TLS: ms(10), TLSRTTs: 1, TTFB: ms(7), Staged: true},
		{Total: ms(5)}, // self probe: no stages
	}
	st := ComputeLatencyStats(samples)
	if st.Samples != 3 || st.Stages == nil {
		t.Fatalf("%+v", st)
	}
	if st.Stages.Connect != ms(11) || st.Stages.TLSPerRTT != ms(10) || st.Stages.TTFB != ms(6) {
		t.Errorf("%+v", *st.Stages)
	}
	if ComputeLatencyStats([]LatencySample{{Total: ms(1)}}).Stages != nil {
		t.Error("unstaged samples must not produce stage medians")
	}
}

func TestRPM(t *testing.T) {
	if got := rpm(ms(10)); got != 6000 {
		t.Errorf("rpm(10ms) = %v", got)
	}
	if got := rpm(0); got != 0 {
		t.Errorf("rpm(0) = %v", got)
	}
}

func TestResponsiveness(t *testing.T) {
	foreign := []LatencySample{{Connect: ms(10), TLS: ms(10), TLSRTTs: 1, HTTP: ms(10), Staged: true}}
	self := []LatencySample{{HTTP: ms(20)}}
	total, f, s := Responsiveness(foreign, self, 95)
	if f != 6000 || s != 3000 || total != 4500 {
		t.Errorf("got %v %v %v", total, f, s)
	}
	// TCP-only: tls ignored, (10+30)/2 = 20ms -> 3000
	total, f, _ = Responsiveness([]LatencySample{{Connect: ms(10), HTTP: ms(30), Staged: true}}, nil, 95)
	if f != 3000 || total != 3000 {
		t.Errorf("tcp-only got %v %v", total, f)
	}
	if total, _, _ = Responsiveness(nil, nil, 95); total != 0 {
		t.Error("empty")
	}
}
