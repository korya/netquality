package netquality

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"

	"github.com/korya/netquality/internal/engine"
)

// probeTimes collects httptrace timestamps. HTTP/2 invokes trace hooks from
// several goroutines, so every access is under the mutex.
type probeTimes struct {
	mu                                   sync.Mutex
	s                                    engine.LatencySample
	dnsStart, connStart, tlsStart, wrote instant
	reused                               bool
}

// foreignProbeBytes / selfProbeBytes are the draft's per-probe cost estimates
// (Section 5.3), used to keep probe traffic under PTC of capacity and to count
// probe bytes against MaxBytes.
const (
	foreignProbeBytes = 5000
	selfProbeBytes    = 1000
	// The draft uses one byte; Cloudflare's supported endpoint returns ten.
	maxProbeBodyBytes = 10
)

var errInvalidProbeSize = errors.New("invalid probe response size")

// foreignProbe performs a GET of the small URL on a brand-new connection and
// records per-stage timings. rt must not reuse connections.
// observe, if non-nil, receives the TLS state of every successful handshake.
func foreignProbe(ctx context.Context, rt http.RoundTripper, url string, extra http.Header, now func() instant, observe func(tls.ConnectionState)) (engine.LatencySample, error) {
	pt := &probeTimes{}
	start := now()
	lock := func(f func()) { pt.mu.Lock(); defer pt.mu.Unlock(); f() }
	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { lock(func() { pt.dnsStart = now() }) },
		DNSDone: func(httptrace.DNSDoneInfo) {
			lock(func() {
				if !pt.dnsStart.isZero() {
					pt.s.DNS = now().sub(pt.dnsStart)
				}
			})
		},
		ConnectStart: func(_, _ string) {
			lock(func() {
				if pt.connStart.isZero() {
					pt.connStart = now()
				}
			})
		},
		ConnectDone: func(_, _ string, err error) {
			lock(func() {
				if err == nil && !pt.connStart.isZero() && pt.s.Connect == 0 {
					pt.s.Connect = now().sub(pt.connStart)
				}
			})
		},
		TLSHandshakeStart: func() { lock(func() { pt.tlsStart = now() }) },
		TLSHandshakeDone: func(cs tls.ConnectionState, err error) {
			lock(func() {
				if err == nil && !pt.tlsStart.isZero() {
					pt.s.TLS = now().sub(pt.tlsStart)
					pt.s.TLSRTTs = tlsRoundTrips(cs.Version)
				}
			})
			if err == nil && observe != nil {
				observe(cs)
			}
		},
		GotConn:      func(info httptrace.GotConnInfo) { lock(func() { pt.reused = info.Reused }) },
		WroteRequest: func(httptrace.WroteRequestInfo) { lock(func() { pt.wrote = now() }) },
		GotFirstResponseByte: func() {
			lock(func() {
				if !pt.wrote.isZero() {
					pt.s.TTFB = now().sub(pt.wrote)
				}
			})
		},
	}
	if err := doProbe(httptrace.WithClientTrace(ctx, trace), rt, url, extra); err != nil {
		return engine.LatencySample{}, err
	}
	end := now()
	pt.mu.Lock()
	defer pt.mu.Unlock()
	s := pt.s
	s.Total = end.sub(start)
	wr := pt.wrote
	if wr.isZero() {
		wr = start
	}
	s.HTTP = end.sub(wr)
	// A reused connection (only possible with a custom RoundTripper) still
	// yields a request-time sample, just without connection stages.
	s.Staged = !pt.reused
	if pt.reused {
		s.DNS, s.Connect, s.TLS, s.TLSRTTs = 0, 0, 0, 0
	}
	return s, nil
}

// doProbe requires a complete, nonempty response within the compatibility
// ceiling. The extra byte detects overflow without draining an arbitrary body.
func doProbe(ctx context.Context, rt http.RoundTripper, url string, extra http.Header) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	setProbeHeaders(req, extra)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return err
	}
	if resp.ContentLength > maxProbeBodyBytes {
		return fmt.Errorf("%w: declared %d bytes exceeds %d-byte limit", errInvalidProbeSize, resp.ContentLength, maxProbeBodyBytes)
	}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxProbeBodyBytes+1))
	if n > maxProbeBodyBytes {
		return fmt.Errorf("%w: body exceeds %d-byte limit", errInvalidProbeSize, maxProbeBodyBytes)
	}
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: empty body (expected 1-%d bytes)", errInvalidProbeSize, maxProbeBodyBytes)
	}
	return nil
}

// selfProbe performs a GET of the small URL on an existing (load) transport.
// Only the request-to-full-response time is meaningful.
func selfProbe(ctx context.Context, rt http.RoundTripper, url string, extra http.Header, now func() instant) (engine.LatencySample, error) {
	pt := &probeTimes{}
	start := now()
	trace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			pt.mu.Lock()
			pt.wrote = now()
			pt.mu.Unlock()
		},
	}
	if err := doProbe(httptrace.WithClientTrace(ctx, trace), rt, url, extra); err != nil {
		return engine.LatencySample{}, err
	}
	end := now()
	pt.mu.Lock()
	wr := pt.wrote
	pt.mu.Unlock()
	if wr.isZero() {
		wr = start
	}
	return engine.LatencySample{Total: end.sub(start), HTTP: end.sub(wr)}, nil
}

// setProbeHeaders applies the headers every test request carries, then the
// caller-supplied extras (which win on conflict).
func setProbeHeaders(req *http.Request, extra http.Header) {
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")
	req.Header.Set("User-Agent", userAgent)
	for k, vs := range extra {
		req.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
}

const userAgent = "netquality-go/1 (+https://github.com/korya/netquality)"

// tlsRoundTrips is the number of round trips the handshake of a TLS version
// costs, used to normalise tls_f per draft Section 5.3.
func tlsRoundTrips(version uint16) int {
	switch version {
	case tls.VersionTLS13:
		return 1
	case 0:
		return 0
	default:
		return 2
	}
}
