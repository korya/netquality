package netquality

import (
	"context"
	"crypto/tls"
	"fmt"
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
	return &countedConn{Conn: c, open: &l.open}, nil
}

// countedConn decrements open exactly once, however often it is closed.
type countedConn struct {
	net.Conn
	open *atomic.Int64
	once sync.Once
}

func (c *countedConn) Close() error {
	c.once.Do(func() { c.open.Add(-1) })
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

// countingDialer wraps the caller's transport dialer so a test can see exactly
// which sockets the library opened and whether it closed them. This is the
// side INV-4 is about: the client's own file descriptors. The peer's socket is
// not ours to promise anything about — aborting a download closes the socket
// with unread data still buffered, which is an abortive close (RST), and
// whether the server's socket then goes away is up to the server and the
// kernel, not to us.
type countingDialer struct {
	opened, open atomic.Int64
	inner        net.Dialer
}

func (d *countingDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := d.inner.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	d.opened.Add(1)
	d.open.Add(1)
	return &countedConn{Conn: c, open: &d.open}, nil
}

// countingClient returns a client that trusts the test server and reports its
// socket usage through the returned dialer.
func countingClient() (*http.Client, *countingDialer) {
	d := &countingDialer{inner: net.Dialer{Timeout: 10 * time.Second}}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server
	tr.DialContext = d.DialContext
	return &http.Client{Transport: tr}, d
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

// countingTLSClient is countingClient with the sockets opened through
// DialTLSContext, which net/http uses instead of DialContext for https.
func countingTLSClient() (*http.Client, *countingDialer) {
	d := &countingDialer{inner: net.Dialer{Timeout: 10 * time.Second}}
	tr := &http.Transport{ForceAttemptHTTP2: true} // IdleConnTimeout 0: idle conns never expire on their own
	tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		tc := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2", "http/1.1"}}) //nolint:gosec // test server
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		return tc, nil
	}
	return &http.Client{Transport: tr}, d
}

// TestNoLeaksAcrossRuns guards INV-4: after Run returns, no goroutine it
// started survives and every connection it opened is closed — measured over
// repeated runs in one process, the way an agent uses the library.
func TestNoLeaksAcrossRuns(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client func() (*http.Client, *countingDialer)
		settle bool
	}{{"DialContext", countingClient, false}, {"DialTLSContext", countingTLSClient, true}} {
		t.Run(tc.name, func(t *testing.T) { testNoLeaksAcrossRuns(t, tc.client, tc.settle) })
	}
}

