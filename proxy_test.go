package netquality

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

func testCert(t *testing.T, issuerOrg string, withSCT bool) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		// Self-signed: the issuer is the subject.
		Subject:   pkix.Name{CommonName: "example.test", Organization: []string{issuerOrg}},
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter:  time.Now().Add(time.Hour),
	}
	if withSCT {
		tmpl.ExtraExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier(sctExtensionOID), Value: []byte{0x04, 0x00}}}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestInspectChain(t *testing.T) {
	public := testCert(t, "Public CA", true)
	private := testCert(t, "Corp Root", false)
	vendor := testCert(t, "Zscaler Inc.", false)
	tests := []struct {
		name     string
		cs       tls.ConnectionState
		want     bool
		contains string
	}{
		{"public leaf with SCT", tls.ConnectionState{PeerCertificates: []*x509.Certificate{public}, VerifiedChains: [][]*x509.Certificate{{public}}}, false, ""},
		{"private CA, verified", tls.ConnectionState{PeerCertificates: []*x509.Certificate{private}, VerifiedChains: [][]*x509.Certificate{{private}}}, true, "private CA"},
		{"inspection vendor", tls.ConnectionState{PeerCertificates: []*x509.Certificate{vendor}, VerifiedChains: [][]*x509.Certificate{{vendor}}}, true, "TLS-inspection product"},
		{"verification skipped (insecure)", tls.ConnectionState{PeerCertificates: []*x509.Certificate{private}}, false, ""},
		{"SCTs via TLS extension", tls.ConnectionState{PeerCertificates: []*x509.Certificate{private}, VerifiedChains: [][]*x509.Certificate{{private}}, SignedCertificateTimestamps: [][]byte{{1}}}, false, ""},
		{"empty", tls.ConnectionState{}, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := inspectChain(tc.cs)
			if (got != nil) != tc.want {
				t.Fatalf("got %+v, want detection=%v", got, tc.want)
			}
			if got != nil && (!got.TLSInterception || !strings.Contains(got.Reason, tc.contains) || got.Issuer == "") {
				t.Errorf("%+v", got)
			}
		})
	}
}

// startConnectProxy runs a minimal HTTP CONNECT proxy and returns its URL and a
// counter of tunnelled connections.
func startConnectProxy(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	hits := new(atomic.Int32)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				up, err := net.Dial("tcp", req.Host)
				if err != nil {
					return
				}
				defer func() { _ = up.Close() }()
				hits.Add(1)
				_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
				go func() { _, _ = io.Copy(up, c) }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	return "http://user:secret@" + ln.Addr().String(), hits
}

func quickOpts(client *http.Client) Options {
	return Options{HTTPClient: client, Directions: Download, IdleProbes: 2,
		MaxDuration: 400 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability()}
}

func TestExplicitProxyDetected(t *testing.T) {
	srv := httptest.NewUnstartedServer(server.Handler(server.Options{TestEndpoint: "nq.example.test"}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	proxyURL, hits := startConnectProxy(t)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(mustParse(t, proxyURL))
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server

	res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, quickOpts(&http.Client{Transport: tr}))
	if err != nil {
		t.Fatal(err)
	}
	p := res.Target.Proxy
	if p == nil || !p.Explicit || p.TLSInterception {
		t.Fatalf("proxy = %+v", p)
	}
	if strings.Contains(p.URL, "secret") || !strings.HasSuffix(p.URL, strings.TrimPrefix(proxyURL, "http://user:secret@")) {
		t.Errorf("url = %q", p.URL)
	}
	if hits.Load() == 0 {
		t.Error("proxy was not used")
	}
	if res.Download.HTTPVersion != "HTTP/2.0" {
		t.Errorf("http version through proxy = %q", res.Download.HTTPVersion)
	}
	var sawProxy, sawEndpoint bool
	for _, w := range res.Warnings {
		sawProxy = sawProxy || strings.Contains(w, "explicit proxy")
		sawEndpoint = sawEndpoint || strings.Contains(w, "test_endpoint")
	}
	if !sawProxy || !sawEndpoint {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestTLSInterceptionDetected(t *testing.T) {
	srv := httptest.NewUnstartedServer(server.Handler(server.Options{}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	// Trust the server's self-signed cert like a corporate root: the chain
	// verifies, but the leaf carries no SCTs.
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}

	res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, quickOpts(&http.Client{Transport: tr}))
	if err != nil {
		t.Fatal(err)
	}
	p := res.Target.Proxy
	if p == nil || !p.TLSInterception || p.Explicit || !strings.Contains(p.Issuer, "Acme") {
		t.Fatalf("proxy = %+v", p)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "TLS interception") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestInsecureDoesNotFlagProxy(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	res, err := Run(context.Background(), target, quickOpts(client))
	if err != nil {
		t.Fatal(err)
	}
	if res.Target.Proxy != nil {
		t.Errorf("proxy = %+v", res.Target.Proxy)
	}
	data, _ := json.Marshal(res)
	if strings.Contains(string(data), `"proxy"`) {
		t.Error("proxy key must be omitted when nil")
	}
	data, _ = json.Marshal(ResolvedTarget{Proxy: &ProxyInfo{Explicit: true, URL: "http://p:1"}})
	if !strings.Contains(string(data), `"proxy":{"explicit":true,"url":"http://p:1","tls_interception":false}`) {
		t.Errorf("json = %s", data)
	}
}

func TestExplicitProxyNilWithoutProxy(t *testing.T) {
	f := &transportFactory{base: &http.Transport{Proxy: nil}}
	if f.explicitProxy("https://example.test/") != nil {
		t.Error("nil Proxy func must yield nil")
	}
	f = &transportFactory{custom: http.DefaultTransport}
	if f.explicitProxy("https://example.test/") != nil {
		t.Error("custom RoundTripper must yield nil")
	}
	f = &transportFactory{base: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }}}
	if f.explicitProxy("https://example.test/") != nil {
		t.Error("DIRECT must yield nil")
	}
}

func mustParse(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
