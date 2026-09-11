package server

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
)

const benchmarkPayloadSize = 64 << 10

func benchmarkHTTPServer(b *testing.B, h2 bool, budget bool) (*httptest.Server, *http.Client) {
	b.Helper()
	maxBytes := int64(-1)
	if budget {
		// Keep the budget out of the way while exercising admission and
		// settlement; the request size remains small enough for long runs.
		maxBytes = 1 << 60
	}
	srv := httptest.NewUnstartedServer(Handler(Options{
		LargeSize:            benchmarkPayloadSize,
		UploadSize:           benchmarkPayloadSize,
		MaxClientBytes:       maxBytes,
		MaxClientConcurrency: 64,
	}))
	srv.EnableHTTP2 = h2
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	b.Cleanup(srv.Close)
	return srv, srv.Client()
}

func BenchmarkHandlerDownload(b *testing.B) {
	for _, h2 := range []bool{false, true} {
		b.Run(map[bool]string{false: "http1", true: "http2"}[h2], func(b *testing.B) {
			srv, client := benchmarkHTTPServer(b, h2, false)
			b.SetBytes(benchmarkPayloadSize)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				resp, err := client.Get(srv.URL + LargePath)
				if err != nil {
					b.Fatal(err)
				}
				if resp.StatusCode != http.StatusOK {
					b.Fatalf("status = %d", resp.StatusCode)
				}
				if _, err := io.Copy(io.Discard, resp.Body); err != nil {
					b.Fatal(err)
				}
				_ = resp.Body.Close()
			}
		})
	}
}

func BenchmarkHandlerUpload(b *testing.B) {
	for _, h2 := range []bool{false, true} {
		b.Run(map[bool]string{false: "http1", true: "http2"}[h2], func(b *testing.B) {
			srv, client := benchmarkHTTPServer(b, h2, false)
			payload := bytes.Repeat([]byte{'x'}, benchmarkPayloadSize)
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				resp, err := client.Post(srv.URL+UploadPath, "application/octet-stream", bytes.NewReader(payload))
				if err != nil {
					b.Fatal(err)
				}
				if resp.StatusCode != http.StatusOK {
					b.Fatalf("status = %d", resp.StatusCode)
				}
				if _, err := io.Copy(io.Discard, resp.Body); err != nil {
					b.Fatal(err)
				}
				_ = resp.Body.Close()
			}
		})
	}
}

func BenchmarkHandlerConcurrentDownloads(b *testing.B) {
	for _, h2 := range []bool{false, true} {
		b.Run(map[bool]string{false: "http1", true: "http2"}[h2], func(b *testing.B) {
			srv, client := benchmarkHTTPServer(b, h2, true)
			b.SetBytes(benchmarkPayloadSize)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					resp, err := client.Get(srv.URL + LargePath)
					if err != nil {
						b.Fatal(err)
					}
					if resp.StatusCode != http.StatusOK {
						b.Fatalf("status = %d", resp.StatusCode)
					}
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
			})
		})
	}
}
