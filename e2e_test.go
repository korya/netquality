package netquality

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

// startServer starts an in-process nqserver with the given handler wrapper
// and TLS tweaks, returning the base URL.
func startServer(t *testing.T, o server.Options, wrap func(http.Handler) http.Handler, tlsCfg *tls.Config, h2 bool) *httptest.Server {
	t.Helper()
	if o.MaxClientBytes == 0 {
		o.MaxClientBytes = -1 // loopback moves gigabytes per run
	}
	h := server.Handler(o)
	if wrap != nil {
		h = wrap(h)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = h2
	srv.TLS = tlsCfg
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func insecureClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server
	return &http.Client{Transport: tr}
}

func TestHTTP11Fallback(t *testing.T) {
	srv := startServer(t, server.Options{}, nil, nil, false)
	res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
		HTTPClient: insecureClient(), Directions: Download, IdleProbes: 2,
		MaxDuration: 600 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability(),
	})
	if err != nil {
		t.Fatal(err)
	}
	d := res.Download
	if d.HTTPVersion != "HTTP/1.1" || res.Target.HTTPVersion != "HTTP/1.1" {
		t.Errorf("http version = %q / %q", d.HTTPVersion, res.Target.HTTPVersion)
	}
	if d.Loaded.Self != nil || d.SelfRPM != 0 {
		t.Errorf("self probes must be absent on HTTP/1.1: %+v", d.Loaded.Self)
	}
	if d.Loaded.Foreign == nil || d.ForeignRPM == 0 || d.RPM != d.ForeignRPM {
		t.Errorf("RPM must come from foreign probes only: rpm=%v foreign=%v", d.RPM, d.ForeignRPM)
	}
	if !hasWarning(res, "HTTP/1.1") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestFlowErrorAbortsPhase(t *testing.T) {
	srv := startServer(t, server.Options{}, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == server.LargePath {
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			h.ServeHTTP(w, r)
		})
	}, nil, true)
	res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
		HTTPClient: insecureClient(), IdleProbes: -1,
		MaxDuration: 2 * time.Second, MaxBytes: 1 << 40, Stability: fastStability(),
	})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("want flow error mentioning 503, got %v", err)
	}
	if res == nil || res.Download == nil || res.Download.Reason != ReasonFlowError || !res.Download.Truncated {
		t.Errorf("failed phase must still return a flagged partial result, got %+v", res)
	}
	if res.Upload != nil {
		t.Error("later phases do not run after a failed one")
	}
}

func TestFlowErrorAfterIntervalsKeepsResult(t *testing.T) {
	// The large download succeeds a few times, then the server starts failing:
	// the phase aborts with reason=flow_error but data gathered so far is kept.
	start := time.Now()
	srv := startServer(t, server.Options{LargeSize: 1 << 20}, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == server.LargePath {
				if time.Since(start) > 350*time.Millisecond {
					http.Error(w, "quota", http.StatusTooManyRequests)
					return
				}
			}
			h.ServeHTTP(w, r)
		})
	}, nil, true)
	res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
		HTTPClient: insecureClient(), Directions: Download, IdleProbes: -1,
		MaxDuration: 3 * time.Second, MaxBytes: 1 << 40, Stability: fastStability(),
	})
	if err != nil {
		t.Fatal(err)
	}
	d := res.Download
	if !d.Truncated || d.Reason != ReasonFlowError || d.FlowErrors == 0 || d.Bytes == 0 {
		t.Errorf("%+v", d)
	}
	if !hasWarning(res, "flow failed") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestTestEndpointHonoured(t *testing.T) {
	// Config URLs name an unresolvable host; test_endpoint points at loopback.
	srv := httptest.NewUnstartedServer(nil)
	srv.EnableHTTP2 = true
	base := "https://nq.invalid:" + srv.Listener.Addr().String()[strings.LastIndex(srv.Listener.Addr().String(), ":")+1:]
	srv.Config.Handler = server.Handler(server.Options{BaseURL: base, TestEndpoint: "127.0.0.1"})
	srv.StartTLS()
	defer srv.Close()
	res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
		HTTPClient: insecureClient(), Directions: Download, IdleProbes: 2,
		MaxDuration: 400 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Target.Host != strings.TrimPrefix(base, "https://") || res.Target.TestEndpoint != "127.0.0.1" {
		t.Errorf("target = %+v", res.Target)
	}
	if res.Idle == nil || res.Download.Bytes == 0 {
		t.Errorf("idle=%v bytes=%d", res.Idle, res.Download.Bytes)
	}
	if len(res.Target.ResolvedIPs) != 1 || res.Target.ResolvedIPs[0] != "127.0.0.1" {
		t.Errorf("resolved = %v", res.Target.ResolvedIPs)
	}
}