// settle allows the sockets to reach zero shortly after Run returns instead of
// at the instant it does. It is set for a caller-supplied TLS dialer: net/http
// dials in a goroutine that outlives the cancelled request that started it, so
// when a phase ends, that goroutine can still be inside the caller's dialer,
// handshaking a socket the library has not been given and cannot close. What
// the library owes is that such a connection dies the moment it is handed over
// (ownedTransport.closed), never that it was never opened.
func testNoLeaksAcrossRuns(t *testing.T, newClient func() (*http.Client, *countingDialer), settle bool) {
	srv, _ := startCountingServer(t, nil)
	target := Target{ConfigURL: srv.URL + server.ConfigPath}
	client, dialer := newClient()
	opts := Options{HTTPClient: client, IdleProbes: 2, MaxFlows: 6,
		MaxDuration: 300 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability()}

	// Warm up once so lazily started runtime/net goroutines are in the baseline.
	if _, err := Run(context.Background(), target, opts); err != nil {
		t.Fatal(err)
	}
	if !eventually(3*time.Second, func() bool { return dialer.open.Load() == 0 }) {
		t.Fatalf("sockets still open after warm-up: %d", dialer.open.Load())
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
		// INV-4 is a promise about the moment Run returns, so check it then —
		// not after a grace period (see settle for the one exception).
		if settle {
			if !eventually(3*time.Second, func() bool { return dialer.open.Load() == 0 }) {
				t.Fatalf("run %d: %d sockets still open after Run returned", i, dialer.open.Load())
			}
		} else if n := dialer.open.Load(); n != 0 {
			t.Fatalf("run %d: %d sockets still open when Run returned", i, n)
		}
	}
	if n := dialer.open.Load(); n != 0 {
		t.Errorf("sockets leaked: %d still open after 8 runs", n)
	}
	runtime.GC()
	if !eventually(3*time.Second, func() bool { return runtime.NumGoroutine() <= baseline+2 }) {
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, true)
		t.Errorf("goroutines leaked: baseline %d, now %d\n%s", baseline, runtime.NumGoroutine(), buf[:n])
	}
	if dialer.opened.Load() < 8*2 {
		t.Errorf("sanity: only %d connections were ever opened", dialer.opened.Load())
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

// runBudget is the per-direction budget of run i. Each run gets a different one
// so that its duration-cap warning names it: that turns "did this result carry
// a previous run's warnings?" into something the text answers directly. Every
// budget is short enough that the phase cannot reach MovingAverageDistance
// intervals, so the cap always trips.
func runBudget(i int) time.Duration {
	return 250*time.Millisecond + time.Duration(i)*10*time.Millisecond
}

// TestRepeatedRunsAreIndependent guards against global state: differently
// configured runs in one process must not influence each other.
func TestRepeatedRunsAreIndependent(t *testing.T) {
	const runs = 6
	target, client := newTestServer(t, server.Options{})
	for i := 0; i < runs; i++ {
		flows := 1 + i%4
		res, err := Run(context.Background(), target, Options{HTTPClient: client, Directions: Download, IdleProbes: 1 + i,
			MaxFlows: flows, MaxDuration: runBudget(i), MaxBytes: 1 << 40, Stability: fastStability()})
		if err != nil {
			t.Fatal(err)
		}
		if res.Idle.Samples != 1+i || res.Download.Flows > flows {
			t.Errorf("run %d: idle=%d (want %d) flows=%d (max %d)", i, res.Idle.Samples, 1+i, res.Download.Flows, flows)
		}
		// This run's own warning must be there...
		mine := fmt.Sprintf("duration cap (%s)", runBudget(i))
		if !hasWarning(res, mine) {
			t.Errorf("run %d: expected %q among %v", i, mine, res.Warnings)
		}
		// ...and nobody else's. A Result that carried warnings over from an
		// earlier run would name that run's budget, which is the leak this
		// test exists to catch. Counting warnings instead only tracked how many
		// kinds a short phase happens to emit, which legitimately varies: a
		// probe series with no sample inside the working-conditions window adds
		// a second warning depending on how the probes fell (see #32).
		for j := 0; j < runs; j++ {
			if j == i {
				continue
			}
			if other := fmt.Sprintf("duration cap (%s)", runBudget(j)); hasWarning(res, other) {
				t.Errorf("run %d carries run %d's warning %q: %v", i, j, other, res.Warnings)
			}
		}
	}
}

// TestProbeCostIsNotGoodput pins the split between the two byte totals: the
// byte cap counts the draft's fixed per-probe estimate (LIM-2), goodput counts
// only what the load flows moved (LOAD-4). Merging them let a phase measure
// its own probe traffic as capacity.
func TestProbeCostIsNotGoodput(t *testing.T) {
	var tripped atomic.Bool
	c := &byteCounter{limit: 10_000, onLimit: func() { tripped.Store(true) }}
	c.addProbe(foreignProbeBytes)
	c.addProbe(selfProbeBytes)
	if c.get() != 6000 || c.payloadBytes() != 0 {
		t.Errorf("probe cost: total=%d payload=%d, want 6000/0", c.get(), c.payloadBytes())
	}
	if tripped.Load() {
		t.Error("cap tripped early")
	}
	c.add(3000)
	if c.get() != 9000 || c.payloadBytes() != 3000 {
		t.Errorf("payload: total=%d payload=%d, want 9000/3000", c.get(), c.payloadBytes())
	}
	// The cap must fire on the combined total, probe cost included.
	c.addProbe(foreignProbeBytes)
	if !tripped.Load() {
		t.Error("byte cap must count probe cost")
	}
}
