package engine

import (
	"testing"
	"time"
)

// TestPercentilePresenceThresholds pins the rule: a percentile field is set
// exactly when nearest-rank makes it distinct from the maximum, and its value
// is the expected order statistic.
func TestPercentilePresenceThresholds(t *testing.T) {
	seq := func(n int) []time.Duration {
		d := make([]time.Duration, n)
		for i := range d {
			d[i] = time.Duration(i+1) * time.Millisecond // 1ms..n ms, sorted
		}
		return d
	}
	cases := []struct {
		n                  int
		p80, p90, p95, p99 int // expected rank (1-based) or 0 = absent
	}{
		{1, 0, 0, 0, 0},
		{4, 0, 0, 0, 0},
		{5, 4, 0, 0, 0},
		{9, 8, 0, 0, 0},
		{10, 8, 9, 0, 0},
		{19, 16, 18, 0, 0},
		{20, 16, 18, 19, 0},
		{99, 80, 90, 95, 0},
		{100, 80, 90, 95, 99},
		{400, 320, 360, 380, 396},
	}
	for _, tc := range cases {
		st := statsOf(seq(tc.n))
		got := map[string]int{"p80": int(st.P80 / time.Millisecond), "p90": int(st.P90 / time.Millisecond), "p95": int(st.P95 / time.Millisecond), "p99": int(st.P99 / time.Millisecond)}
		want := map[string]int{"p80": tc.p80, "p90": tc.p90, "p95": tc.p95, "p99": tc.p99}
		for k := range want {
			if got[k] != want[k] {
				t.Errorf("n=%d %s: got rank %d want %d", tc.n, k, got[k], want[k])
			}
		}
		for k, v := range got {
			if v == tc.n && v != 0 {
				t.Errorf("n=%d %s equals the maximum — that is the bug this test exists for", tc.n, k)
			}
		}
		if st.Max != time.Duration(tc.n)*time.Millisecond {
			t.Errorf("n=%d max=%v", tc.n, st.Max)
		}
	}
	if p, v := statsOf(seq(5)).HighestPercentile(); p != 80 || v != 4*time.Millisecond {
		t.Errorf("highest at 5 = p%v %v", p, v)
	}
	if p, v := statsOf(seq(100)).HighestPercentile(); p != 99 || v != 99*time.Millisecond {
		t.Errorf("highest at 100 = p%v %v", p, v)
	}
	if p, _ := statsOf(seq(3)).HighestPercentile(); p != 0 {
		t.Errorf("highest at 3 = p%v", p)
	}
}
