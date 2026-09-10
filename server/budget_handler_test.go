package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A gate observes admission inside the first payload read/write. It never
// blocks refused responses, and its context lets real server cancellation exit.
type transferGate struct {
	entered     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
}

func newTransferGate() *transferGate {
	return &transferGate{entered: make(chan struct{}, 1), release: make(chan struct{})}
}
func (g *transferGate) unblock() { g.releaseOnce.Do(func() { close(g.release) }) }

func (g *transferGate) wait(ctx context.Context) error {
	g.once.Do(func() { g.entered <- struct{}{} })
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type gatedBudgetBody struct {
	io.ReadCloser
	gate *transferGate
	ctx  context.Context
}

func (b gatedBudgetBody) Read(p []byte) (int, error) {
	if err := b.gate.wait(b.ctx); err != nil {
		return 0, err
	}
	return b.ReadCloser.Read(p)
}

type gatedBudgetWriter struct {
	http.ResponseWriter
	gate   *transferGate
	ctx    context.Context
	status int
}

func (w *gatedBudgetWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *gatedBudgetWriter) Write(p []byte) (int, error) {
	if w.status == 0 || w.status == 200 {
		if err := w.gate.wait(w.ctx); err != nil {
			return 0, err
		}
	}
	return w.ResponseWriter.Write(p)
}
func awaitBudget[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for budget test barrier")
		var zero T
		return zero
	}
}

func TestHandlerConcurrentBudget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		limit, want int
		bytes       int64
	}{
		{"default", 0, 32, 1}, {"negative default", -1, 32, 1}, {"custom", 3, 3, 1}, {"disabled", 1, 40, -1},
	} {
		for _, kind := range []string{"download", "upload", "mixed"} {
			for _, signed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/signed=%v", tc.name, kind, signed), func(t *testing.T) {
					o := Options{MaxClientBytes: tc.bytes, ClientWindow: time.Hour, MaxClientConcurrency: tc.limit, LargeSize: 64, UploadSize: 64}
					if signed {
						o.SigningKeys = [][]byte{testKey}
					}
					h := Handler(o)
					type pending struct {
						gate *transferGate
						done chan *httptest.ResponseRecorder
					}
					requests := make([]pending, 40)
					for i := range requests {
						gate := newTransferGate()
						done := make(chan *httptest.ResponseRecorder, 1)
						requests[i] = pending{gate, done}
						t.Cleanup(func() { gate.unblock() })
						method, path := "GET", LargePath
						if kind == "upload" || kind == "mixed" && i%2 == 1 {
							method, path = "POST", UploadPath
						}
						u := "https://nq.test" + path
						if signed {
							var err error
							u, err = SignURL(testKey, u, time.Now().Add(time.Hour), "client")
							if err != nil {
								t.Fatal(err)
							}
						}
						r := httptest.NewRequest(method, u, strings.NewReader(strings.Repeat("x", 64)))
						r.Body = gatedBudgetBody{r.Body, gate, r.Context()}
						go func() {
							w := httptest.NewRecorder()
							h.ServeHTTP(&gatedBudgetWriter{ResponseWriter: w, gate: gate, ctx: r.Context()}, r)
							done <- w
						}()
					}
					var admitted []pending
					for _, p := range requests {
						select {
						case <-p.gate.entered:
							admitted = append(admitted, p)
						case w := <-p.done:
							if w.Code != 429 || w.Header().Get("Retry-After") != "1" || !strings.Contains(w.Body.String(), concurrencyExhausted) {
								t.Fatalf("refusal: %d %v %q", w.Code, w.Header(), w.Body.String())
							}
						case <-time.After(10 * time.Second):
							t.Fatal("admission barrier timed out")
						}
					}
					if len(admitted) != tc.want {
						t.Fatalf("admitted %d, want %d", len(admitted), tc.want)
					}
					// Allow the batch to complete; cleanup must not close the same channels twice.
					for _, p := range admitted {
						p.gate.unblock()
					}
					for _, p := range admitted {
						w := awaitBudget(t, p.done)
						if w.Code != 200 {
							t.Fatalf("admitted request interrupted: %d", w.Code)
						}
					}
				})
			}
		}
	}
}

