package netquality

import (
	"github.com/korya/netquality/internal/engine"
	"log/slog"
	"net/http"
	"time"
)

// Directions selects which load phases run. Phases always run sequentially.
type Directions int

const (
	// Both runs download then upload (default).
	Both Directions = iota
	// Download runs only the download phase.
	Download
	// Upload runs only the upload phase.
	Upload
)

func (d Directions) String() string {
	switch d {
	case Download:
		return "download"
	case Upload:
		return "upload"
	default:
		return "both"
	}
}

// StabilityParams are the draft's algorithm parameters (Section 5.2); see the
// engine package for field documentation.
type StabilityParams = engine.StabilityParams

// DefaultStabilityParams returns the draft-09 defaults, except Interval, which
// is 1s instead of 5s so that a phase can stabilise within the 12s MaxDuration
// budget (see README "Deviations").
func DefaultStabilityParams() StabilityParams { return engine.DefaultStabilityParams() }

// Default safety limits and probe counts.
const (
	DefaultMaxDuration = 12 * time.Second
	// DefaultMaxBytes is 0: no byte cap by default. Time is the budget —
	// cost is at most rate × MaxDuration per direction — so a confident
	// result is reachable at any link speed. Callers on metered links set
	// MaxBytes explicitly.
	DefaultMaxBytes   = 0
	DefaultMaxFlows   = 16
	DefaultIdleProbes = 5
	// DefaultConfigTimeout bounds config discovery, which happens before the
	// per-direction budgets apply.
	DefaultConfigTimeout = 10 * time.Second
)

// Options configures a test run. The zero value is valid and uses the defaults.
type Options struct {
	// MaxDuration bounds each direction's load phase (default 12s). A phase that
	// has not stabilised when the bound hits is reported as truncated.
	MaxDuration time.Duration
	// MaxBytes, if > 0, bounds the bytes moved by each direction's load
	// phase, probes included; hitting it truncates the phase with
	// reason=bytes_cap. 0 (the default) means no byte cap: MaxDuration alone
	// bounds the run. Set it on metered links.
	MaxBytes int64
	// MaxFlows caps the number of concurrent load-generating connections
	// (default 16, draft MNP).
	MaxFlows int
	// Directions selects the phases to run (default Both).
	Directions Directions
	// IdleProbes is the number of fresh-connection probes for idle latency
	// (default 5). 0 uses the default; negative skips idle measurement.
	IdleProbes int
	// Stability holds the draft's algorithm parameters; zero fields use defaults.
	Stability StabilityParams
	// HTTPClient supplies the base transport (proxy, TLS config, dialer). Only
	// its Transport is used; each load flow gets its own clone so flows do not
	// share a connection. If the Transport is not an *http.Transport it is used
	// as-is and flows may share connections (a warning is recorded).
	HTTPClient *http.Client
	// Logger receives debug logs; nil discards them.
	Logger *slog.Logger
	// ConfigTimeout bounds config discovery (default 10s).
	ConfigTimeout time.Duration
	// Header is added to every request the test sends (config fetch, probes,
	// load flows): credentials for a protected server, for example
	// Authorization: Bearer <token>. Keys set here override the defaults.
	// It is read from every flow and probe goroutine, so it must not be
	// mutated while Run is in flight.
	Header http.Header

	// clock is injectable for tests; nil means the wall clock.
	clock clock
}

func (o Options) withDefaults() Options {
	if o.MaxDuration <= 0 {
		o.MaxDuration = DefaultMaxDuration
	}
	if o.MaxBytes < 0 {
		o.MaxBytes = 0
	}
	if o.MaxFlows <= 0 {
		o.MaxFlows = DefaultMaxFlows
	}
	if o.IdleProbes == 0 {
		o.IdleProbes = DefaultIdleProbes
	}
	if o.ConfigTimeout <= 0 {
		o.ConfigTimeout = DefaultConfigTimeout
	}
	o.Stability = o.Stability.WithDefaults()
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{}
	}
	if o.Logger == nil {
		o.Logger = slog.New(discardHandler{})
	}
	if o.clock == nil {
		o.clock = realClock{}
	}
	return o
}

// clock abstracts time for tests.
type clock interface {
	Now() time.Time
	NewTicker(d time.Duration) ticker
	After(d time.Duration) <-chan time.Time
}

type ticker interface {
	C() <-chan time.Time
	Stop()
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) NewTicker(d time.Duration) ticker       { return realTicker{time.NewTicker(d)} }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type realTicker struct{ t *time.Ticker }

func (t realTicker) C() <-chan time.Time { return t.t.C }
func (t realTicker) Stop()               { t.t.Stop() }
