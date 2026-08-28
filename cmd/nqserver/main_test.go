package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
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