func TestHandlerBudgetIdentityAndMethods(t *testing.T) {
	h := Handler(Options{AuthToken: "token", SigningKeys: [][]byte{testKey}, MaxClientBytes: 1 << 20, MaxClientConcurrency: 1, LargeSize: 64})
	signed := func(sub string) string {
		u, err := SignURL(testKey, "https://nq.test"+LargePath, time.Now().Add(time.Hour), sub)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	gate := newTransferGate()
	defer gate.unblock()
	r := httptest.NewRequest("GET", signed("a"), nil)
	r.RemoteAddr = "192.0.2.1:1"
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(&gatedBudgetWriter{ResponseWriter: httptest.NewRecorder(), gate: gate, ctx: r.Context()}, r)
	}()
	awaitBudget(t, gate.entered)
	for _, tc := range []struct {
		name, url, ip, token, method string
		want                         int
	}{
		{"same subject different IP", signed("a"), "192.0.2.2:2", "", "GET", 429},
		{"different subject same IP", signed("b"), "192.0.2.1:2", "", "GET", 200},
		{"bearer takes IP precedence", signed("a"), "192.0.2.1:2", "token", "GET", 200},
		{"bad bearer falls back to signature", signed("a"), "192.0.2.2:2", "bad", "GET", 429},
		{"method before admission", signed("a"), "192.0.2.1:2", "", "POST", 405},
		{"auth before method", "https://nq.test" + LargePath, "192.0.2.1:2", "bad", "POST", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.url, nil)
			r.RemoteAddr = tc.ip
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
		})
	}
	// A bearer request uses its IP's slot even when it carries a signed subject.
	ipGate := newTransferGate()
	defer ipGate.unblock()
	ir := httptest.NewRequest("GET", signed("b"), nil)
	ir.RemoteAddr = "192.0.2.1:3"
	ir.Header.Set("Authorization", "Bearer token")
	ipDone := make(chan struct{})
	go func() {
		defer close(ipDone)
		h.ServeHTTP(&gatedBudgetWriter{ResponseWriter: httptest.NewRecorder(), gate: ipGate, ctx: ir.Context()}, ir)
	}()
	awaitBudget(t, ipGate.entered)
	for _, tc := range []struct {
		path, ip string
		want     int
	}{{LargePath, "192.0.2.1:4", 429}, {LargePath, "192.0.2.2:4", 200}, {SmallPath, "192.0.2.1:4", 200}, {ConfigPath, "192.0.2.1:4", 200}} {
		r := httptest.NewRequest("GET", "https://nq.test"+tc.path, nil)
		r.RemoteAddr = tc.ip
		r.Header.Set("Authorization", "Bearer token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s: %d", tc.path, tc.ip, w.Code)
		}
	}
	gate.unblock()
	ipGate.unblock()
	awaitBudget(t, done)
	awaitBudget(t, ipDone)
}

type budgetFaultReader struct {
	err   error
	panic bool
}

func (r budgetFaultReader) Read(p []byte) (int, error) {
	if r.panic {
		panic("injected I/O panic")
	}
	return copy(p, "12345"), r.err
}

type budgetFaultWriter struct{ *httptest.ResponseRecorder }

func (w budgetFaultWriter) Write([]byte) (int, error) { return 5, io.ErrClosedPipe }

func TestHandlerBudgetExitSettlement(t *testing.T) {
	for _, kind := range []string{"read", "cancel", "write", "panic", "zero"} {
		t.Run(kind, func(t *testing.T) {
			charge := int64(5)
			if kind == "panic" {
				charge = 64
			}
			if kind == "zero" {
				charge = 0
			}
			h := Handler(Options{MaxClientBytes: charge + 1, MaxClientConcurrency: 1, LargeSize: 64, UploadSize: 64, ClientWindow: time.Hour})
			method, path := "POST", UploadPath
			var body io.Reader = budgetFaultReader{err: io.ErrUnexpectedEOF}
			if kind == "cancel" {
				body = budgetFaultReader{err: context.Canceled}
			}
			if kind == "panic" {
				body = budgetFaultReader{panic: true}
			}
			if kind == "zero" {
				body = bytes.NewReader(nil)
			}
			if kind == "write" {
				method, path = "GET", LargePath
			}
			func() {
				defer func() {
					v := recover()
					if kind == "panic" && v == nil {
						t.Error("missing injected panic")
					}
					if kind != "panic" && v != nil {
						t.Errorf("unexpected panic: %v", v)
					}
				}()
				r := httptest.NewRequest(method, "https://nq.test"+path, body)
				w := httptest.NewRecorder()
				if kind == "write" {
					h.ServeHTTP(budgetFaultWriter{w}, r)
				} else {
					h.ServeHTTP(w, r)
				}
			}()
			// Exactly one byte of credit remains: proves actual partial bytes were
			// settled, not the full cap; panic deliberately uses the cap instead.
			r := httptest.NewRequest("POST", "https://nq.test"+UploadPath, strings.NewReader("xx"))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("slot leaked or charge overstated: %d %s", w.Code, w.Body)
			}
			w = httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "https://nq.test"+UploadPath, nil))
			if w.Code != 429 || !strings.Contains(w.Body.String(), budgetExhausted) {
				t.Fatalf("partial charge lost: %d %s", w.Code, w.Body)
			}
		})
	}
	// A panic with default upload cap and the smallest budget must produce a
	// portable positive Retry-After, without float-to-duration overflow.
	h := Handler(Options{MaxClientBytes: 1, ClientWindow: time.Hour})
	func() {
		defer func() { _ = recover() }()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "https://nq.test"+UploadPath, budgetFaultReader{panic: true}))
	}()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "https://nq.test"+LargePath, nil))
	seconds, err := strconv.ParseInt(w.Header().Get("Retry-After"), 10, 64)
	if err != nil || seconds != int64(maxRetryWait/time.Second)+1 {
		t.Fatalf("Retry-After=%q: %v", w.Header().Get("Retry-After"), err)
	}
}
