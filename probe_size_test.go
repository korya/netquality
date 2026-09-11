package netquality

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/korya/netquality/internal/engine"
	"github.com/korya/netquality/server"
)

// Instrument the real transport's response body, including unsuccessful reads.
type probeReadTracker struct {
	http.RoundTripper
	read   atomic.Int64
	closed atomic.Bool
}

type trackedProbeBody struct {
	io.ReadCloser
	tracker *probeReadTracker
}

func (rt *probeReadTracker) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.RoundTripper.RoundTrip(req)
	if err == nil {
		resp.Body = &trackedProbeBody{ReadCloser: resp.Body, tracker: rt}
	}
	return resp, err
}

func (b *trackedProbeBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.tracker.read.Add(int64(n))
	return n, err
}

func (b *trackedProbeBody) Close() error {
	err := b.ReadCloser.Close()
	b.tracker.closed.Store(true)
	return err
}

func TestProbeResponseSize(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			declared string
			body     string
			stall    bool
			status   int
			wantRead int64
			wantErr  string
		}{
			{"one", "1", "x", false, 200, 1, ""},
			{"two", "2", "xx", false, 200, 2, ""},
			{"nine", "9", "123456789", false, 200, 9, ""},
			{"ten", "10", "0123456789", false, 200, 10, ""},
			{"unknown-one", "", "x", false, 200, 1, ""},
			{"unknown-ten", "", "0123456789", false, 200, 10, ""},
			{"empty", "0", "", false, 200, 0, "size"},
			{"unknown-empty", "", "", false, 200, 0, "size"},
			{"declared-eleven", "11", "", true, 200, 0, "size"},
			{"declared-megabyte", "1048576", "", true, 200, 0, "size"},
			{"unknown-eleven", "", "01234567890", true, 200, 11, "size"},
			{"unknown-megabyte", "", strings.Repeat("x", 1<<20), false, 200, 11, "size"},
			{"ten-stall", "", "0123456789", true, 200, 10, "deadline"},
			{"body-stall", "1", "", true, 200, 0, "deadline"},
			{"truncated", "5", "x", false, 200, 1, "read"},
			{"status-precedence", "1048576", "", true, 403, 0, "status"},
		} {
			t.Run(fmt.Sprintf("h2=%v/%s", h2, tc.name), func(t *testing.T) {
				exited := make(chan struct{})
				srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(exited)
					if tc.declared != "" {
						w.Header().Set("Content-Length", tc.declared)
					}
					w.WriteHeader(tc.status)
					w.(http.Flusher).Flush() // prevent automatic Content-Length for unknown cases
					_, _ = io.WriteString(w, tc.body)
					if tc.stall {
						w.(http.Flusher).Flush()
						<-r.Context().Done()
					}
				}))
				srv.EnableHTTP2 = h2
				srv.StartTLS()
				defer srv.Close()
				client := srv.Client()
				defer client.CloseIdleConnections()
				rt := &probeReadTracker{RoundTripper: client.Transport}
				ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				defer cancel()
				sample, err := foreignProbe(ctx, rt, srv.URL, nil, monoNow, nil)
				if (err == nil) != (tc.wantErr == "") {
					t.Fatalf("sample=%+v err=%v; want %q", sample, err, tc.wantErr)
				}
				if err != nil && sample != (engine.LatencySample{}) {
					t.Errorf("failed probe produced a sample: %+v", sample)
				}
				switch tc.wantErr {
				case "size":
					if !errors.Is(err, errInvalidProbeSize) {
						t.Errorf("want size error: %v", err)
					}
				case "deadline":
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Errorf("incomplete body must wait for deadline: %v", err)
					}
				case "status":
					if errors.Is(err, errInvalidProbeSize) || !strings.Contains(err.Error(), "403") {
						t.Errorf("status must win: %v", err)
					}
				case "read":
					if errors.Is(err, errInvalidProbeSize) || !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Errorf("truncated body must retain read error: %v", err)
					}
				}
				if rt.read.Load() != tc.wantRead || !rt.closed.Load() {
					t.Errorf("read=%d want=%d closed=%v", rt.read.Load(), tc.wantRead, rt.closed.Load())
				}
				select {
				case <-exited:
				case <-time.After(time.Second):
					t.Fatal("response close did not release handler")
				}
			})
		}
	}
}

