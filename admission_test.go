package netquality

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

// Observe actual server admission inside the first read/write, then hold the
// first requests until every initial flow has arrived. This avoids mistaking
// client-side flow creation for a successful server admission.
type admissionBarrier struct {
	count  atomic.Int32
	ready  chan struct{}
	target int32
}

func (b *admissionBarrier) arrive(ctx context.Context) error {
	if b.count.Add(1) == b.target {
		close(b.ready)
	}
	select {
	case <-b.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type admissionReadBody struct {
	io.ReadCloser
	b    *admissionBarrier
	ctx  context.Context
	once sync.Once
	err  error
}

func (r *admissionReadBody) Read(p []byte) (int, error) {
	r.once.Do(func() { r.err = r.b.arrive(r.ctx) })
	if r.err != nil {
		return 0, r.err
	}
	return r.ReadCloser.Read(p)
}

type admissionWriteBody struct {
	http.ResponseWriter
	b      *admissionBarrier
	ctx    context.Context
	once   sync.Once
	err    error
	status int
}

func (w *admissionWriteBody) WriteHeader(n int) { w.status = n; w.ResponseWriter.WriteHeader(n) }
func (w *admissionWriteBody) Write(p []byte) (int, error) {
	if w.status == http.StatusOK {
		w.once.Do(func() { w.err = w.b.arrive(w.ctx) })
		if w.err != nil {
			return 0, w.err
		}
	}
	return w.ResponseWriter.Write(p)
}

func TestClientAdmissionMultiFlow(t *testing.T) {
	down := &admissionBarrier{ready: make(chan struct{}), target: 16}
	up := &admissionBarrier{ready: make(chan struct{}), target: 16}
	srv := startServer(t, server.Options{MaxClientBytes: 8 << 30, LargeSize: 1 << 20, UploadSize: 1 << 20}, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case server.LargePath:
				w = &admissionWriteBody{ResponseWriter: w, b: down, ctx: r.Context()}
			case server.UploadPath:
				r.Body = &admissionReadBody{ReadCloser: r.Body, b: up, ctx: r.Context()}
			}
			h.ServeHTTP(w, r)
		})
	}, nil, true)
	for repeat := 0; repeat < 2; repeat++ {
		res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, Options{HTTPClient: insecureClient(), IdleProbes: 2, MaxFlows: 16,
			MaxDuration: 10 * time.Second, MaxBytes: 128 << 20, Stability: StabilityParams{InitialFlows: 16},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range []*DirectionResult{res.Download, res.Upload} {
			if d == nil || d.FlowErrors != 0 || d.Reason == ReasonFlowError || d.Flows != 16 {
				t.Fatalf("repeat %d: %+v", repeat, d)
			}
		}
		if down.count.Load() < 16 || up.count.Load() < 16 {
			t.Fatalf("actual admissions download=%d upload=%d", down.count.Load(), up.count.Load())
		}
	}
	// Small finite objects must have been re-requested with released slots.
	if down.count.Load() <= 32 || up.count.Load() <= 32 {
		t.Fatalf("finite objects were not re-requested: %d/%d", down.count.Load(), up.count.Load())
	}
}

func TestClientAdmissionRefusalIsGraceful(t *testing.T) {
	barrier := &admissionBarrier{ready: make(chan struct{}), target: 2}
	srv := startServer(t, server.Options{MaxClientBytes: 1 << 30, MaxClientConcurrency: 1}, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == server.LargePath {
				w = &admissionWriteBody{ResponseWriter: w, b: barrier, ctx: r.Context()}
			}
			h.ServeHTTP(w, r)
		})
	}, nil, true)
	res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, Options{HTTPClient: insecureClient(), IdleProbes: -1, Directions: Download,
		MaxDuration: 10 * time.Second, MaxFlows: 2, Stability: StabilityParams{InitialFlows: 2},
	})
	if res == nil || res.Download == nil || res.Download.Reason != ReasonFlowError || !res.Download.Truncated || res.Download.FlowErrors == 0 || !hasWarning(res, "429") {
		t.Fatalf("refusal lost its partial result: result=%+v err=%v", res, err)
	}
	if barrier.count.Load() != 1 {
		t.Fatalf("admitted %d requests at cap 1", barrier.count.Load())
	}
}
