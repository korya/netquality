package netquality

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

// A background-context run must survive either kind of idle stall on both
// supported HTTP versions, retaining only the probes that actually finished.
func TestIdleTimeout(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, body := range []bool{false, true} {
			for _, successful := range []int64{0, 1} {
				t.Run(fmt.Sprintf("h2=%v/body=%v/samples=%d", h2, body, successful), func(t *testing.T) {
					var loading atomic.Bool
					var attempts, warningEvents atomic.Int64
					stalled, released := make(chan struct{}), make(chan struct{})
					srv := startServer(t, server.Options{}, func(h http.Handler) http.Handler {
						return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if r.URL.Path == server.SmallPath && !loading.Load() && attempts.Add(1) > successful {
								close(stalled) // idle requests are sequential; only this one can stall
								if body {
									w.Header().Set("Content-Length", "1")
									w.WriteHeader(http.StatusOK)
									w.(http.Flusher).Flush()
								}
								<-r.Context().Done()
								close(released)
								return
							}
							h.ServeHTTP(w, r)
						})
					}, nil, h2)
					client, sockets := countingClient()
					if h2 {
						client, sockets = countingTLSClient()
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					watchdog := time.AfterFunc(5*time.Second, cancel)
					defer watchdog.Stop()
					res, err := RunWithEvents(ctx, Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
						HTTPClient: client, IdleProbes: 1000, IdleTimeout: 500 * time.Millisecond,
						Directions: Download, MaxDuration: 100 * time.Millisecond, MaxBytes: 1 << 20,
					}, func(e Event) {
						if e.Kind == EventPhase && e.Phase == "download" {
							if n := sockets.open.Load(); n != 0 {
								t.Errorf("idle left %d owned sockets open before load", n)
							}
							loading.Store(true)
						}
						if e.Kind == EventWarning && strings.Contains(e.Message, "idle timeout") {
							warningEvents.Add(1)
						}
					})
					if err != nil || res == nil {
						t.Fatalf("idle timeout must be nonfatal: result=%+v err=%v", res, err)
					}
					select {
					case <-stalled:
					default:
						t.Fatal("test never reached the stalled idle request")
					}
					select {
					case <-released:
					case <-time.After(time.Second):
						t.Fatal("server did not observe idle cancellation")
					}
					if res.Cancelled || res.Download == nil || res.Upload != nil || !loading.Load() {
						t.Fatalf("selected load must run after idle expiry: %+v", res)
					}
					if successful == 0 && res.Idle != nil || successful > 0 && (res.Idle == nil || res.Idle.Samples != int(successful)) {
						t.Errorf("completed samples=%d, idle=%+v", successful, res.Idle)
					}
					want := fmt.Sprintf("%d/1000 probes completed", successful)
					if !hasWarning(res, want) || warningEvents.Load() != 1 || attempts.Load() != successful+1 {
						t.Errorf("warnings=%v events=%d attempts=%d", res.Warnings, warningEvents.Load(), attempts.Load())
					}
				})
			}
		}
	}
}

// Record real timeout contexts rather than substituting the transport. This
// verifies phase budgets and their cancellation without tightly timing I/O.
type budgetClock struct {
	realClock
	mu        sync.Mutex
	durations []time.Duration
	contexts  []context.Context
}

func (c *budgetClock) WithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	child, cancel := c.realClock.WithTimeout(ctx, d)
	c.mu.Lock()
	c.durations = append(c.durations, d)
	c.contexts = append(c.contexts, child)
	c.mu.Unlock()
	return child, cancel
}

func TestPhaseTimeoutBudgets(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  Directions
		idle int
	}{
		{"both", Both, 1000}, {"download", Download, 1000},
		{"upload", Upload, 1000}, {"skip-idle", Both, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var loading atomic.Bool
			srv := startServer(t, server.Options{}, func(h http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case server.ConfigPath:
						select {
						case <-time.After(20 * time.Millisecond):
						case <-r.Context().Done():
							return
						}
					case server.LargePath, server.UploadPath:
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						return
					case server.SmallPath:
						if !loading.Load() {
							<-r.Context().Done()
							return
						}
					}
					h.ServeHTTP(w, r)
				})
			}, nil, true)
			clock := &budgetClock{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			watchdog := time.AfterFunc(5*time.Second, cancel)
			defer watchdog.Stop()
			res, err := RunWithEvents(ctx, Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
				HTTPClient: insecureClient(), ConfigTimeout: time.Second, IdleTimeout: 200 * time.Millisecond,
				IdleProbes: tc.idle, Directions: tc.dir, MaxDuration: 100 * time.Millisecond, clock: clock,
			}, func(e Event) {
				if e.Kind == EventPhase && (e.Phase == "download" || e.Phase == "upload") {
					loading.Store(true)
				}
			})
			if err != nil || res == nil || res.Cancelled {
				t.Fatalf("phase budget did not bound run: res=%+v err=%v", res, err)
			}
			want := []time.Duration{time.Second}
			if tc.idle > 0 {
				want = append(want, 200*time.Millisecond)
			} else if res.Idle != nil || hasWarning(res, "idle timeout") {
				t.Errorf("skipped idle was measured or timed out: %+v", res)
			}
			for _, dir := range []struct {
				selected bool
				result   *DirectionResult
			}{{tc.dir != Upload, res.Download}, {tc.dir != Download, res.Upload}} {
				if !dir.selected {
					if dir.result != nil {
						t.Error("unselected direction ran")
					}
					continue
				}
				want = append(want, 100*time.Millisecond)
				if dir.result == nil || dir.result.Reason != ReasonDurationCap {
					t.Errorf("own direction cap: %+v", dir.result)
				}
			}
			clock.mu.Lock()
			defer clock.mu.Unlock()
			if !reflect.DeepEqual(clock.durations, want) {
				t.Errorf("timeout creation: got %v, want %v", clock.durations, want)
			}
			for i, child := range clock.contexts {
				if child.Err() == nil {
					t.Errorf("phase %d context/timer survived Run", i)
				}
			}
		})
	}
}

func TestCallerDeadlineDuringLoad(t *testing.T) {
	srv := startServer(t, server.Options{}, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == server.LargePath {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return
			}
			h.ServeHTTP(w, r)
		})
	}, nil, true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := Run(ctx, Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
		HTTPClient: insecureClient(), IdleProbes: -1, MaxDuration: 10 * time.Second,
	})
	if err != context.DeadlineExceeded || res == nil || !res.Cancelled || res.Download == nil {
		t.Fatalf("caller deadline: res=%+v err=%v", res, err)
	}
	if res.Download.Reason != ReasonCancelled || res.Upload != nil {
		t.Errorf("parent deadline must cancel the current phase and omit later phases: %+v", res)
	}
}