func TestProbeRejectionPreservesLoad(t *testing.T) {
	for _, mode := range []string{"declared", "streamed", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			more := make(chan struct{})
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/load" {
					_, _ = io.WriteString(w, "LOAD")
					w.(http.Flusher).Flush()
					select {
					case <-more:
					case <-r.Context().Done():
						return
					}
					_, _ = io.WriteString(w, "MORE")
					w.(http.Flusher).Flush()
				} else {
					if mode == "declared" {
						w.Header().Set("Content-Length", "1048576")
					}
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					if mode == "streamed" {
						_, _ = io.WriteString(w, "01234567890")
						w.(http.Flusher).Flush()
					}
				}
				<-r.Context().Done()
			}))
			srv.EnableHTTP2 = true
			srv.StartTLS()
			defer srv.Close()
			client := srv.Client()
			defer client.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var mu sync.Mutex
			var connections []httptrace.GotConnInfo
			ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
				mu.Lock()
				defer mu.Unlock()
				connections = append(connections, i)
			}})
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/load", nil)
			if err != nil {
				t.Fatal(err)
			}
			load, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer load.Body.Close()
			buf := make([]byte, 4)
			if _, err := io.ReadFull(load.Body, buf); err != nil {
				t.Fatal(err)
			}
			probeCtx, stopProbe := context.WithTimeout(ctx, 500*time.Millisecond)
			defer stopProbe()
			sample, err := selfProbe(probeCtx, client.Transport, srv.URL+"/small", nil, monoNow)
			want := errInvalidProbeSize
			if mode == "deadline" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) || sample != (engine.LatencySample{}) {
				t.Fatalf("sample=%+v error=%v; want %v", sample, err, want)
			}
			mu.Lock()
			shared := len(connections) == 2 && connections[0].Conn == connections[1].Conn && connections[1].Reused
			mu.Unlock()
			if !shared || load.ProtoMajor != 2 {
				t.Fatal("probe did not share the active HTTP/2 load connection")
			}
			close(more)
			if _, err := io.ReadFull(load.Body, buf); err != nil || string(buf) != "MORE" {
				t.Fatalf("load broken after rejected probe: %q %v", buf, err)
			}
		})
	}
}

func TestInvalidProbeResponses(t *testing.T) {
	for _, body := range []string{"", "01234567890"} {
		t.Run(fmt.Sprintf("bytes=%d", len(body)), func(t *testing.T) {
			var idleCalls atomic.Int64
			var loading atomic.Bool
			srv := startServer(t, server.Options{}, func(h http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == server.SmallPath {
						if !loading.Load() && idleCalls.Add(1) != 2 {
							_, _ = io.WriteString(w, "x")
						} else {
							_, _ = io.WriteString(w, body)
						}
						return
					}
					if r.URL.Path == server.LargePath {
						// No load payload: every accounted byte must be a probe estimate.
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						return
					}
					h.ServeHTTP(w, r)
				})
			}, nil, true)
			for range 2 { // warning guards must reset between runs as well as directions
				idleCalls.Store(0)
				loading.Store(false)
				var mu sync.Mutex
				var events []Event
				res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
					HTTPClient: insecureClient(), IdleProbes: 3, MaxFlows: 1,
					MaxDuration: 500 * time.Millisecond, MaxBytes: 1 << 40,
					Events: func(e Event) {
						if e.Kind == EventPhase && e.Phase == "download" {
							loading.Store(true)
						}
						mu.Lock()
						defer mu.Unlock()
						events = append(events, e)
					},
				})
				if err != nil || res.Idle == nil || res.Idle.Samples != 2 || res.Download == nil || res.Upload == nil {
					t.Fatalf("partial idle and both load results must survive: %+v err=%v", res, err)
				}
				for _, d := range []*DirectionResult{res.Download, res.Upload} {
					if d.Loaded.Foreign != nil || d.Loaded.Self != nil || d.RPM != 0 || d.FlowErrors != 0 || d.Reason != ReasonDurationCap {
						t.Errorf("invalid probes polluted load result: %+v", d)
					}
				}
				if d := res.Download; d.Bytes < foreignProbeBytes+selfProbeBytes || d.Bytes%1000 != 0 || d.ThroughputBPS != 0 {
					t.Errorf("rejected probes must cost estimates, never goodput: %+v", d)
				}
				wantWarnings := []string{"idle latency:", "download foreign probe:", "download self probe:", "upload foreign probe:", "upload self probe:"}
				for _, prefix := range wantWarnings {
					var results, emitted int
					for _, w := range res.Warnings {
						if strings.HasPrefix(w, prefix) && strings.Contains(w, errInvalidProbeSize.Error()) {
							results++
						}
					}
					mu.Lock()
					for _, e := range events {
						if e.Kind == EventWarning && strings.HasPrefix(e.Message, prefix) && strings.Contains(e.Message, errInvalidProbeSize.Error()) {
							emitted++
						}
					}
					mu.Unlock()
					if results != 1 || emitted != 1 {
						t.Errorf("%s warnings: result=%d events=%d; %v", prefix, results, emitted, res.Warnings)
					}
				}
				mu.Lock()
				var samples int
				for _, e := range events {
					if e.Kind == EventProbe {
						samples++
						if e.ProbeKind != "idle" {
							t.Errorf("invalid loaded probe emitted success: %+v", e)
						}
					}
				}
				mu.Unlock()
				if samples != 2 {
					t.Errorf("successful probe events=%d; want 2", samples)
				}
			}
		})
	}
}

