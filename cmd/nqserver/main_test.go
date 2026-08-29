package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/korya/netquality"
	"github.com/korya/netquality/server"
)

func TestUsageAndVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if c := run(context.Background(), []string{"--version"}, &out, &errb, nil); c != exitOK || !strings.HasPrefix(out.String(), "nqserver ") {
		t.Errorf("version: %d %q", c, out.String())
	}
	if c := run(context.Background(), nil, &out, &errb, nil); c != exitUsage || !strings.Contains(errb.String(), "TLS is required") {
		t.Errorf("no tls: %d %q", c, errb.String())
	}
	if c := run(context.Background(), []string{"--bogus"}, &out, &errb, nil); c != exitUsage {
		t.Errorf("bad flag: %d", c)
	}
	if c := run(context.Background(), []string{"--cert", "/nonexistent", "--key", "/nonexistent"}, &out, &errb, nil); c != exitUsage {
		t.Errorf("bad cert: %d", c)
	}
	if c := run(context.Background(), []string{"--self-signed", "--listen", "256.256.256.256:1"}, &out, &errb, nil); c != exitFail {
		t.Errorf("bad listen: %d", c)
	}
	// A real certificate without a token is refused unless anonymous is explicit.
	dir := t.TempDir()
	cert, key := writeCert(t, dir)
	errb.Reset()
	if c := run(context.Background(), []string{"--cert", cert, "--key", key, "--listen", "127.0.0.1:0"}, &out, &errb, nil); c != exitUsage || !strings.Contains(errb.String(), "refusing to serve anonymously") {
		t.Errorf("anonymous with real cert: %d %q", c, errb.String())
	}
}

