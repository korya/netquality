package netquality

import "github.com/korya/netquality/internal/engine"

// The engine holds the measurement algorithm and its own value types; the
// public types are declared in this package, rather than aliased, so that
// pkg.go.dev documents them and a refactor inside internal/engine cannot
// silently change the public API. These converters are the only seam between
// the two. A renamed engine field fails to compile here; one added and not
// carried across fails the conversion tests in convert_test.go.

func (p StabilityParams) toEngine() engine.StabilityParams {
	return engine.StabilityParams{
		MovingAverageDistance: p.MovingAverageDistance,
		Interval:              p.Interval,
		TrimmedMeanPercent:    p.TrimmedMeanPercent,
		StdDevTolerance:       p.StdDevTolerance,
		InitialFlows:          p.InitialFlows,
		FlowIncrement:         p.FlowIncrement,
		MaxProbesPerSecond:    p.MaxProbesPerSecond,
		ProbeCapacityPercent:  p.ProbeCapacityPercent,
		SendBufferBytes:       p.SendBufferBytes,
		RampGainTolerance:     p.RampGainTolerance,
		ChangeTolerance:       p.ChangeTolerance,
	}
}

func stabilityFrom(p engine.StabilityParams) StabilityParams {
	return StabilityParams{
		MovingAverageDistance: p.MovingAverageDistance,
		Interval:              p.Interval,
		TrimmedMeanPercent:    p.TrimmedMeanPercent,
		StdDevTolerance:       p.StdDevTolerance,
		InitialFlows:          p.InitialFlows,
		FlowIncrement:         p.FlowIncrement,
		MaxProbesPerSecond:    p.MaxProbesPerSecond,
		ProbeCapacityPercent:  p.ProbeCapacityPercent,
		SendBufferBytes:       p.SendBufferBytes,
		RampGainTolerance:     p.RampGainTolerance,
		ChangeTolerance:       p.ChangeTolerance,
	}
}

func latencyStatsFrom(s engine.LatencyStats) LatencyStats {
	out := LatencyStats{
		Samples: s.Samples,
		Min:     s.Min,
		Median:  s.Median,
		Mean:    s.Mean,
		P80:     s.P80,
		P90:     s.P90,
		P95:     s.P95,
		P99:     s.P99,
		Max:     s.Max,
		Jitter:  s.Jitter,
	}
	if s.Stages != nil {
		out.Stages = &StageMedians{
			DNS:       s.Stages.DNS,
			Connect:   s.Stages.Connect,
			TLS:       s.Stages.TLS,
			TLSPerRTT: s.Stages.TLSPerRTT,
			TTFB:      s.Stages.TTFB,
		}
	}
	return out
}
