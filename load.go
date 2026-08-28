package netquality

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
)

// flow is one load-generating connection with its own transport.
type flow struct {
	id    int
	rt    http.RoundTripper
	proto atomic.Pointer[string] // negotiated protocol once known
	ready atomic.Bool            // response headers received
}

// byteCounter accumulates bytes moved by a phase and trips a callback when the
// cap is reached. It is safe for concurrent use.
//
// It keeps two totals because they answer different questions. total is the
// phase's cost — load payload plus the draft's fixed per-probe estimate — and
// is what MaxBytes bounds when a caller sets one (LIM-2). payload is the load
// flows' bytes alone and is the only thing goodput may be computed from
// (LOAD-4) — feeding the probes' own estimated cost into the throughput
// estimate makes a stalled link look fast and, through ProbeGap, throttles the
// very probes that would show it is stalled.
type byteCounter struct {
	total   atomic.Int64
	payload atomic.Int64
	limit   int64
	once    sync.Once
	onLimit func()
}

// add records n bytes moved by a load flow: goodput and cost alike.
func (c *byteCounter) add(n int64) {
	c.payload.Add(n)
	c.addProbe(n)
}

// addProbe records n bytes of probe traffic: cost only, never goodput. It
// trips onLimit once when a positive limit is reached; a limit of 0 means
// unlimited (LIM-2).
func (c *byteCounter) addProbe(n int64) {
	if t := c.total.Add(n); c.limit > 0 && t >= c.limit {
		c.once.Do(c.onLimit)
	}
}

// get is the phase's total cost; payloadBytes is what the load flows moved.
func (c *byteCounter) get() int64          { return c.total.Load() }
func (c *byteCounter) payloadBytes() int64 { return c.payload.Load() }

type countingWriter struct{ c *byteCounter }

func (w countingWriter) Write(p []byte) (int, error) {
	w.c.add(int64(len(p)))
	return len(p), nil
}

// uploadBody is an effectively unbounded reader of pseudo-random bytes that
// stops at context cancellation and counts what it hands out.
type uploadBody struct {
	ctx context.Context
	c   *byteCounter
	buf []byte
	off int
}

func newUploadBody(ctx context.Context, c *byteCounter) *uploadBody {
	buf := make([]byte, 256<<10)
	r := rand.New(rand.NewSource(0x6e71)) //nolint:gosec // not security-sensitive; just incompressible filler
	r.Read(buf)
	return &uploadBody{ctx: ctx, c: c, buf: buf}
}

func (u *uploadBody) Read(p []byte) (int, error) {
	if err := u.ctx.Err(); err != nil {
		return 0, io.EOF
	}
	n := copy(p, u.buf[u.off:])
	u.off = (u.off + n) % len(u.buf)
	u.c.add(int64(n))
	return n, nil
}

// errFlowDone is returned by runFlow when the phase context ended the flow.
var errFlowDone = errors.New("flow done")

// runFlow drives one load-generating request until ctx is done. A response
// body that ends cleanly (finite server object) is re-requested on the same
// transport. Any other failure is returned.
func runFlow(ctx context.Context, f *flow, dir Directions, url string, c *byteCounter, extra http.Header, observe func(tls.ConnectionState)) error {
	for {
		if ctx.Err() != nil {
			return errFlowDone
		}
		err := oneRequest(ctx, f, dir, url, c, extra, observe)
		if ctx.Err() != nil {
			return errFlowDone
		}
		if err != nil {
			return err
		}
	}
}

func oneRequest(ctx context.Context, f *flow, dir Directions, url string, c *byteCounter, extra http.Header, observe func(tls.ConnectionState)) error {
	var req *http.Request
	var err error
	// Uploads only see their response after the body ends, so learn the
	// protocol from ALPN and readiness from the connection instead.
	trace := &httptrace.ClientTrace{
		TLSHandshakeDone: func(cs tls.ConnectionState, err error) {
			if err == nil {
				proto := "HTTP/1.1"
				if cs.NegotiatedProtocol == "h2" {
					proto = "HTTP/2.0"
				}
				f.proto.Store(&proto)
				if observe != nil {
					observe(cs)
				}
			}
		},
		GotConn: func(httptrace.GotConnInfo) { f.ready.Store(true) },
	}
	ctx = httptrace.WithClientTrace(ctx, trace)
	if dir == Upload {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, newUploadBody(ctx, c))
		if err != nil {
			return err
		}
		req.ContentLength = -1
		req.Header.Set("Content-Type", "application/octet-stream")
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
	}
	setProbeHeaders(req, extra)
	resp, err := f.rt.RoundTrip(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	proto := resp.Proto
	f.proto.Store(&proto)
	if err := checkStatus(resp); err != nil {
		return err
	}
	_, err = io.Copy(countingWriter{c}, resp.Body)
	return err
}