func TestTLS12Normalisation(t *testing.T) {
	srv := startServer(t, server.Options{}, nil, &tls.Config{MaxVersion: tls.VersionTLS12}, true) //nolint:gosec // deliberate
	res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
		HTTPClient: insecureClient(), Directions: Download, IdleProbes: 5,
		MaxDuration: 300 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability(),
	})
	if err != nil {
		t.Fatal(err)
	}
	st := res.Idle.Stages
	if st == nil || st.TLS == 0 || st.TLSPerRTT == 0 {
		t.Fatalf("stages = %+v", st)
	}
	// TLS 1.2 = 2 round trips: per-RTT value is half the handshake.
	if st.TLSPerRTT > st.TLS/2+time.Millisecond {
		t.Errorf("tls=%v per_rtt=%v: expected halving for TLS 1.2", st.TLS, st.TLSPerRTT)
	}
	// And on TLS 1.3 (default) it is the raw value.
	target, client := newTestServer(t, server.Options{})
	res13, err := Run(context.Background(), target, Options{HTTPClient: client, Directions: Download, IdleProbes: 3,
		MaxDuration: 200 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability()})
	if err != nil {
		t.Fatal(err)
	}
	if s := res13.Idle.Stages; s.TLSPerRTT != s.TLS {
		t.Errorf("TLS 1.3: tls=%v per_rtt=%v", s.TLS, s.TLSPerRTT)
	}
}

func TestUploadBytesCap(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	res, err := Run(context.Background(), target, Options{
		HTTPClient: client, Directions: Upload, IdleProbes: -1,
		MaxDuration: 10 * time.Second, MaxBytes: 4 << 20, Stability: fastStability(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := res.Upload; !d.Truncated || d.Reason != ReasonBytesCap || d.Bytes < 4<<20 || d.Bytes > 64<<20 {
		t.Errorf("%+v", d)
	}
}

type wrappedRT struct{ rt http.RoundTripper }

func (w wrappedRT) RoundTrip(r *http.Request) (*http.Response, error) { return w.rt.RoundTrip(r) }

func TestCustomRoundTripperWarns(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	client = &http.Client{Transport: wrappedRT{client.Transport}}
	res, err := Run(context.Background(), target, Options{
		HTTPClient: client, Directions: Download, IdleProbes: 2,
		MaxDuration: 400 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(res, "custom RoundTripper") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	if res.Download.Bytes == 0 || res.Idle == nil || res.Idle.Samples != 2 {
		t.Errorf("run must still work: %+v idle=%+v", res.Download, res.Idle)
	}
	if res.Idle.Stages != nil {
		t.Error("reused connections must not report stage timings")
	}
	if res.Download.Loaded.Foreign == nil {
		t.Error("foreign probes must still produce samples")
	}
	if res.Target.Proxy != nil {
		t.Error("proxy cannot be inspected through a custom RoundTripper")
	}
	if res.Target.LocalIPs != nil || res.Target.ResolvedIPs != nil {
		t.Errorf("addresses are unknowable through a custom RoundTripper: %+v", res.Target)
	}
}

func TestConfigTimeoutAndStatus(t *testing.T) {
	srv := startServer(t, server.Options{}, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("mode") {
			case "slow":
				select {
				case <-time.After(2 * time.Second):
				case <-r.Context().Done():
				}
				return
			case "busy":
				w.Header().Set("Retry-After", "30")
				http.Error(w, "busy", http.StatusTooManyRequests)
				return
			}
			h.ServeHTTP(w, r)
		})
	}, nil, true)
	client := insecureClient()
	start := time.Now()
	_, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath + "?mode=slow"},
		Options{HTTPClient: client, ConfigTimeout: 200 * time.Millisecond})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Errorf("slow config: err=%v after %v", err, time.Since(start))
	}
	_, err = Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath + "?mode=busy"}, Options{HTTPClient: client})
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("busy config: err=%v", err)
	}
}

