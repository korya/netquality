package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/korya/netquality"
)

func TestPrintDirShowsBound(t *testing.T) {
	var b bytes.Buffer
	printDir(&b, "download", &netquality.DirectionResult{
		Direction: "download", ThroughputBPS: 94.1e6, RPM: 1720, Flows: 4,
		ThroughputConfidence: netquality.ConfidenceMedium, ResponsivenessConfidence: netquality.ConfidenceLow,
		ThroughputLowerBoundBPS: 88e6, RPMUpperBound: 1800,
		LowerBoundWindow: &netquality.IntervalWindow{Start: 3 * time.Second, Duration: 4 * time.Second, Intervals: 4},
	})
	if got := b.String(); !strings.Contains(got, "(>= 88.0 Mbps, <= 1800 RPM over 4s)") {
		t.Errorf("bound missing: %q", got)
	}
	b.Reset()
	printDir(&b, "upload", &netquality.DirectionResult{Direction: "upload", ThroughputBPS: 1e6})
	if got := b.String(); strings.Contains(got, ">=") {
		t.Errorf("bound printed without a window: %q", got)
	}
}
