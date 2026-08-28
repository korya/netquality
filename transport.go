package netquality

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// transportFactory produces per-flow round trippers so that each load flow
// owns its own transport connection, as the draft requires.
type transportFactory struct {
	base         *http.Transport // nil when the user supplied a custom RoundTripper
	custom       http.RoundTripper
	testEndpoint string // dial override host (draft "test_endpoint"), "" if none
	urlHost      string // host[:port] from the config URLs
	dialTimeout  time.Duration
	resolvedIPs  atomic.Pointer[[]string]
}

func newTransportFactory(client *http.Client, cfg *ServerConfig, urlHost string) (*transportFactory, []string) {
	var warnings []string
	f := &transportFactory{urlHost: urlHost, dialTimeout: 10 * time.Second}
	if cfg.TestEndpoint != "" && cfg.TestEndpoint != hostOnly(urlHost) {
		f.testEndpoint = cfg.TestEndpoint
	}
	rt := client.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	if t, ok := rt.(*http.Transport); ok {
		f.base = t
	} else {
		f.custom = rt
		warnings = append(warnings, "custom RoundTripper in use: load flows may share connections and test_endpoint is ignored")
	}
	return f, warnings
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// newTransport returns a fresh transport with its own connection pool.
// keepAlive=false yields a transport whose every request uses a brand-new
// connection (foreign/idle probes).
func (f *transportFactory) newTransport(keepAlive bool) http.RoundTripper {
	if f.custom != nil {
		return f.custom
	}
	t := f.base.Clone()
	t.DisableCompression = true
	t.ForceAttemptHTTP2 = true
	t.MaxConnsPerHost = 0
	if !keepAlive {
		t.DisableKeepAlives = true
	}
	if t.TLSClientConfig == nil {
		t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	inner := t.DialContext
	if inner == nil {
		d := &net.Dialer{Timeout: f.dialTimeout, KeepAlive: 30 * time.Second}
		inner = d.DialContext
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if f.testEndpoint != "" && hostOnly(addr) == hostOnly(f.urlHost) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			addr = net.JoinHostPort(f.testEndpoint, port)
		}
		c, err := inner(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if ra := c.RemoteAddr(); ra != nil {
			f.recordIP(hostOnly(ra.String()))
		}
		return c, nil
	}
	return t
}

func (f *transportFactory) recordIP(ip string) {
	for {
		cur := f.resolvedIPs.Load()
		var next []string
		if cur != nil {
			for _, x := range *cur {
				if x == ip {
					return
				}
			}
			next = append(next, *cur...)
		}
		next = append(next, ip)
		if f.resolvedIPs.CompareAndSwap(cur, &next) {
			return
		}
	}
}

func (f *transportFactory) ips() []string {
	if p := f.resolvedIPs.Load(); p != nil {
		return append([]string(nil), *p...)
	}
	return nil
}

// closeIdle releases pooled connections of a transport we created.
func closeIdle(rt http.RoundTripper) {
	if t, ok := rt.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
}

func checkStatus(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	return nil
}
