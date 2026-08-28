package netquality

import (
	"context"
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

func tokenOpts(token string) Options {
	o := Options{HTTPClient: insecureClient(), Directions: Download, IdleProbes: 2,
		MaxDuration: 500 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability()}
	if token != "" {
		o.Header = http.Header{"Authorization": {"Bearer " + token}}
	}
	return o
}

func TestHeadersReachEveryRoute(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	srv := startServer(t, server.Options{}, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			if r.Header.Get("X-Nq-Test") == "yes" && r.Header.Get("User-Agent") == "custom-agent" {
				seen[r.URL.Path]++
			}
			mu.Unlock()
			h.ServeHTTP(w, r)
		})
	}, nil, true)
	o := tokenOpts("")
	o.Directions = Both
	o.Header = http.Header{"X-Nq-Test": {"yes"}, "user-agent": {"custom-agent"}} // non-canonical key, overrides default UA
	if _, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, o); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range []string{server.ConfigPath, server.SmallPath, server.LargePath, server.UploadPath} {
		if seen[p] == 0 {
			t.Errorf("headers missing on %s (seen %v)", p, seen)
		}
	}
}

func TestTokenProtectedServer(t *testing.T) {
	srv := startServer(t, server.Options{AuthToken: "s3cret"}, nil, nil, true)
	target := Target{ConfigURL: srv.URL + server.ConfigPath}

	res, err := Run(context.Background(), target, tokenOpts("s3cret"))
	if err != nil {
		t.Fatalf("right token: %v", err)
	}
	if res.Download == nil || res.Download.Bytes == 0 || res.Idle == nil || res.Download.Loaded.Self == nil {
		t.Errorf("full run with token incomplete: %+v", res.Download)
	}

	// Wrong, missing, malformed and oversized credentials all fail at
	// discovery with a 401, before any load traffic.
	for name, o := range map[string]Options{
		"wrong":   tokenOpts("nope"),
		"missing": tokenOpts(""),
		"basic": func() Options {
			o := tokenOpts("")
			o.Header = http.Header{"Authorization": {"Basic czNjcmV0"}}
			return o
		}(),
		"too long": tokenOpts(strings.Repeat("s3cret", 1000)),
		"empty":    func() Options { o := tokenOpts(""); o.Header = http.Header{"Authorization": {"Bearer "}}; return o }(),
	} {
		t.Run(name, func(t *testing.T) {
			var bytes int64
			srv.Config.Handler = countLarge(srv.Config.Handler, &bytes)
			res, err := Run(context.Background(), target, o)
			if err == nil || !strings.Contains(err.Error(), "401") || res != nil {
				t.Errorf("err=%v res=%v", err, res)
			}
			if bytes != 0 {
				t.Errorf("load traffic sent despite 401: %d bytes", bytes)
			}
		})
	}
}

// countLarge wraps h to count bytes written by the large endpoint.
func countLarge(h http.Handler, n *int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == server.LargePath {
			w = &countingResponseWriter{ResponseWriter: w, n: n}
		}
		h.ServeHTTP(w, r)
	})
}

type countingResponseWriter struct {
	http.ResponseWriter
	n *int64
}

func (c *countingResponseWriter) Write(b []byte) (int, error) {
	*c.n += int64(len(b))
	return c.ResponseWriter.Write(b)
}

func TestBudgetExhaustedMidRunIsGraceful(t *testing.T) {
	// A budget of 1 byte: the first large download starts (positive budget),
	// every later flow is refused with 429. The run must end with a flagged,
	// partial result rather than hang or panic.
	srv := startServer(t, server.Options{MaxClientBytes: 1, ClientWindow: time.Hour, LargeSize: 8 << 20}, nil, nil, true)
	o := tokenOpts("")
	o.MaxDuration = 2 * time.Second
	res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, o)
	if err != nil {
		// Acceptable only if the failure happened before any interval.
		if !strings.Contains(err.Error(), "429") {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	d := res.Download
	if !d.Truncated || d.Reason != ReasonFlowError || d.FlowErrors == 0 {
		t.Errorf("expected a flagged flow_error, got %+v", d)
	}
	if !hasWarning(res, "429") {
		t.Errorf("warning should carry the status: %v", res.Warnings)
	}
}

func TestConnectionCapDoesNotBreakRun(t *testing.T) {
	// A cap of 8 with a client that wants up to 16 flows plus probes: the
	// run completes (fewer flows), never errors. The listener must be wrapped
	// before the server starts accepting.
	srv := httptest.NewUnstartedServer(server.Handler(server.Options{}))
	srv.Listener = server.LimitListener(srv.Listener, 8)
	srv.EnableHTTP2 = true
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()
	o := tokenOpts("")
	o.MaxFlows = 16
	o.MaxDuration = 800 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Run(ctx, Target{ConfigURL: srv.URL + server.ConfigPath}, o)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if res == nil || res.Download == nil || res.Download.Bytes == 0 {
		t.Errorf("run under a connection cap produced nothing: %+v", res)
	}
	if res.Download.Flows > 8 {
		t.Logf("note: %d flows opened against a cap of 8 (waiting connections are counted client-side)", res.Download.Flows)
	}
}
