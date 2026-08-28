package netquality

import (
	"context"
	"time"
)

// Result is the outcome of a run. It serialises to JSON with stable snake_case
// names; the CLI's --json output is exactly this struct.
type Result struct {
	Target    ResolvedTarget `json:"target"`
	StartedAt time.Time      `json:"started_at"`
	Duration  time.Duration  `json:"duration_ns"`
	// Idle is the idle latency measured on fresh connections before any load.
	// Nil when IdleProbes < 0 or no probe succeeded.
	Idle *LatencyStats `json:"idle,omitempty"`
	// Download / Upload are nil when the direction was not requested.
	Download *DirectionResult `json:"download,omitempty"`
	Upload   *DirectionResult `json:"upload,omitempty"`
	// Cancelled is true when the context was cancelled; the result is partial.
	Cancelled bool     `json:"cancelled"`
	Warnings  []string `json:"warnings,omitempty"`
}

// ResolvedTarget describes the server actually used.
type ResolvedTarget struct {
	ConfigURL    string   `json:"config_url"`
	Host         string   `json:"host"`
	TestEndpoint string   `json:"test_endpoint,omitempty"`
	ResolvedIPs  []string `json:"resolved_ips,omitempty"`
	// HTTPVersion is the protocol negotiated by load flows ("HTTP/2.0", "HTTP/1.1").
	HTTPVersion string       `json:"http_version,omitempty"`
	Config      ServerConfig `json:"config"`
	// Proxy is set when the measured path involves a proxy: an explicit one
	// from the transport's Proxy function, or TLS interception inferred from
	// the certificate chain. Nil when nothing was detected. Numbers are still
	// valid measurements, but of the client→proxy leg.
	Proxy *ProxyInfo `json:"proxy,omitempty"`
}

// ProxyInfo describes a detected proxy. Explicit and TLSInterception may both
// be set.
type ProxyInfo struct {
	// Explicit is true when the HTTP transport routed requests via a proxy.
	Explicit bool `json:"explicit"`
	// URL of the explicit proxy, credentials removed.
	URL string `json:"url,omitempty"`
	// TLSInterception is true when the server certificate chain verified but
	// the leaf is not publicly trusted (no Certificate Transparency SCTs): a
	// TLS-inspecting proxy or a private CA re-issued it.
	TLSInterception bool `json:"tls_interception"`
	// Issuer of the intercepted leaf certificate.
	Issuer string `json:"issuer,omitempty"`
	// Reason is a human-readable explanation of the detection.
	Reason string `json:"reason,omitempty"`
}

// TruncationReason says why a phase ended before stabilising.
type TruncationReason string

const (
	ReasonNone        TruncationReason = ""
	ReasonBytesCap    TruncationReason = "bytes_cap"
	ReasonDurationCap TruncationReason = "duration_cap"
	ReasonCancelled   TruncationReason = "cancelled"
	ReasonFlowError   TruncationReason = "flow_error"
)

// DirectionResult is the outcome of one load phase.
type DirectionResult struct {
	Direction string `json:"direction"`
	// ThroughputBPS is the moving-average goodput (bits/s) over the last MAD
	// intervals at the end of the phase.
	ThroughputBPS float64 `json:"throughput_bps"`
	// PeakThroughputBPS is the highest single-interval goodput observed.
	PeakThroughputBPS float64 `json:"peak_throughput_bps"`
	// MeanThroughputBPS is Bytes*8/Duration over the whole phase, ramp-up
	// included. It is the only throughput figure when no interval completed.
	MeanThroughputBPS float64 `json:"mean_throughput_bps"`
	// Bytes moved by this phase (payload of load flows plus probe bodies).
	Bytes int64 `json:"bytes"`
	// Duration is the wall time of the phase.
	Duration time.Duration `json:"duration_ns"`
	// Flows is the number of load-generating connections in use at the end.
	Flows int `json:"flows"`
	// Intervals is the number of stability intervals that completed.
	Intervals int `json:"intervals"`
	// ThroughputStable / ResponsivenessStable report whether the draft's stability
	// criterion was met for each series.
	ThroughputStable     bool `json:"throughput_stable"`
	ResponsivenessStable bool `json:"responsiveness_stable"`
	// ThroughputConfidence / ResponsivenessConfidence per draft Section 5.4.1.
	ThroughputConfidence     Confidence `json:"throughput_confidence"`
	ResponsivenessConfidence Confidence `json:"responsiveness_confidence"`
	// Truncated is true when a limit ended the phase before both series were
	// stable; Reason says which limit.
	Truncated bool             `json:"truncated"`
	Reason    TruncationReason `json:"reason,omitempty"`
	// Loaded latency statistics gathered while the link was under load.
	Loaded LoadedLatency `json:"loaded"`
	// RPM is the draft's Responsiveness score: the mean of ForeignRPM and SelfRPM
	// (or ForeignRPM alone when self probes were unavailable).
	RPM float64 `json:"rpm"`
	// ForeignRPM = 60000 / mean(TM(tcp_f), TM(tls_f), TM(http_f)).
	ForeignRPM float64 `json:"foreign_rpm"`
	// SelfRPM = 60000 / TM(http_l). 0 when no self probes were possible.
	SelfRPM float64 `json:"self_rpm"`
	// HTTPVersion negotiated by this phase's flows.
	HTTPVersion string `json:"http_version,omitempty"`
	// FlowErrors counts load flows that ended with an error.
	FlowErrors int `json:"flow_errors"`
}

// LoadedLatency groups latency samples taken under load.
type LoadedLatency struct {
	// Foreign: fresh-connection probes (TCP+TLS+HTTP, like idle probes).
	Foreign *LatencyStats `json:"foreign,omitempty"`
	// Self: probes multiplexed on load-generating HTTP/2 connections.
	Self *LatencyStats `json:"self,omitempty"`
	// Combined: all probes, with foreign probes counted by their HTTP time
	// (http_f) so both kinds measure "request to full response".
	Combined *LatencyStats `json:"combined,omitempty"`
}

func ctxReason(ctx context.Context) TruncationReason {
	if ctx.Err() == context.DeadlineExceeded {
		return ReasonDurationCap
	}
	return ReasonCancelled
}
