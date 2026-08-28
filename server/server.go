// Package server implements a minimal responsiveness test server following
// draft-ietf-ippm-responsiveness Section 7 and network-quality/server's
// SERVER_SPEC.md. It backs cmd/nqserver and the library's integration tests.
package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
	defaultUpload = 16 << 30
	// DefaultMaxClientBytes / DefaultClientWindow: roughly four gigabit-class
	// nq runs per client per window before requests are refused with 429.
	DefaultMaxClientBytes = 8 << 30
	DefaultClientWindow   = 10 * time.Minute
	// maxTokenLength bounds the credential we are willing to compare.
	maxTokenLength = 1024
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
	// AuthToken, if set, is accepted as "Authorization: Bearer <token>" on
	// every endpoint, the config document included.
	AuthToken string
	// SigningKeys, if set, make the three test endpoints accept URLs signed
	// with any of the keys (see SignURL). The config document is not covered.
	// With neither AuthToken nor SigningKeys the server is anonymous.
	SigningKeys [][]byte
	// UploadSize caps the bytes accepted by one upload request (default 16 GiB).
	UploadSize int64
	// MaxClientBytes and ClientWindow form a per-client budget keyed by source
	// IP: a large download or upload that would exceed the budget within the
	// window is refused with 429 before it starts. Requests already running
	// are never slowed down, so measurements stay unbiased. The small
	// endpoint is exempt. MaxClientBytes < 0 disables the budget.
	MaxClientBytes int64
	ClientWindow   time.Duration
}

// clientBudget is a per-IP token bucket refilled at MaxClientBytes per
// ClientWindow. It is consulted once when a request starts.
type clientBudget struct {
	mu     sync.Mutex
	max    float64
	window time.Duration
	seen   map[string]*bucket
	now    func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newClientBudget(max int64, window time.Duration) *clientBudget {
	return &clientBudget{max: float64(max), window: window, seen: map[string]*bucket{}, now: time.Now}
}

// refill brings ip's bucket up to date and returns it (locked by caller).
func (b *clientBudget) refill(ip string) *bucket {
	now := b.now()
	bk := b.seen[ip]
	if bk == nil {
		bk = &bucket{tokens: b.max, last: now}
		b.seen[ip] = bk
		if len(b.seen) > 10000 { // bound memory: forget idle clients
			for k, v := range b.seen {
				if now.Sub(v.last) > b.window {
					delete(b.seen, k)
				}
			}
		}
	}
	bk.tokens = minf(b.max, bk.tokens+b.max*now.Sub(bk.last).Seconds()/b.window.Seconds())
	bk.last = now
	return bk
}

// allow reports whether ip may start a request: its budget must be positive.
// The request is charged what it actually moves (see charge), so a client can
// overshoot by at most one request's cap.
func (b *clientBudget) allow(ip string) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	bk := b.refill(ip)
	if bk.tokens > 0 {
		return true, 0
	}
	return false, time.Duration(-bk.tokens / b.max * float64(b.window))
}

// charge deducts the bytes a finished request moved; the bucket may go
// negative, which delays the next allow accordingly.
func (b *clientBudget) charge(ip string, n int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	bk := b.refill(ip)
	bk.tokens -= float64(n)
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// authorize checks the bearer token. It never reveals whether a token was
// close: any failure is the same 401.
func authorize(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	h := r.Header.Get("Authorization")
	if len(h) > maxTokenLength+16 {
		return false
	}
	scheme, cred, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	cred = strings.TrimSpace(cred)
	return subtle.ConstantTimeCompare([]byte(cred), []byte(token)) == 1
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="nq"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func clientIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// Handler returns an http.Handler serving the four responsiveness endpoints.
func Handler(o Options) http.Handler {
	if o.LargeSize <= 0 {
		o.LargeSize = defaultLarge
	}
	if o.UploadSize <= 0 {
		o.UploadSize = defaultUpload
	}
	if o.MaxClientBytes == 0 {
		o.MaxClientBytes = DefaultMaxClientBytes
	}
	if o.ClientWindow <= 0 {
		o.ClientWindow = DefaultClientWindow
	}
	var budget *clientBudget
	if o.MaxClientBytes > 0 {
		budget = newClientBudget(o.MaxClientBytes, o.ClientWindow)
	}
	filler := make([]byte, streamChunk)
	_, _ = rand.Read(filler)
	mux := http.NewServeMux()
	// guard wraps a route with authorization and, for metered routes, the
	// per-client budget: checked before any body is written, charged with the
	// bytes actually moved when the handler returns. A verified signed
	// subject keys the budget instead of the source IP.
	type metered func(w http.ResponseWriter, r *http.Request) (moved int64)
	authed := o.AuthToken != "" || len(o.SigningKeys) > 0
	guard := func(meter bool, signed bool, h metered) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			key := clientIP(r)
			if authed {
				ok := o.AuthToken != "" && authorize(r, o.AuthToken)
				if !ok && signed {
					if sub, sok := verifySignature(r, o.SigningKeys, time.Now()); sok {
						ok = true
						if sub != "" {
							key = "sub:" + sub
						}
					}
				}
				if !ok {
					unauthorized(w)
					return
				}
			}
			if budget != nil && meter {
				ip := key
				if ok, wait := budget.allow(ip); !ok {
					w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
					http.Error(w, "client byte budget exhausted", http.StatusTooManyRequests)
					return
				}
				budget.charge(ip, h(w, r))
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc(ConfigPath, guard(false, false, func(w http.ResponseWriter, r *http.Request) int64 {
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
		return 0
	}))
	mux.HandleFunc(SmallPath, guard(false, true, func(w http.ResponseWriter, r *http.Request) int64 {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return 0
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "1")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte{'x'})
		return 0
	}))
	mux.HandleFunc(LargePath, guard(true, true, func(w http.ResponseWriter, r *http.Request) (moved int64) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return 0
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
			written, err := w.Write(filler[:n])
			moved += int64(written)
			if err != nil {
				return moved
			}
			remaining -= n
		}
		return moved
	}))
	mux.HandleFunc(UploadPath, guard(true, true, func(w http.ResponseWriter, r *http.Request) int64 {
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return 0
		}
		n, _ := io.Copy(io.Discard, io.LimitReader(r.Body, o.UploadSize))
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strconv.FormatInt(n, 10)))
		return n
	}))
	return mux
}

// LimitListener caps the number of simultaneously open connections. Accept
// blocks once max connections are open and resumes as they close; clients
// wait rather than fail, and are bounded by their own MaxDuration.
func LimitListener(l net.Listener, max int) net.Listener {
	if max <= 0 {
		return l
	}
	return &limitListener{Listener: l, sem: make(chan struct{}, max)}
}

type limitListener struct {
	net.Listener
	sem chan struct{}
}

func (l *limitListener) Accept() (net.Conn, error) {
	l.sem <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.sem }}, nil
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
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
