package netquality

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLive hits the public Apple and Cloudflare servers. Opt in with NQ_LIVE=1.
func TestLive(t *testing.T) {
	if os.Getenv("NQ_LIVE") == "" {
		t.Skip("set NQ_LIVE=1 to run against public servers")
	}
	for name, target := range map[string]Target{"apple": Apple, "cloudflare": Cloudflare} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			res, err := Run(ctx, target, Options{MaxDuration: 8 * time.Second, MaxBytes: 100 << 20})
			if err != nil {
				t.Fatal(err)
			}
			if res.Idle == nil || res.Idle.Median <= 0 {
				t.Errorf("idle: %+v", res.Idle)
			}
			for _, d := range []*DirectionResult{res.Download, res.Upload} {
				if d == nil || d.ThroughputBPS <= 0 || d.RPM <= 0 {
					t.Errorf("%+v", d)
				}
			}
			t.Logf("%s: idle %v, down %.1f Mbps / %.0f RPM, up %.1f Mbps / %.0f RPM, warnings %v",
				name, res.Idle.Median, res.Download.ThroughputBPS/1e6, res.Download.RPM,
				res.Upload.ThroughputBPS/1e6, res.Upload.RPM, res.Warnings)
		})
	}
}