func TestTokenFromEnvAndAnonymousOptIn(t *testing.T) {
	t.Setenv("NQSERVER_AUTH_TOKEN", "from-env")
	addr := serve(t, "--self-signed")
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // self-signed
	client := &http.Client{Transport: tr}
	status := func(token string) int {
		req, _ := http.NewRequest(http.MethodGet, "https://"+addr.String()+server.SmallPath, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if status("") != http.StatusUnauthorized || status("wrong") != http.StatusUnauthorized || status("from-env") != http.StatusOK {
		t.Error("env token not enforced")
	}
	t.Setenv("NQSERVER_AUTH_TOKEN", "")
	dir := t.TempDir()
	cert, key := writeCert(t, dir)
	addr = serve(t, "--cert", cert, "--key", key, "--allow-anonymous", "--max-connections", "8")
	pool := x509.NewCertPool()
	pool.AddCert(loadLeaf(t, cert))
	client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	resp, err := client.Get("https://" + addr.String() + server.SmallPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("allow-anonymous: %s", resp.Status)
	}
}

// writeCert writes a self-signed cert/key pair to dir and returns their paths.
func writeCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	cert, err := server.SelfSignedCert()
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath = filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	keyDER, _ := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func loadLeaf(t *testing.T, certPath string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(b)
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// serve starts the server with args on an ephemeral port and returns its address.
func serve(t *testing.T, args ...string) net.Addr {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan net.Addr, 1)
	done := make(chan int, 1)
	var errb bytes.Buffer
	go func() {
		done <- run(ctx, append([]string{"--listen", "127.0.0.1:0"}, args...), &errb, &errb, func(a net.Addr) { addrCh <- a })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case c := <-done:
			if c != exitOK {
				t.Errorf("server exit %d: %s", c, errb.String())
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	select {
	case a := <-addrCh:
		return a
	case c := <-done:
		t.Fatalf("server exited early with %d: %s", c, errb.String())
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	return nil
}

func TestServeSelfSignedEndToEnd(t *testing.T) {
	addr := serve(t, "--self-signed", "--large-size", "4194304", "--test-endpoint", "127.0.0.1")
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // self-signed
	res, err := netquality.Run(context.Background(), netquality.WellKnown(addr.String()), netquality.Options{
		HTTPClient: &http.Client{Transport: tr}, IdleProbes: 2, MaxDuration: 500 * time.Millisecond, MaxBytes: 1 << 40,
		Stability: netquality.StabilityParams{Interval: 100 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Target.TestEndpoint != "127.0.0.1" || res.Download == nil || res.Upload == nil || res.Download.HTTPVersion != "HTTP/2.0" {
		t.Errorf("%+v", res.Target)
	}
}

// TestIdleTimeoutClosesQuietConnections covers SRV-11: a connection with no
// request in flight is closed after --idle-timeout, over HTTP/1.1 (EOF on the
// raw connection) and HTTP/2 (the client's next request opens a new one).
func TestIdleTimeoutClosesQuietConnections(t *testing.T) {
	addr := serve(t, "--self-signed", "--idle-timeout", "200ms")
	tlsCfg := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}} //nolint:gosec // self-signed
	c, err := tls.Dial("tcp", addr.String(), tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\n\r\n", server.ConfigPath, addr); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Close {
		t.Fatalf("status %d close=%v: keep-alive expected", res.StatusCode, res.Close)
	}
	start := time.Now()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := c.Read(make([]byte, 1)); err == nil || n != 0 {
		t.Fatalf("idle HTTP/1.1 connection still open after %s (n=%d err=%v)", time.Since(start), n, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("idle connection closed only after %s", time.Since(start))
	}

	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // self-signed
	client := &http.Client{Transport: tr}
	get := func() (reused bool, proto string) {
		var info httptrace.GotConnInfo
		ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { info = i }})
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr.String()+server.ConfigPath, nil)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		return info.Reused, res.Proto
	}
	if _, proto := get(); proto != "HTTP/2.0" {
		t.Fatalf("proto %s", proto)
	}
	if reused, _ := get(); !reused {
		t.Fatal("back-to-back request did not reuse the HTTP/2 connection")
	}
	time.Sleep(time.Second)
	if reused, _ := get(); reused {
		t.Error("HTTP/2 connection reused after the idle timeout")
	}
}

func TestServeWithCertFilesAndBaseURL(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	cert, err := server.SelfSignedCert()
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	addr := serve(t, "--cert", certPath, "--key", keyPath, "--base-url", "https://nq.example.test:8443", "--auth-token", "s3cret")
	pool := x509.NewCertPool()
	pool.AddCert(cert.Leaf)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	// A real certificate requires a token: anonymous requests get 401.
	resp, err := client.Get("https://" + addr.String() + server.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("anonymous: %s %v", resp.Status, resp.Header)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://"+addr.String()+server.ConfigPath, nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc struct {
		URLs map[string]string `json:"urls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(doc.URLs["upload_url"], "https://nq.example.test:8443/") {
		t.Errorf("urls = %v", doc.URLs)
	}
	_ = elliptic.P256
	_ = rand.Reader
}

func TestSignSubcommandAndSigningKeyFlag(t *testing.T) {
	var out, errb bytes.Buffer
	// Generate a key, then sign URLs with it.
	if c := run(context.Background(), []string{"sign", "--new-key"}, &out, &errb, nil); c != exitOK || len(strings.TrimSpace(out.String())) != 64 {
		t.Fatalf("new-key: %d %q", c, out.String())
	}
	key := strings.TrimSpace(out.String())
	out.Reset()
	if c := run(context.Background(), []string{"sign", "--key", key, "--ttl", "5m", "--sub", "dev1", "https://h/nq/small", "https://h/nq/large?bytes=1"}, &out, &errb, nil); c != exitOK {
		t.Fatalf("sign: %d %s", c, errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "sig=") || !strings.Contains(lines[1], "bytes=1") || !strings.Contains(lines[1], "sub=dev1") {
		t.Errorf("signed urls: %v", lines)
	}
	for _, args := range [][]string{
		{"sign"}, // no URLs
		{"sign", "--key", "short", "https://h/p"},             // bad key
		{"sign", "--key", key, "--ttl", "48h", "https://h/p"}, // beyond max TTL
		{"sign", "--key", key, "https://h"},                   // no path
	} {
		if c := run(context.Background(), args, &out, &errb, nil); c != exitUsage {
			t.Errorf("%v: exit %d", args, c)
		}
	}

	// A server with only a signing key accepts signed URLs and refuses others.
	dir := t.TempDir()
	cert, keyFile := writeCert(t, dir)
	addr := serve(t, "--cert", cert, "--key", keyFile, "--signing-key", key)
	pool := x509.NewCertPool()
	pool.AddCert(loadLeaf(t, cert))
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	out.Reset()
	if c := run(context.Background(), []string{"sign", "--key", key, "https://" + addr.String() + server.SmallPath}, &out, &errb, nil); c != exitOK {
		t.Fatal(errb.String())
	}
	resp, err := client.Get(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("signed request: %s", resp.Status)
	}
	resp, err = client.Get("https://" + addr.String() + server.SmallPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unsigned request: %s", resp.Status)
	}
	// Env var form and a bad key at startup.
	t.Setenv("NQSERVER_SIGNING_KEY", "nope")
	if c := run(context.Background(), []string{"--self-signed", "--listen", "127.0.0.1:0"}, &out, &errb, nil); c != exitUsage {
		t.Errorf("bad env signing key: exit %d", c)
	}
}
