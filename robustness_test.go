package netquality

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

// Real-world failure modes the client must survive gracefully.

func robustOpts(dir Directions) Options {
	return Options{HTTPClient: insecureClient(), Directions: dir, IdleProbes: -1,
		MaxDuration: 1500 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability()}
}

func TestUploadRejectedMidStream(t *testing.T) {
	// The server reads 1 MiB of each upload and then answers 413. Upload flows
	// fail one after another; the phase ends flagged and the download result
	// (run first) is intact.
	srv := startServer(t, server.Options{}, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == server.UploadPath {
				_, _ = io.CopyN(io.Discard, r.Body, 1<<20)
				http.Error(w, "too large", http.StatusRequestEntityTooLarge)
				return
			}
			h.ServeHTTP(w, r)
		})
	}, nil, true)
	o := robustOpts(Both)
	start := time.Now()
	res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, o)
	if res == nil {
		t.Fatalf("partial result must survive an upload failure: %v", err)
	}
	if res.Download == nil || res.Download.Bytes == 0 || res.Download.Reason == ReasonFlowError {
		t.Errorf("download must be unaffected: %+v", res.Download)
	}
	if u := res.Upload; u == nil || u.Reason != ReasonFlowError || !u.Truncated || u.FlowErrors == 0 {
		t.Errorf("upload should be a flagged flow error: %+v", u)
	}
	if err == nil || !strings.Contains(err.Error(), "413") {
		t.Errorf("error must carry the status: %v", err)
	}
	if !hasWarning(res, "413") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	if time.Since(start) > 2*o.MaxDuration+2*time.Second {
		t.Errorf("run took %v: rejected uploads must not stall", time.Since(start))
	}
}

func TestStalledLargeBody(t *testing.T) {
	// Headers arrive, the body never does. The phase must end at MaxDuration
	// with duration_cap and an honest ~0 throughput — not hang.
	srv := startServer(t, server.Options{}, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == server.LargePath {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusOK)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				<-r.Context().Done()
				return
			}
			h.ServeHTTP(w, r)
		})
	}, nil, true)
	o := robustOpts(Download)
	start := time.Now()
	var foreignProbes atomic.Int64
	res, err := RunWithEvents(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, o, func(e Event) {
		if e.Kind == EventProbe && e.ProbeKind == "foreign" {
			foreignProbes.Add(1)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	el := time.Since(start)
	if el > o.MaxDuration+time.Second {
		t.Errorf("stalled body must end at MaxDuration, took %v", el)
	}
	d := res.Download
	if !d.Truncated || d.Reason != ReasonDurationCap || d.ThroughputConfidence == ConfidenceHigh {
		t.Errorf("stalled download must be honest: %+v", d)
	}
	// Not one payload byte arrived, so every throughput figure must be zero.
	// Probe cost is charged to MaxBytes but is never goodput; counting it made
	// a stalled link read as ~500 kbps and, via ProbeGap, throttled probing to
	// one probe per second.
	if d.ThroughputBPS != 0 || d.MeanThroughputBPS != 0 || d.PeakThroughputBPS != 0 {
		t.Errorf("stalled download must report zero throughput, not its own probe traffic: %+v", d)
	}
	if d.Bytes == 0 {
		t.Error("probe cost must still be charged to the phase's byte total")
	}
	// Probes keep running while the body is stalled.
	if foreignProbes.Load() < 5 || d.Loaded.Foreign == nil {
		t.Errorf("foreign probes must keep running while the body is stalled: %d probes, loaded=%+v",
			foreignProbes.Load(), d.Loaded.Foreign)
	}
}

func TestRedirectOnTestURLIsAFlowError(t *testing.T) {
	// SERVER_SPEC: redirects fail the client. We never follow them.
	srv := startServer(t, server.Options{}, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == server.LargePath && r.URL.Query().Get("r") == "" {
				http.Redirect(w, r, server.LargePath+"?r=1", http.StatusMovedPermanently)
				return
			}
			h.ServeHTTP(w, r)
		})
	}, nil, true)
	_, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, robustOpts(Download))
	if err == nil || !strings.Contains(err.Error(), "301") {
		t.Errorf("redirect must fail the flow with its status, got %v", err)
	}
}

func TestUnresolvableTestHostFailsFast(t *testing.T) {
	// The config is fine but its URLs name a dead host and there is no
	// test_endpoint. Discovery succeeds; the first flow fails at DNS; the run
	// returns quickly with a clear error rather than burning the budget.
	srv := startServer(t, server.Options{BaseURL: "https://nq.invalid:1"}, nil, nil, true)
	o := robustOpts(Download)
	o.IdleProbes = 2
	o.MaxDuration = 10 * time.Second
	start := time.Now()
	res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, o)
	el := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "nq.invalid") {
		t.Fatalf("error should name the host: %v", err)
	}
	if res == nil || res.Download == nil || res.Download.Reason != ReasonFlowError || res.Idle != nil {
		t.Errorf("partial result must be flagged, idle absent (all probes failed): %+v", res)
	}
	if !hasWarning(res, "idle latency") {
		t.Errorf("idle failure must be a warning: %v", res.Warnings)
	}
	if el > 5*time.Second {
		t.Errorf("unresolvable host took %v; must fail fast", el)
	}
}

func TestUploadCancellation(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(400 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	res, err := Run(ctx, target, Options{HTTPClient: client, Directions: Upload, IdleProbes: -1,
		MaxDuration: 10 * time.Second, MaxBytes: 1 << 40, Stability: fastStability()})
	el := time.Since(start)
	if !errors.Is(err, context.Canceled) || res == nil || !res.Cancelled {
		t.Fatalf("err=%v res=%+v", err, res)
	}
	if el > 400*time.Millisecond+250*time.Millisecond {
		t.Errorf("upload cancellation took %v after cancel (upload body must stop on ctx)", el-400*time.Millisecond)
	}
	if res.Upload == nil || res.Upload.Reason != ReasonCancelled || res.Upload.Bytes == 0 {
		t.Errorf("%+v", res.Upload)
	}
}

func TestHTTP11FallbackUpload(t *testing.T) {
	srv := startServer(t, server.Options{}, nil, nil, false)
	res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, robustOpts(Upload))
	if err != nil {
		t.Fatal(err)
	}
	u := res.Upload
	if u.HTTPVersion != "HTTP/1.1" || u.Bytes == 0 || u.Loaded.Self != nil || u.Loaded.Foreign == nil || u.RPM != u.ForeignRPM {
		t.Errorf("h1 upload: %+v", u)
	}
	if !hasWarning(res, "HTTP/1.1") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}
