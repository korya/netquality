// Package netquality measures download/upload capacity, idle and loaded
// latency, jitter and responsiveness (RPM) of a network path by running the
// IETF "Responsiveness under Working Conditions" test
// (draft-ietf-ippm-responsiveness) against a compatible server.
//
// The library depends only on the standard library, emits no traffic besides
// the test itself, and bounds every run by duration, bytes and flow count
// before it starts. See Options for the limits and Result for how each
// number was obtained.
//
// Typical use:
//
//	res, err := netquality.Run(ctx, netquality.Cloudflare, netquality.Options{})
//	if err != nil { ... }
//	fmt.Printf("%.0f RPM, %.1f Mbps down\n", res.Download.RPM, res.Download.ThroughputBPS/1e6)
package netquality
