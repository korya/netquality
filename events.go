package netquality

import "time"

// EventKind classifies an Event.
type EventKind string

const (
	// EventPhase marks a phase boundary; Phase names it, Message may add detail.
	EventPhase EventKind = "phase"
	// EventInterval reports the moving-average throughput after each interval.
	EventInterval EventKind = "interval"
	// EventProbe reports one latency sample.
	EventProbe EventKind = "probe"
	// EventFlow reports a flow being added (Flows is the new count).
	EventFlow EventKind = "flow"
	// EventWarning carries a warning as it is discovered.
	EventWarning EventKind = "warning"
)

// Event is a progress notification delivered to RunWithEvents' sink. Sinks are
// called synchronously from test goroutines and must return promptly.
type Event struct {
	Time      time.Time `json:"time"`
	Kind      EventKind `json:"kind"`
	Phase     string    `json:"phase"` // "discover", "idle", "download", "upload", "done"
	Direction string    `json:"direction,omitempty"`
	Message   string    `json:"message,omitempty"`
	// Interval fields.
	Interval      int     `json:"interval,omitempty"`
	Flows         int     `json:"flows,omitempty"`
	ThroughputBPS float64 `json:"throughput_bps,omitempty"`
	Bytes         int64   `json:"bytes,omitempty"`
	RPM           float64 `json:"rpm,omitempty"`
	// Probe fields.
	ProbeKind string        `json:"probe_kind,omitempty"` // "idle", "foreign", "self"
	Latency   time.Duration `json:"latency_ns,omitempty"`
}
