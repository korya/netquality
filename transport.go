package netquality

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
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
	urlHostPort  string // urlHost with the scheme's default port filled in
	dialTimeout  time.Duration
	customTLS    bool    // the base transport has DialTLSContext/DialTLS
	remote       addrSet // server IPs the flows connected to
	local        addrSet // source IPs the flows went out on
}

// addrSet is a small lock-free, deduplicating, insertion-ordered set of
// address strings, safe for concurrent use from dial callbacks.
type addrSet struct{ p atomic.Pointer[[]string] }

func (s *addrSet) add(a string) {
	if a == "" {
		return
	}
	for {
		cur := s.p.Load()
		var next []string
		if cur != nil {
			for _, x := range *cur {
				if x == a {
					return
				}
			}
			next = append(next, *cur...)
		}
		next = append(next, a)
		if s.p.CompareAndSwap(cur, &next) {
			return
		}
	}
}

func (s *addrSet) list() []string {
	if p := s.p.Load(); p != nil {
		return append([]string(nil), *p...)
	}
	return nil
}

func newTransportFactory(client *http.Client, cfg *ServerConfig, u *url.URL) (*transportFactory, []string) {
	var warnings []string
	urlHost := u.Host
	f := &transportFactory{urlHost: urlHost, urlHostPort: urlHost, dialTimeout: 10 * time.Second}
	if _, _, err := net.SplitHostPort(urlHost); err != nil {
		port := "443"
		if u.Scheme == "http" {
			port = "80"
		}
		f.urlHostPort = net.JoinHostPort(urlHost, port)
	}
	if cfg.TestEndpoint != "" && cfg.TestEndpoint != hostOnly(urlHost) {
		f.testEndpoint = cfg.TestEndpoint
	}
	rt := client.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	if t, ok := rt.(*http.Transport); ok {
		f.base = t
		f.customTLS = t.DialTLSContext != nil || t.DialTLS != nil //nolint:staticcheck // DialTLS is deprecated but still honoured by net/http
	} else {
		f.custom = rt
		warnings = append(warnings, "custom RoundTripper in use: load flows may share connections, probes may reuse connections (no per-stage timings), test_endpoint is ignored")
	}
	return f, warnings
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// ownedTransport is a transport the library created and therefore must tear
// down completely (INV-4). CloseIdleConnections only closes idle
// connections — an HTTP/2 connection still winding down a cancelled stream is
// not idle and would outlive Run — so every dialled connection is tracked and
// closed explicitly by closeAll.
type ownedTransport struct {
	*http.Transport
	mu    sync.Mutex
	conns map[net.Conn]struct{}
	// closed latches at teardown. net/http dials in a goroutine that outlives
	// the cancelled request that started it, so a dial can still complete after
	// closeAll has taken its snapshot; that connection would otherwise be filed
	// in a map nobody reads again and stay open for ever. Closing it on arrival
	// costs six lines. In practice the abandoned dial is cancelled with the
	// phase and never gets this far — this was not observed firing.
	closed bool
}

func (o *ownedTransport) track(c net.Conn) net.Conn {
	tc := &trackedConn{Conn: c, owner: o}
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		_ = tc.Close()
		return tc
	}
	o.conns[tc] = struct{}{}
	o.mu.Unlock()
	return tc
}

// trackRaw registers c without wrapping it. net/http only upgrades to HTTP/2
// when the dialled value is exactly a *tls.Conn, so connections from a
// caller's DialTLSContext cannot be wrapped; they stay in the map until
// closeAll, which is bounded by the dials of one run.
func (o *ownedTransport) trackRaw(c net.Conn) net.Conn {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		_ = c.Close()
		return c
	}
	o.conns[c] = struct{}{}
	o.mu.Unlock()
	return c
}

func (o *ownedTransport) forget(c net.Conn) {
	o.mu.Lock()
	delete(o.conns, c)
	o.mu.Unlock()
}

// closeAll closes idle connections through the transport and every live one
// directly.
func (o *ownedTransport) closeAll() {
	o.CloseIdleConnections()
	o.mu.Lock()
	o.closed = true
	conns := make([]net.Conn, 0, len(o.conns))
	for c := range o.conns {
		conns = append(conns, c)
	}
	o.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

type trackedConn struct {
	net.Conn
	owner *ownedTransport
	once  sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() { c.owner.forget(c) })
	return c.Conn.Close()
}

