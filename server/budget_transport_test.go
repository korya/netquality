package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type budgetSignalBody struct {
	io.ReadCloser
	gate *transferGate
}

func (b budgetSignalBody) Read(p []byte) (int, error) {
	b.gate.once.Do(func() { b.gate.entered <- struct{}{} })
	return b.ReadCloser.Read(p)
}

func TestBudgetCancellationOverHTTP(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, upload := range []bool{false, true} {
			t.Run(fmt.Sprintf("h2=%v/upload=%v", h2, upload), func(t *testing.T) {
				gates := map[string]*transferGate{"a": newTransferGate(), "b": newTransferGate(), "c": newTransferGate()}
				exits := make(chan string, 8)
				h := Handler(Options{MaxClientBytes: 1 << 20, MaxClientConcurrency: 2, LargeSize: 64, UploadSize: 64})
				srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					id := r.URL.Query().Get("id")
					if g := gates[id]; g != nil {
						defer func() { exits <- id }()
						if upload {
							r.Body = budgetSignalBody{r.Body, g}
						} else {
							w = &gatedBudgetWriter{ResponseWriter: w, gate: g, ctx: r.Context()}
						}
					}
					h.ServeHTTP(w, r)
				}))
				srv.EnableHTTP2 = h2
				srv.Config.ErrorLog = log.New(io.Discard, "", 0)
				var mu sync.Mutex
				connections := map[net.Conn]bool{}
				srv.Config.ConnState = func(c net.Conn, state http.ConnState) {
					if state == http.StateNew {
						mu.Lock()
						connections[c] = true
						mu.Unlock()
					}
				}
				srv.StartTLS()
				defer srv.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				client := srv.Client()
				get := func(path string, want int) {
					t.Helper()
					r, err := http.NewRequestWithContext(ctx, "GET", srv.URL+path, nil)
					if err != nil {
						t.Fatal(err)
					}
					resp, err := client.Do(r)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
					if resp.StatusCode != want {
						t.Fatalf("status %d, want %d", resp.StatusCode, want)
					}
				}
				get(SmallPath, 200) // Establish H2 before launching concurrent streams.
				type pending struct {
					cancel context.CancelFunc
					finish func()
					done   chan error
				}
				start := func(id string) pending {
					cctx, stop := context.WithCancel(ctx)
					method, path := "GET", LargePath
					var body io.Reader
					finish := gates[id].unblock
					if upload {
						method, path = "POST", UploadPath
						pr, pw := io.Pipe()
						body = pr
						finish = func() { _ = pw.Close() }
						written := make(chan struct{})
						go func() { defer close(written); _, _ = pw.Write([]byte("x")) }()
						t.Cleanup(func() { _ = pr.Close(); _ = pw.Close(); awaitBudget(t, written) })
					}
					r, err := http.NewRequestWithContext(cctx, method, srv.URL+path+"?id="+id, body)
					if err != nil {
						t.Fatal(err)
					}
					done := make(chan error, 1)
					go func() {
						resp, err := client.Do(r)
						if err == nil {
							_, err = io.Copy(io.Discard, resp.Body)
							_ = resp.Body.Close()
							if resp.StatusCode != 200 {
								err = fmt.Errorf("status %d", resp.StatusCode)
							}
						}
						done <- err
					}()
					// The caller owns an open upload producer; close it on cancellation
					// so HTTP/1.1's request writer can leave its blocked Read.
					return pending{func() {
						stop()
						if upload {
							finish()
						}
					}, finish, done}
				}
				a := start("a")
				defer a.cancel()
				b := start("b")
				defer b.cancel()
				awaitBudget(t, gates["a"].entered)
				awaitBudget(t, gates["b"].entered)
				get(LargePath, 429)
				get(SmallPath, 200)
				get(ConfigPath, 200)
				a.cancel()
				if err := awaitBudget(t, a.done); err == nil {
					t.Fatal("canceled request succeeded")
				}
				if id := awaitBudget(t, exits); id != "a" {
					t.Fatalf("another admitted transfer exited: %s", id)
				}
				c := start("c")
				defer c.cancel()
				awaitBudget(t, gates["c"].entered)
				select {
				case err := <-b.done:
					t.Fatalf("cancel disturbed other transfer: %v", err)
				default:
				}
				get(SmallPath, 200)
				if h2 {
					mu.Lock()
					n := len(connections)
					mu.Unlock()
					if n != 1 {
						t.Fatalf("requests used %d connections; H2 stream bound not exercised", n)
					}
				}
				b.finish()
				c.finish()
				if err := awaitBudget(t, b.done); err != nil {
					t.Fatal(err)
				}
				if err := awaitBudget(t, c.done); err != nil {
					t.Fatal(err)
				}
				awaitBudget(t, exits)
				awaitBudget(t, exits)
			})
		}
	}
}

func TestCanceledHandlerKeepsAdmissionUntilReturn(t *testing.T) {
	h := Handler(Options{MaxClientBytes: 1000, MaxClientConcurrency: 1, UploadSize: 64})
	gate := newTransferGate()
	defer gate.unblock()
	// Deliberately non-cooperating embedding: cancellation alone cannot free
	// admission while a body reader still owns outstanding work.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("POST", "https://nq.test"+UploadPath, nil).WithContext(ctx)
	r.Body = gatedBudgetBody{io.NopCloser(strings.NewReader("x")), gate, context.Background()}
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), r) }()
	awaitBudget(t, gate.entered)
	cancel()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "https://nq.test"+LargePath, nil))
	if w.Code != 429 || !strings.Contains(w.Body.String(), concurrencyExhausted) {
		t.Fatalf("cancellation released active work: %d", w.Code)
	}
	gate.unblock()
	awaitBudget(t, done)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "https://nq.test"+UploadPath, nil))
	if w.Code != 200 {
		t.Fatal("returned handler did not release admission")
	}
}
