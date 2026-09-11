package engine

import (
	"testing"
	"time"
)

func BenchmarkEngineInterval(b *testing.B) {
	e := New(DefaultStabilityParams(), 32)
	o := Observation{Elapsed: time.Second, Flows: 1, Bytes: 1 << 20}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			e.Interval(o)
		}
	})
}

func BenchmarkEngineProbeGap(b *testing.B) {
	e := New(DefaultStabilityParams(), 32)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			e.ProbeGap(5000, 1000)
		}
	})
}
