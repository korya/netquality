package server

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	srv := httptest.NewUnstartedServer(Handler(Options{LargeSize: 1 << 20, TestEndpoint: "ep"}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	client := srv.Client()

	resp, err := client.Get(srv.URL + ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Version      int               `json:"version"`
		TestEndpoint string            `json:"test_endpoint"`
		URLs         map[string]string `json:"urls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if cfg.Version != 1 || cfg.TestEndpoint != "ep" || !strings.HasPrefix(cfg.URLs["large_download_url"], srv.URL) {
		t.Errorf("%+v", cfg)
	}

	resp, err = client.Get(srv.URL + SmallPath)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || len(b) != 1 || resp.Proto != "HTTP/2.0" {
		t.Errorf("small: %d %d %s", resp.StatusCode, len(b), resp.Proto)
	}

	resp, err = client.Get(srv.URL + LargePath)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if n != 1<<20 {
		t.Errorf("large: %d bytes", n)
	}

	resp, err = client.Post(srv.URL+UploadPath, "application/octet-stream", strings.NewReader(strings.Repeat("x", 12345)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(b) != "12345" {
		t.Errorf("upload echo: %s", b)
	}

	resp, err = client.Post(srv.URL+SmallPath, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Error(resp.Status)
	}
}

func TestSelfSignedCert(t *testing.T) {
	cert, err := SelfSignedCert("example.test", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.Leaf.VerifyHostname("example.test"); err != nil {
		t.Error(err)
	}
	if err := cert.Leaf.VerifyHostname("10.0.0.1"); err != nil {
		t.Error(err)
	}
	if got := TLSConfig(cert).NextProtos[0]; got != "h2" {
		t.Error(got)
	}
	_ = tls.Config{}
}
