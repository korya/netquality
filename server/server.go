// Package server implements a minimal responsiveness test server following
// draft-ietf-ippm-responsiveness Section 7 and network-quality/server's
// SERVER_SPEC.md. It backs cmd/nqserver and the library's integration tests.
package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Paths served by Handler.
const (
	ConfigPath    = "/.well-known/nq"
	SmallPath     = "/nq/small"
	LargePath     = "/nq/large"
	UploadPath    = "/nq/upload"
	defaultLarge  = 8 << 30 // 8 GiB, the spec's minimum
	streamChunk   = 64 << 10
	maxUploadSize = 16 << 30
)

// Options configures a Handler.
type Options struct {
	// BaseURL is the external URL prefix advertised in the config document,
	// e.g. https://localhost:8443. If empty, it is derived from each request.
	BaseURL string
	// LargeSize caps the large download body (default 8 GiB).
	LargeSize int64
	// TestEndpoint, if set, is advertised as the config's test_endpoint.
	TestEndpoint string
}

// Handler returns an http.Handler serving the four responsiveness endpoints.
func Handler(o Options) http.Handler {
	if o.LargeSize <= 0 {
		o.LargeSize = defaultLarge
	}
	filler := make([]byte, streamChunk)
	_, _ = rand.Read(filler)
	mux := http.NewServeMux()
	mux.HandleFunc(ConfigPath, func(w http.ResponseWriter, r *http.Request) {
		base := o.BaseURL
		if base == "" {
			base = "https://" + r.Host
		}
		doc := map[string]any{
			"version": 1,
			"urls": map[string]string{
				"small_download_url": base + SmallPath,
				"large_download_url": base + LargePath,
				"upload_url":         base + UploadPath,
			},
		}
		if o.TestEndpoint != "" {
			doc["test_endpoint"] = o.TestEndpoint
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(doc)
	})
	mux.HandleFunc(SmallPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "1")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte{'x'})
	})
	mux.HandleFunc(LargePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(o.LargeSize, 10))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		remaining := o.LargeSize
		ctx := r.Context()
		for remaining > 0 && ctx.Err() == nil {
			n := int64(len(filler))
			if remaining < n {
				n = remaining
			}
			if _, err := w.Write(filler[:n]); err != nil {
				return
			}
			remaining -= n
		}
	})
	mux.HandleFunc(UploadPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		n, _ := io.Copy(io.Discard, io.LimitReader(r.Body, maxUploadSize))
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strconv.FormatInt(n, 10)))
	})
	return mux
}

// SelfSignedCert generates an ECDSA certificate valid for localhost, the given
// extra hosts, and loopback addresses. For development only.
func SelfSignedCert(hosts ...string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "nqserver dev"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// TLSConfig returns a server TLS config with HTTP/2 enabled for cert.
func TLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2", "http/1.1"},
		MinVersion:   tls.VersionTLS12,
	}
}
