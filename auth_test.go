package netquality

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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
	var loadBytes atomic.Int64
	srv := startServer(t, server.Options{AuthToken: "s3cret"}, func(h http.Handler) http.Handler { return countLarge(h, &loadBytes) }, nil, true)
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
			res, err := Run(context.Background(), target, o)
			if err == nil || !strings.Contains(err.Error(), "401") || res != nil {
				t.Errorf("err=%v res=%v", err, res)
			}
			if moved := loadBytes.Load(); moved != 0 {
				t.Errorf("load traffic sent to an unauthenticated request: %d bytes", moved)
			}
		})
	}
}

// countLarge wraps h to count bytes written by the large endpoint to
// requests that do not carry the valid token. Authorised runs may still be
// flushing chunks into closing sockets after Run returns, so counting every
// write would race with the previous, legitimate run.
func countLarge(h http.Handler, n *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == server.LargePath && r.Header.Get("Authorization") != "Bearer s3cret" {
			w = &countingResponseWriter{ResponseWriter: w, n: n}
		}
		h.ServeHTTP(w, r)
	})
}

type countingResponseWriter struct {
	http.ResponseWriter
	n *atomic.Int64
}

func (c *countingResponseWriter) Write(b []byte) (int, error) {
	c.n.Add(int64(len(b)))
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
	srv := httptest.NewUnstartedServer(server.Handler(server.Options{MaxClientBytes: -1}))
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

// signedBackend serves a config document whose test URLs point at nq (an
// nqserver) and are signed with key for the given subject and expiry — the
// role a product backend plays in the signed-URL flow.
func signedBackend(t *testing.T, nqURL string, key []byte, exp time.Time, sub string) *httptest.Server {
	t.Helper()
	sign := func(path string) string {
		s, err := server.SignURL(key, nqURL+path, exp, sub)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	doc := map[string]any{"version": 1, "urls": map[string]string{
		"small_download_url": sign(server.SmallPath),
		"large_download_url": sign(server.LargePath),
		"upload_url":         sign(server.UploadPath),
	}}
	body, _ := json.Marshal(doc)
	b := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(b.Close)
	return b
}

var signKey = []byte("an-example-signing-key-of-32-byte")

func TestSignedURLsEndToEnd(t *testing.T) {
	nq := startServer(t, server.Options{SigningKeys: [][]byte{signKey}}, nil, nil, true)
	backend := signedBackend(t, nq.URL, signKey, time.Now().Add(5*time.Minute), "laptop-7")
	o := tokenOpts("") // no credential on the client at all
	o.Directions = Both
	res, err := Run(context.Background(), Target{ConfigURL: backend.URL + "/config"}, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []*DirectionResult{res.Download, res.Upload} {
		if d == nil || d.Bytes == 0 || d.FlowErrors != 0 || d.Loaded.Self == nil || d.Loaded.Foreign == nil {
			t.Errorf("signed run incomplete: %+v", d)
		}
	}
	if res.Idle == nil || res.Idle.Samples != 2 {
		t.Errorf("idle probes over signed small URL: %+v", res.Idle)
	}
	if !strings.Contains(res.Target.Config.LargeDownloadURL, "sig=") || res.Target.Host != strings.TrimPrefix(nq.URL, "https://") {
		t.Errorf("target = %+v", res.Target)
	}
}

func TestSignedURLsRejected(t *testing.T) {
	nq := startServer(t, server.Options{SigningKeys: [][]byte{signKey}}, nil, nil, true)
	cases := map[string]*httptest.Server{
		"expired":   signedBackend(t, nq.URL, signKey, time.Now().Add(-time.Hour), "d"),
		"wrong key": signedBackend(t, nq.URL, []byte("a-different-key-of-32-bytes-long"), time.Now().Add(time.Minute), "d"),
	}
	for name, backend := range cases {
		t.Run(name, func(t *testing.T) {
			o := tokenOpts("")
			o.IdleProbes = 2
			res, err := Run(context.Background(), Target{ConfigURL: backend.URL + "/config"}, o)
			// Discovery succeeds (the backend is open); every test request is
			// refused. Idle probes all fail (warning), the first flow fails
			// before any interval, so Run returns the 401 error.
			if err == nil || !strings.Contains(err.Error(), "401") {
				t.Errorf("err = %v (res=%v)", err, res)
			}
		})
	}
}
