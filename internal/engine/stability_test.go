package engine

import "testing"

func TestStabilityTracker(t *testing.T) {
	tests := []struct {
		name       string
		mad        int
		tol        float64
		values     []float64
		wantStable bool
		wantConf   Confidence
	}{
		{"too few intervals", 4, 0.05, []float64{100, 100, 100}, false, ConfidenceLow},
		{"flat", 4, 0.05, []float64{100, 100, 100, 100}, true, ConfidenceHigh},
		{"ramping", 4, 0.05, []float64{10, 20, 40, 80, 160}, false, ConfidenceMedium},
		{"ramp then flat", 4, 0.05, []float64{10, 50, 90, 100, 100, 100, 100, 100, 100, 100}, true, ConfidenceHigh},
		{"small noise ok", 4, 0.05, []float64{100, 102, 98, 101, 99, 100, 101}, true, ConfidenceHigh},
		{"drifting", 4, 0.05, []float64{100, 200, 300, 400, 500, 600, 700, 800}, false, ConfidenceMedium},
		{"noise beyond tolerance", 4, 0.01, []float64{100, 100, 100, 100, 100, 110, 110, 110}, false, ConfidenceMedium},
		{"zeros never stable", 4, 0.05, []float64{0, 0, 0, 0, 0}, false, ConfidenceMedium},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewTracker(tc.mad, tc.tol)
			for _, v := range tc.values {
				tr.Push(v)
			}
			if got := tr.Stable(); got != tc.wantStable {
				t.Errorf("stable = %v, want %v (averages %v)", got, tc.wantStable, tr.averages)
			}
			if got := tr.Confidence(); got != tc.wantConf {
				t.Errorf("confidence = %v, want %v", got, tc.wantConf)
			}
		})
	}
}

func TestStabilityTrackerMovingAverage(t *testing.T) {
	tr := NewTracker(2, 0.05)
	if got := tr.Push(10); got != 10 {
		t.Error(got)
	}
	if got := tr.Push(30); got != 20 {
		t.Error(got)
	}
	if got := tr.Push(50); got != 40 { // window of 2: (30+50)/2
		t.Error(got)
	}
	if tr.Intervals() != 3 || tr.Current() != 40 {
		t.Error("bookkeeping")
	}
}

func TestDefaultStabilityParams(t *testing.T) {
	p := StabilityParams{}.WithDefaults()
	if p != DefaultStabilityParams() {
		t.Errorf("%+v", p)
	}
	q := StabilityParams{MovingAverageDistance: 8}.WithDefaults()
	if q.MovingAverageDistance != 8 || q.Interval != DefaultStabilityParams().Interval {
		t.Errorf("%+v", q)
	}
}
