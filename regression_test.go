package netquality

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

// countingListener tracks connections accepted and currently open.
type countingListener struct {
	net.Listener
	opened, open atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.opened.Add(1)
	l.open.Add(1)
	return &countedConn{Conn: c, l: l}, nil
}

type countedConn struct {
	net.Conn
	l    *countingListener
	once sync.Once
}

func (c *countedConn) Close() error {
	c.once.Do(func() { c.l.open.Add(-1) })
	return c.Conn.Close()
}

// startCountingServer starts an in-process server behind a countingListener,
// optionally recording every request through rec.
func startCountingServer(t *testing.T, rec func(*http.Request)) (*httptest.Server, *countingListener) {
	t.Helper()
	h := server.Handler(server.Options{MaxClientBytes: -1})
	if rec != nil {
		inner := h
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec(r)
			inner.ServeHTTP(w, r)
		})
	}
	srv := httptest.NewUnstartedServer(h)
	cl := &countingListener{Listener: srv.Listener}
	srv.Listener = cl
	srv.EnableHTTP2 = true
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, cl
}

// eventually polls cond for up to d.
func eventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// TestNoLeaksAcrossRuns guards INV-4: after Run returns, no goroutine it
// started survives and every connection it opened is closed — measured over
// repeated runs in one process, the way an agent uses the library.
func TestNoLeaksAcrossRuns(t *testing.T) {
	srv, cl := startCountingServer(t, nil)
	target := Target{ConfigURL: srv.URL + server.ConfigPath}
	opts := Options{HTTPClient: insecureClient(), IdleProbes: 2, MaxFlows: 6,
		MaxDuration: 300 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability()}

	// Warm up once so lazily started runtime/net goroutines are in the baseline.
	if _, err := Run(context.Background(), target, opts); err != nil {
		t.Fatal(err)
	}
	if !eventually(3*time.Second, func() bool { return cl.open.Load() == 0 }) {
		t.Fatalf("connections still open after warm-up: %d", cl.open.Load())
	}
	runtime.GC()
	baseline := runtime.NumGoroutine()

	for i := 0; i < 8; i++ {
		o := opts
		if i%3 == 1 {
			o.Directions = Upload
		}
		ctx := context.Background()
		if i%4 == 3 { // cancelled runs must not leak either
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 150*time.Millisecond)
			defer cancel()
		}
		res, err := Run(ctx, target, o)
		if res == nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if !eventually(3*time.Second, func() bool { return cl.open.Load() == 0 }) {
		t.Errorf("connections leaked: %d still open after 8 runs", cl.open.Load())
	}
	runtime.GC()
	if !eventually(3*time.Second, func() bool { return runtime.NumGoroutine() <= baseline+2 }) {
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, true)
		t.Errorf("goroutines leaked: baseline %d, now %d\n%s", baseline, runtime.NumGoroutine(), buf[:n])
	}
	if cl.opened.Load() < 8*2 {
		t.Errorf("sanity: only %d connections were ever opened", cl.opened.Load())
	}
}

// TestWireContract pins the draft's MUSTs and the server spec's expectations
// about what the client sends: identity encoding on every request, octet
// stream uploads, plain GET/POST, no redirect following, and a brand-new
// connection for every idle and foreign probe.
func TestWireContract(t *testing.T) {
	type req struct {
		method, path, accept, ctype, ua string
	}
	var mu sync.Mutex
	var seen []req
	srv, cl := startCountingServer(t, func(r *http.Request) {
		mu.Lock()
		seen = append(seen, req{r.Method, r.URL.Path, r.Header.Get("Accept-Encoding"), r.Header.Get("Content-Type"), r.Header.Get("User-Agent")})
		mu.Unlock()
	})
	var probes atomic.Int64
	res, err := RunWithEvents(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
		HTTPClient: insecureClient(), IdleProbes: 3, MaxFlows: 3,
		MaxDuration: 400 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability(),
	}, func(e Event) {
		if e.Kind == EventProbe && (e.ProbeKind == "idle" || e.ProbeKind == "foreign") {
			probes.Add(1)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var uploads, larges, smalls, configs int
	for _, r := range seen {
		if r.accept != "identity" {
			t.Errorf("%s %s: Accept-Encoding %q, draft requires identity", r.method, r.path, r.accept)
		}
		if !strings.HasPrefix(r.ua, "netquality-go/") {
			t.Errorf("%s %s: User-Agent %q", r.method, r.path, r.ua)
		}
		switch r.path {
		case server.ConfigPath:
			configs++
			if r.method != http.MethodGet {
				t.Errorf("config fetched with %s", r.method)
			}
		case server.SmallPath:
			smalls++
			if r.method != http.MethodGet {
				t.Errorf("probe with %s", r.method)
			}
		case server.LargePath:
			larges++
			if r.method != http.MethodGet {
				t.Errorf("download with %s", r.method)
			}
		case server.UploadPath:
			uploads++
			if r.method != http.MethodPost || r.ctype != "application/octet-stream" {
				t.Errorf("upload: %s %q", r.method, r.ctype)
			}
		default:
			t.Errorf("unexpected request to %s", r.path)
		}
	}
	if configs != 1 || larges == 0 || uploads == 0 || smalls == 0 {
		t.Errorf("config=%d large=%d upload=%d small=%d", configs, larges, uploads, smalls)
	}
	if larges > 6 || uploads > 6 {
		t.Errorf("more load requests than flows: large=%d upload=%d (finite object re-requests aside)", larges, uploads)
	}
	// Every idle/foreign probe is a fresh connection: connections opened must
	// cover them plus at least the load flows (config fetch and self probes
	// reuse). Self probes ride the load connections.
	if cl.opened.Load() < probes.Load()+int64(res.Download.Flows)+int64(res.Upload.Flows) {
		t.Errorf("only %d connections for %d fresh probes + %d/%d flows: probes reused connections",
			cl.opened.Load(), probes.Load(), res.Download.Flows, res.Upload.Flows)
	}
	if res.Download.Loaded.Self == nil || res.Upload.Loaded.Self == nil {
		t.Error("self probes must ride the load connections (HTTP/2)")
	}
}

// TestRepeatedRunsAreIndependent guards against global state: differently
// configured runs in one process must not influence each other.
func TestRepeatedRunsAreIndependent(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	for i := 0; i < 6; i++ {
		flows := 1 + i%4
		res, err := Run(context.Background(), target, Options{HTTPClient: client, Directions: Download, IdleProbes: 1 + i,
			MaxFlows: flows, MaxDuration: 250 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability()})
		if err != nil {
			t.Fatal(err)
		}
		if res.Idle.Samples != 1+i || res.Download.Flows > flows {
			t.Errorf("run %d: idle=%d (want %d) flows=%d (max %d)", i, res.Idle.Samples, 1+i, res.Download.Flows, flows)
		}
		if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "duration cap") {
			t.Errorf("run %d: warnings must not accumulate across runs: %v", i, res.Warnings)
		}
	}
}