func TestCancelDuringIdle(t *testing.T) {
	srv := startServer(t, server.Options{}, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == server.SmallPath {
				time.Sleep(30 * time.Millisecond)
			}
			h.ServeHTTP(w, r)
		})
	}, nil, true)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	res, err := Run(ctx, Target{ConfigURL: srv.URL + server.ConfigPath}, Options{HTTPClient: insecureClient(), IdleProbes: 1000})
	if !errors.Is(err, context.DeadlineExceeded) || res == nil || !res.Cancelled || res.Download != nil || res.Idle != nil {
		t.Errorf("err=%v res=%+v", err, res)
	}
}

func TestEventStream(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	var mu = make(chan Event, 10000)
	res, err := RunWithEvents(context.Background(), target, Options{
		HTTPClient: client, Directions: Download, IdleProbes: 2, MaxFlows: 3,
		MaxDuration: 500 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability(),
	}, func(e Event) { mu <- e })
	if err != nil {
		t.Fatal(err)
	}
	close(mu)
	counts := map[EventKind]int{}
	probeKinds := map[string]int{}
	var flows, intervals int
	for e := range mu {
		counts[e.Kind]++
		if e.Time.IsZero() {
			t.Error("event without time")
		}
		switch e.Kind {
		case EventProbe:
			probeKinds[e.ProbeKind]++
			if e.Latency <= 0 {
				t.Error("probe without latency")
			}
		case EventFlow:
			flows = e.Flows
		case EventInterval:
			intervals++
			if e.Flows == 0 || e.ThroughputBPS <= 0 || e.Interval != intervals {
				t.Errorf("interval event %+v", e)
			}
		}
		if b, err := json.Marshal(e); err != nil || !strings.Contains(string(b), `"kind"`) {
			t.Errorf("event json: %s %v", b, err)
		}
	}
	if flows != res.Download.Flows || intervals != res.Download.Intervals {
		t.Errorf("flows %d/%d intervals %d/%d", flows, res.Download.Flows, intervals, res.Download.Intervals)
	}
	if probeKinds["idle"] != 2 || probeKinds["foreign"] == 0 || probeKinds["self"] == 0 {
		t.Errorf("probe kinds = %v", probeKinds)
	}
	if counts[EventPhase] != 4 { // discover, idle, download, done
		t.Errorf("phase events = %d", counts[EventPhase])
	}
}

func TestMaxFlowsDefaultAndOptionsDefaults(t *testing.T) {
	o := Options{}.withDefaults()
	if o.MaxDuration != DefaultMaxDuration || o.MaxBytes != 0 || DefaultMaxBytes != 0 || o.MaxFlows != DefaultMaxFlows ||
		o.IdleProbes != DefaultIdleProbes || o.ConfigTimeout != DefaultConfigTimeout || o.HTTPClient == nil || o.Logger == nil || o.clock == nil {
		t.Errorf("%+v", o)
	}
	if (Options{IdleProbes: -1}).withDefaults().IdleProbes != -1 {
		t.Error("negative IdleProbes must survive defaults")
	}
	if (Options{MaxBytes: -5}).withDefaults().MaxBytes != 0 {
		t.Error("negative MaxBytes must mean unlimited")
	}
	for _, tc := range []struct {
		d    Directions
		want string
	}{{Both, "both"}, {Download, "download"}, {Upload, "upload"}} {
		if tc.d.String() != tc.want {
			t.Error(tc.d)
		}
		if b, _ := json.Marshal(tc.d); string(b) != `"`+tc.want+`"` {
			t.Errorf("json %s", b)
		}
	}
}

func hasWarning(res *Result, substr string) bool {
	for _, w := range res.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// TestTestEndpointCustomTLSDialer (DISC-9): a custom TLS dialer cannot honour
// test_endpoint, and the run says so instead of silently ignoring it.
func TestTestEndpointCustomTLSDialer(t *testing.T) {
	srv := startServer(t, server.Options{TestEndpoint: "192.0.2.1"}, nil, nil, true)
	client, _ := countingTLSClient()
	res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
		HTTPClient: client, Directions: Download, IdleProbes: 1,
		MaxDuration: 300 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(res, "custom TLS dialer") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	if res.Download.HTTPVersion != "HTTP/2.0" {
		t.Errorf("tracking must not cost HTTP/2: got %q", res.Download.HTTPVersion)
	}
}
