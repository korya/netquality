package main

import (
	"io"
	"log"
	"net/http/httptest"
	"testing"

	"github.com/korya/netquality/server"
)

func BenchmarkCLIThroughput(b *testing.B) {
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
			args := []string{
				"--config-url", srv.URL + server.ConfigPath,
				"--insecure", "--download-only", "--json",
				"--idle-probes", "-1", "--max-flows", "4",
				"--max-duration", "100ms", "--max-bytes", "1MiB",
			}
			b.SetBytes(1 << 20)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if code := run(args, io.Discard, io.Discard); code != exitOK {
					b.Fatalf("exit code = %d", code)
				}
			}
		})
	}
}
