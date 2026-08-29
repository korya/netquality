package netquality

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

// TestSoak loops Run and checks that goroutines and heap plateau. The server
// runs in-process, so a leaked client connection shows up as server
// goroutines too.
func TestSoak(t *testing.T) {
	if testing.Short() {
		t.Skip("soak")
	}
	const warm, iters = 5, 40
	scenarios := []struct {
		name   string
		client func() *http.Client
		cancel time.Duration // cancel ctx this long into the run; 0 = let it finish
	}{
		{"default", insecureClient, 0},
		{"default-cancel", insecureClient, 250 * time.Millisecond},
		{"dialTLSContext-noIdleTimeout", func() *http.Client {
			tr := &http.Transport{
				DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2", "http/1.1"}}} //nolint:gosec
					return d.DialContext(ctx, network, addr)
				},
				ForceAttemptHTTP2: true,
				IdleConnTimeout:   0,
			}
			return &http.Client{Transport: tr}
		}, 0},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			srv := startServer(t, server.Options{}, nil, nil, true)
			client := sc.client()
			sample := func() (int, uint64) {
				runtime.GC()
				time.Sleep(50 * time.Millisecond) // let closed conns' goroutines exit
				runtime.GC()
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				return runtime.NumGoroutine(), ms.HeapInuse
			}
			one := func() {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if sc.cancel > 0 {
					time.AfterFunc(sc.cancel, cancel)
				}
				_, err := Run(ctx, Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
					HTTPClient: client, IdleProbes: 2,
					MaxDuration: 400 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability(),
				})
				if err != nil && ctx.Err() == nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < warm; i++ {
				one()
			}
			g0, h0 := sample()
			for i := 0; i < iters; i++ {
				one()
				if i%10 == 9 {
					g, h := sample()
					fmt.Printf("%-30s iter %2d  goroutines %3d (%+d)  heapInuse %6.2fMiB (%+.2f)\n", sc.name, i+1, g, g-g0, float64(h)/1048576, float64(int64(h)-int64(h0))/1048576)
				}
			}
			// Poll for the plateau rather than demanding it at one instant. The
			// counts include the in-process server's per-connection goroutines,
			// and the client aborts its flows, so the server side unwinds on the
			// kernel's schedule, not ours (INV-4 covers the client's sockets
			// only). The leak this guards against is ~3 goroutines and ~150KiB
			// per run, so a settled bound of +2 and +8MiB still catches it many
			// times over.
			var g1 int
			var h1 uint64
			ok := eventually(15*time.Second, func() bool {
				g1, h1 = sample()
				return g1-g0 <= 2 && h1 <= h0+8<<20
			})
			if !ok {
				buf := make([]byte, 1<<16)
				n := runtime.Stack(buf, true)
				t.Errorf("no plateau over %d runs: goroutines %d -> %d, heapInuse %d -> %d\n%s",
					iters, g0, g1, h0, h1, buf[:n])
			}
		})
	}
}
