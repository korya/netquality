package netquality

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

// newTestServer starts an in-process nqserver over TLS with HTTP/2 and returns
// its target and a client that trusts it.
func newTestServer(t *testing.T, o server.Options) (Target, *http.Client) {
	t.Helper()
	if o.MaxClientBytes == 0 {
		o.MaxClientBytes = -1 // loopback moves gigabytes per run
	}
	srv := httptest.NewUnstartedServer(server.Handler(o))
	srv.EnableHTTP2 = true
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // aborted probes log TLS handshake errors
	srv.StartTLS()
	t.Cleanup(srv.Close)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server
	return Target{ConfigURL: srv.URL + server.ConfigPath}, &http.Client{Transport: tr}
}

func fastStability() StabilityParams {
	p := DefaultStabilityParams()
	p.Interval = 100 * time.Millisecond
	p.MaxProbesPerSecond = 20
	return p
}

func TestRunLoopback(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	var (
		mu     sync.Mutex
		events []Event
	)
	res, err := RunWithEvents(context.Background(), target, Options{
		HTTPClient:  client,
		MaxDuration: 3 * time.Second,
		MaxBytes:    1 << 40,
		MaxFlows:    4,
		Stability:   fastStability(),
	}, func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Idle == nil || res.Idle.Samples != DefaultIdleProbes || res.Idle.Stages == nil {
		t.Errorf("idle = %+v", res.Idle)
	}
	if res.SchemaVersion != ResultSchemaVersion || ResultSchemaVersion != 1 {
		t.Errorf("schema_version = %d", res.SchemaVersion)
	}
	for _, d := range []*DirectionResult{res.Download, res.Upload} {
		if d == nil {
			t.Fatal("direction missing")
		}
		if d.Bytes == 0 || d.ThroughputBPS == 0 || d.Intervals == 0 {
			t.Errorf("%s: %+v", d.Direction, d)
		}
		if d.Flows > 4 {
			t.Errorf("%s: flows %d > MaxFlows", d.Direction, d.Flows)
		}
		if d.HTTPVersion != "HTTP/2.0" {
			t.Errorf("%s: http version %q", d.Direction, d.HTTPVersion)
		}
		if d.Loaded.Foreign == nil || d.Loaded.Self == nil || d.Loaded.Combined == nil || d.RPM == 0 || d.SelfRPM == 0 {
			t.Errorf("%s: loaded latency incomplete: %+v", d.Direction, d.Loaded)
		}
		if d.Reason != ReasonNone && d.Reason != ReasonDurationCap {
			t.Errorf("%s: unexpected reason %q", d.Direction, d.Reason)
		}
		if d.Truncated != (d.Reason != ReasonNone) {
			t.Errorf("%s: truncated/reason mismatch", d.Direction)
		}
	}
	if res.Target.HTTPVersion != "HTTP/2.0" || len(res.Target.ResolvedIPs) == 0 {
		t.Errorf("target = %+v", res.Target)
	}
	if len(res.Target.LocalIPs) != 1 || res.Target.LocalIPs[0] != "127.0.0.1" {
		t.Errorf("local_ips = %v", res.Target.LocalIPs)
	}
	var phases []string
	for _, e := range events {
		if e.Kind == EventPhase {
			phases = append(phases, e.Phase)
		}
	}
	if got := strings.Join(phases, ","); got != "discover,idle,download,upload,done" {
		t.Errorf("phases = %s", got)
	}
}

func TestRunDirections(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	for _, tc := range []struct {
		dir            Directions
		wantDL, wantUL bool
	}{{Download, true, false}, {Upload, false, true}} {
		res, err := Run(context.Background(), target, Options{
			HTTPClient: client, Directions: tc.dir, IdleProbes: -1,
			MaxDuration: 500 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if (res.Download != nil) != tc.wantDL || (res.Upload != nil) != tc.wantUL {
			t.Errorf("%v: download=%v upload=%v", tc.dir, res.Download != nil, res.Upload != nil)
		}
		if res.Idle != nil {
			t.Error("idle should be skipped when IdleProbes < 0")
		}
	}
}

func TestBytesCap(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	res, err := Run(context.Background(), target, Options{
		HTTPClient: client, Directions: Download, IdleProbes: -1,
		MaxDuration: 10 * time.Second, MaxBytes: 4 << 20, Stability: fastStability(),
	})
	if err != nil {
		t.Fatal(err)
	}
	d := res.Download
	if !d.Truncated || d.Reason != ReasonBytesCap {
		t.Errorf("want bytes_cap truncation, got %+v", d)
	}
	if d.Bytes < 4<<20 || d.Bytes > 64<<20 {
		t.Errorf("bytes = %d", d.Bytes)
	}
	if d.ThroughputBPS == 0 {
		t.Error("mean throughput fallback missing")
	}
	if len(res.Warnings) == 0 {
		t.Error("cap must produce a warning")
	}
}

func TestDurationCap(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	p := fastStability()
	p.StdDevTolerance = 1e-9 // effectively never stable
	start := time.Now()
	res, err := Run(context.Background(), target, Options{
		HTTPClient: client, Directions: Upload, IdleProbes: -1,
		MaxDuration: 700 * time.Millisecond, MaxBytes: 1 << 40, Stability: p,
	})
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("run took %v", el)
	}
	if d := res.Upload; !d.Truncated || d.Reason != ReasonDurationCap || d.ThroughputStable {
		t.Errorf("%+v", d)
	}
}

func TestCancellation(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(400 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	res, err := Run(ctx, target, Options{
		HTTPClient: client, IdleProbes: -1, MaxDuration: 10 * time.Second, MaxBytes: 1 << 40, Stability: fastStability(),
	})
	el := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if res == nil || !res.Cancelled {
		t.Fatalf("want partial cancelled result, got %+v", res)
	}
	if el > 400*time.Millisecond+250*time.Millisecond {
		t.Errorf("cancellation took %v after cancel", el-400*time.Millisecond)
	}
	if res.Download == nil || res.Download.Reason != ReasonCancelled || res.Upload != nil {
		t.Errorf("download=%+v upload=%v", res.Download, res.Upload)
	}
	if len(res.Target.ResolvedIPs) == 0 || len(res.Target.LocalIPs) == 0 {
		t.Errorf("partial result must still say which network it ran on: %+v", res.Target)
	}
}

func TestMaxFlowsWithFakeClock(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	fc := newFakeClock()
	p := fastStability()
	p.StdDevTolerance = 1e-9
	p.RampGainTolerance = -1 // ramp to the cap regardless of gain
	done := make(chan *Result)
	go func() {
		res, _ := Run(context.Background(), target, Options{
			HTTPClient: client, Directions: Download, IdleProbes: -1, MaxFlows: 3,
			MaxDuration: 5 * time.Second, MaxBytes: 1 << 40, Stability: p, clock: fc,
		})
		done <- res
	}()
	for i := 0; i < 6; i++ {
		time.Sleep(50 * time.Millisecond)
		fc.tick()
	}
	select {
	case res := <-done:
		if d := res.Download; d.Flows != 3 || d.Intervals != 6 || d.Reason != ReasonDurationCap {
			t.Errorf("%+v", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not finish")
	}
}

func TestDiscoveryErrors(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusMovedPermanently)
		case "/invalid":
			_, _ = w.Write([]byte(`{"version":2}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer bad.Close()
	for _, path := range []string{"/redirect", "/invalid", "/missing"} {
		_, err := Run(context.Background(), Target{ConfigURL: bad.URL + path}, Options{})
		if err == nil {
			t.Errorf("%s: want error", path)
		}
	}
	if _, err := Run(context.Background(), Target{}, Options{}); err == nil {
		t.Error("empty target: want error")
	}
}

func TestResultJSONShape(t *testing.T) {
	res := Result{Download: &DirectionResult{Direction: "download", Reason: ReasonBytesCap, Truncated: true}}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["upload"]; ok {
		t.Error("absent direction must be omitted")
	}
	if !strings.HasPrefix(string(data), `{"schema_version":`) {
		t.Errorf("schema_version must be the first field: %.40s", data)
	}
	if tgt := m["target"].(map[string]any); tgt["local_ips"] != nil || tgt["resolved_ips"] != nil {
		t.Errorf("empty address lists must be omitted: %v", tgt)
	}
	withIPs, _ := json.Marshal(ResolvedTarget{LocalIPs: []string{"10.0.0.2"}, ResolvedIPs: []string{"1.2.3.4"}})
	if !strings.Contains(string(withIPs), `"resolved_ips":["1.2.3.4"]`) || !strings.Contains(string(withIPs), `"local_ips":["10.0.0.2"]`) {
		t.Errorf("json = %s", withIPs)
	}
	dl := m["download"].(map[string]any)
	for _, k := range []string{"throughput_bps", "mean_throughput_bps", "bytes", "duration_ns", "truncated", "reason", "loaded", "rpm", "foreign_rpm", "self_rpm", "throughput_confidence"} {
		if _, ok := dl[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	if dl["reason"] != "bytes_cap" {
		t.Error(dl["reason"])
	}
	for k := range dl {
		if strings.ToLower(k) != k || strings.Contains(k, "-") {
			t.Errorf("key %q is not snake_case", k)
		}
	}
}
