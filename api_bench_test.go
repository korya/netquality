package netquality

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

func BenchmarkRunLibrary(b *testing.B) {
	for _, h2 := range []bool{false, true} {
		b.Run(map[bool]string{false: "http1", true: "http2"}[h2], func(b *testing.B) {
			srv := httptest.NewUnstartedServer(server.Handler(server.Options{
				LargeSize:      64 << 20,
				MaxClientBytes: -1,
			}))
			srv.EnableHTTP2 = h2
			srv.Config.ErrorLog = log.New(io.Discard, "", 0)
			srv.StartTLS()
			b.Cleanup(srv.Close)
			tr := http.DefaultTransport.(*http.Transport).Clone()
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // benchmark server
			client := &http.Client{Transport: tr}
			b.SetBytes(1 << 20)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
					HTTPClient:  client,
					Directions:  Download,
					IdleProbes:  -1,
					MaxFlows:    4,
					MaxBytes:    1 << 20,
					MaxDuration: 100 * time.Millisecond,
					Stability: StabilityParams{
						Interval:              10 * time.Millisecond,
						MovingAverageDistance: 2,
						MaxProbesPerSecond:    100,
					},
				})
				if err != nil {
					b.Fatal(err)
				}
				if res == nil || res.Download == nil || res.Download.Bytes == 0 {
					b.Fatal("run produced no download payload")
				}
			}
		})
	}
}