func TestInvalidIdleDiagnostics(t *testing.T) {
	for _, mode := range []string{"all-invalid", "last-status", "timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var attempts atomic.Int64
			var loading atomic.Bool
			srv := startServer(t, server.Options{}, func(h http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != server.SmallPath || loading.Load() {
						h.ServeHTTP(w, r)
						return
					}
					n := attempts.Add(1)
					switch {
					case mode == "all-invalid" || n == 2:
						w.Header().Set("Content-Length", "11")
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
						<-r.Context().Done()
					case n == 1:
						h.ServeHTTP(w, r)
					case n == 3:
						w.WriteHeader(http.StatusForbidden)
					default:
						if mode == "cancel" {
							cancel()
						}
						<-r.Context().Done()
					}
				})
			}, nil, true)
			probes := 4
			if mode == "last-status" {
				probes = 3
			}
			res, err := Run(ctx, Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
				HTTPClient: insecureClient(), IdleProbes: probes, IdleTimeout: 500 * time.Millisecond,
				Directions: Download, MaxDuration: 100 * time.Millisecond, MaxBytes: 1 << 20,
				Events: func(e Event) {
					if e.Kind == EventPhase && e.Phase == "download" {
						loading.Store(true)
					}
				},
			})
			if res == nil {
				t.Fatalf("missing partial result: %v", err)
			}
			if mode == "cancel" {
				if !errors.Is(err, context.Canceled) || !res.Cancelled || res.Download != nil || hasWarning(res, "idle latency:") {
					t.Fatalf("parent cancellation must take precedence: %+v err=%v", res, err)
				}
			} else {
				if err != nil || res.Download == nil || res.Cancelled {
					t.Fatalf("idle rejection must allow load: %+v err=%v", res, err)
				}
				var warnings []string
				for _, w := range res.Warnings {
					if strings.HasPrefix(w, "idle latency:") {
						warnings = append(warnings, w)
					}
				}
				if len(warnings) != 1 || !strings.Contains(warnings[0], errInvalidProbeSize.Error()) {
					t.Fatalf("expected one size warning: %v", res.Warnings)
				}
				if mode != "all-invalid" && !strings.Contains(warnings[0], "403") {
					t.Errorf("last failure lost: %v", warnings)
				}
				if strings.Contains(warnings[0], "idle timeout") != (mode == "timeout") {
					t.Errorf("timeout diagnosis: %v", warnings)
				}
			}
			if mode == "all-invalid" {
				if res.Idle != nil {
					t.Errorf("invalid responses produced idle statistics: %+v", res.Idle)
				}
			} else if res.Idle == nil || res.Idle.Samples != 1 {
				t.Errorf("valid sample lost: %+v", res.Idle)
			}
		})
	}
}

func TestRejectedProbesHitByteCap(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(fmt.Sprintf("known=%v", known), func(t *testing.T) {
			srv := startServer(t, server.Options{}, func(h http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case server.SmallPath:
						if known {
							w.Header().Set("Content-Length", "1048576")
						}
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
						if !known {
							_, _ = io.WriteString(w, "01234567890")
							w.(http.Flusher).Flush()
						}
						<-r.Context().Done()
					case server.LargePath:
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
						<-r.Context().Done()
					default:
						h.ServeHTTP(w, r)
					}
				})
			}, nil, true)
			res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
				HTTPClient: insecureClient(), IdleProbes: -1, Directions: Download,
				MaxBytes: 10_000, MaxDuration: 2 * time.Second, MaxFlows: 1,
			})
			if err != nil || res == nil || res.Download == nil {
				t.Fatalf("result=%+v err=%v", res, err)
			}
			d := res.Download
			if d.Reason != ReasonBytesCap || d.Bytes < 10_000 || d.Bytes > 15_000 || d.Bytes%1000 != 0 {
				t.Errorf("failed estimates must trip cap: %+v", d)
			}
			if d.ThroughputBPS != 0 || d.RPM != 0 || d.Loaded.Foreign != nil || d.Loaded.Self != nil {
				t.Errorf("rejected probes became measurements: %+v", d)
			}
		})
	}
}