// newTransport returns a fresh transport with its own connection pool.
// keepAlive=false yields a transport whose every request uses a brand-new
// connection (foreign/idle probes).
func (f *transportFactory) newTransport(keepAlive bool) http.RoundTripper {
	if f.custom != nil {
		return f.custom
	}
	t := f.base.Clone()
	owned := &ownedTransport{Transport: t, conns: map[net.Conn]struct{}{}}
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
		// Only rewrite dials to the origin itself; a proxy dial (which may
		// share the origin's host) must be left alone.
		if f.testEndpoint != "" && addr == f.urlHostPort {
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
			f.remote.add(hostOnly(ra.String()))
		}
		if la := c.LocalAddr(); la != nil {
			f.local.add(hostOnly(la.String()))
		}
		return owned.track(c), nil
	}
	if f.customTLS {
		// A custom TLS dialer bypasses DialContext for https, so track its
		// connections here. The address is not rewritten for test_endpoint:
		// the caller's dialer would verify the certificate against it.
		innerTLS := t.DialTLSContext
		if innerTLS == nil {
			legacy := t.DialTLS //nolint:staticcheck // deprecated but still honoured by net/http
			innerTLS = func(_ context.Context, network, addr string) (net.Conn, error) { return legacy(network, addr) }
		}
		t.DialTLS = nil //nolint:staticcheck // DialTLSContext takes precedence; make that explicit
		t.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := innerTLS(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			if ra := c.RemoteAddr(); ra != nil {
				f.remote.add(hostOnly(ra.String()))
			}
			if la := c.LocalAddr(); la != nil {
				f.local.add(hostOnly(la.String()))
			}
			return owned.trackRaw(c), nil
		}
	}
	return owned
}

// closeIdle tears down a transport we created; a caller-supplied
// RoundTripper is left alone.
func closeIdle(rt http.RoundTripper) {
	switch t := rt.(type) {
	case *ownedTransport:
		t.closeAll()
	case *http.Transport:
		t.CloseIdleConnections()
	}
}

// explicitProxy reports the proxy the base transport would use for rawURL, or
// nil. A custom RoundTripper cannot be inspected and yields nil.
func (f *transportFactory) explicitProxy(rawURL string) *url.URL {
	if f.base == nil || f.base.Proxy == nil {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil
	}
	pu, err := f.base.Proxy(req)
	if err != nil || pu == nil {
		return nil
	}
	clean := *pu
	clean.User = nil
	return &clean
}

// sctExtensionOID is the X.509 extension carrying embedded Certificate
// Transparency SCTs (RFC 6962 §3.3). Publicly trusted leaf certificates carry
// it; certificates minted by a private or TLS-inspecting CA do not.
var sctExtensionOID = []int{1, 3, 6, 1, 4, 1, 11129, 2, 4, 2}

// inspectionVendors are issuer substrings of well-known TLS-inspection products.
var inspectionVendors = []string{"zscaler", "netskope", "palo alto", "fortinet", "fortigate", "cisco umbrella", "blue coat", "bluecoat", "symantec", "check point", "mcafee", "forcepoint", "sophos", "websense"}

// inspectChain reports TLS interception when the chain verified but the leaf
// is not publicly trusted. It returns nil when verification was skipped
// (InsecureSkipVerify) or the leaf carries SCTs.
func inspectChain(cs tls.ConnectionState) *ProxyInfo {
	if len(cs.VerifiedChains) == 0 || len(cs.PeerCertificates) == 0 {
		return nil
	}
	if len(cs.SignedCertificateTimestamps) > 0 || hasSCTExtension(cs.PeerCertificates[0]) {
		return nil
	}
	issuer := cs.PeerCertificates[0].Issuer.String()
	info := &ProxyInfo{TLSInterception: true, Issuer: issuer}
	lower := strings.ToLower(issuer)
	for _, v := range inspectionVendors {
		if strings.Contains(lower, v) {
			info.Reason = fmt.Sprintf("certificate issued by TLS-inspection product (%s); measurements cover the client→proxy leg", issuer)
			return info
		}
	}
	info.Reason = fmt.Sprintf("certificate chain verified but the leaf has no Certificate Transparency SCTs, so it is not publicly trusted: a TLS-inspecting proxy or a private CA (%s) is in the path", issuer)
	return info
}

func hasSCTExtension(c *x509.Certificate) bool {
	for _, e := range c.Extensions {
		if e.Id.Equal(sctExtensionOID) {
			return true
		}
	}
	return false
}

func checkStatus(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	return nil
}
