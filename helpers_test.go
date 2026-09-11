package netquality

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/korya/netquality/server"
)

// skipIfShort marks a test that starts a listener or a process: by this repo's definition it is
// not a unit test, and `go test -short` skips it.
func skipIfShort(tb testing.TB) {
	tb.Helper()
	if testing.Short() {
		tb.Skip("starts a listener or process; skipped under -short")
	}
}

// startServer starts an in-process nqserver with the given handler wrapper
// and TLS tweaks, returning the base URL.
func startServer(t *testing.T, o server.Options, wrap func(http.Handler) http.Handler, tlsCfg *tls.Config, h2 bool) *httptest.Server {
	t.Helper()
	skipIfShort(t)
	if o.MaxClientBytes == 0 {
		o.MaxClientBytes = -1 // loopback moves gigabytes per run
	}
	h := server.Handler(o)
	if wrap != nil {
		h = wrap(h)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = h2
	srv.TLS = tlsCfg
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func insecureClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server
	return &http.Client{Transport: tr}
}

func hasWarning(res *Result, substr string) bool {
	for _, w := range res.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
