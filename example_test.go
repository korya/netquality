package netquality_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/korya/netquality"
)

func Example() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	res, err := netquality.Run(ctx, netquality.WellKnown("nq.example.com"), netquality.Options{
		MaxDuration: 10 * time.Second,
		MaxBytes:    100 << 20,
		MaxFlows:    8,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("idle %v, down %.1f Mbps / %.0f RPM (truncated=%v), up %.1f Mbps / %.0f RPM\n",
		res.Idle.Median, res.Download.ThroughputBPS/1e6, res.Download.RPM, res.Download.Truncated,
		res.Upload.ThroughputBPS/1e6, res.Upload.RPM)
}
