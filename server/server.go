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
	"slices"
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
	// DefaultMaxClientConcurrency leaves room for 16 flows and server teardown.
	DefaultMaxClientConcurrency = 32
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
	// MaxClientBytes and ClientWindow form a token bucket per source IP or
	// verified signed subject. A large download or upload starts only while
	// its balance is positive, and charges actual payload bytes on completion.
	// Defaults are 8 GiB per 10 minutes. This is not a strict byte quota:
	// admitted transfers are never slowed or canceled by the budget.
	// MaxClientBytes < 0 disables both byte accounting and admission slots.
	MaxClientBytes int64
	ClientWindow   time.Duration
	// MaxClientConcurrency caps active large/download and upload handlers per
	// budget identity; nonpositive values select 32. Config/small are exempt.
	// Outstanding payload and concurrent overshoot are bounded by this cap
	// times max(LargeSize, UploadSize): 512 GiB with defaults. Increase it for
	// more simultaneous flows or clients sharing an IP. Ignored when the
	// byte budget is disabled. A handler holds its slot until return; stalled
	// requests can deny every client sharing the identity indefinitely. Use
	// separately issued signed subjects to isolate clients behind a shared IP.
	MaxClientConcurrency int
}

// clientBudget gates admission, never streaming. Its mutex protects every
// bucket, including those referenced by outstanding admission tickets.
type clientBudget struct {
	mu          sync.Mutex
	max         float64
	window      time.Duration
	concurrency int
	seen        map[string]*bucket
	now         func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
	active int
}

const (
	budgetExhausted      = "client byte budget exhausted"
	concurrencyExhausted = "client concurrency limit reached"
	maxRetryWait         = time.Duration(1<<63 - 1)
)

func newClientBudget(max int64, window time.Duration, concurrency int) *clientBudget {
	return &clientBudget{max: float64(max), window: window, concurrency: concurrency,
		seen: map[string]*bucket{}, now: time.Now}
}

// balance computes replenishment without changing last (caller holds mu).
func (b *clientBudget) balance(bk *bucket, now time.Time) float64 {
	return min(b.max, bk.tokens+b.max*max(0, now.Sub(bk.last).Seconds())/b.window.Seconds())
}

func (b *clientBudget) refill(bk *bucket, now time.Time) {
	bk.tokens = b.balance(bk, now)
	bk.last = now
}

// admission owns one slot. The admitting handler settles it exactly once,
// after all its transfer work has returned, even on an I/O error or panic.
type admission struct {
	budget *clientBudget
	bucket *bucket
}

func (b *clientBudget) admit(key string) (admission, string, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	bk := b.seen[key]
	if bk == nil {
		if len(b.seen) >= 10000 { // opportunistic cleanup, not a hard map cap
			for k, v := range b.seen {
				// Never forget ownership or debt, which can last many windows.
				if v.active == 0 && b.balance(v, now) == b.max {
					delete(b.seen, k)
				}
			}
		}
		bk = &bucket{tokens: b.max, last: now}
		b.seen[key] = bk
	}
	b.refill(bk, now)
	if bk.tokens <= 0 {
		wait := -bk.tokens / b.max * float64(b.window)
		// Out-of-range float-to-int conversion is implementation-dependent.
		if wait >= float64(maxRetryWait) {
			return admission{}, budgetExhausted, maxRetryWait
		}
		return admission{}, budgetExhausted, time.Duration(wait)
	}
	if bk.active >= b.concurrency {
		// No transfer timeout exists, so this is only an advisory retry hint.
		return admission{}, concurrencyExhausted, 0
	}
	bk.active++
	return admission{budget: b, bucket: bk}, "", 0
}

func (a admission) settle(n int64) {
	b := a.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(a.bucket, b.now())
	a.bucket.tokens -= float64(n)
	a.bucket.active--
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
	if o.MaxClientConcurrency <= 0 {
		o.MaxClientConcurrency = DefaultMaxClientConcurrency
	}
	var budget *clientBudget
	if o.MaxClientBytes > 0 {
		budget = newClientBudget(o.MaxClientBytes, o.ClientWindow, o.MaxClientConcurrency)
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
	guard := func(requestCap int64, signed bool, methods []string, h metered) http.HandlerFunc {
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
			if len(methods) > 0 && !slices.Contains(methods, r.Method) {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if budget != nil && requestCap > 0 {
				ticket, reason, wait := budget.admit(key)
				if reason != "" {
					w.Header().Set("Retry-After", strconv.FormatInt(int64(wait/time.Second)+1, 10))
					http.Error(w, reason, http.StatusTooManyRequests)
					return
				}
				// A panic cannot leak a slot or erase its potential cost. Normal
				// returns (including I/O failures) replace the cap with actual bytes.
				moved := requestCap
				defer func() { ticket.settle(moved) }()
				moved = h(w, r)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc(ConfigPath, guard(0, false, nil, func(w http.ResponseWriter, r *http.Request) int64 {
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
	mux.HandleFunc(SmallPath, guard(0, true, []string{http.MethodGet, http.MethodHead}, func(w http.ResponseWriter, r *http.Request) int64 {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "1")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte{'x'})
		return 0
	}))
	mux.HandleFunc(LargePath, guard(o.LargeSize, true, []string{http.MethodGet}, func(w http.ResponseWriter, r *http.Request) (moved int64) {
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
	mux.HandleFunc(UploadPath, guard(o.UploadSize, true, []string{http.MethodPost, http.MethodPut}, func(w http.ResponseWriter, r *http.Request) int64 {
		n, _ := io.Copy(io.Discard, io.LimitReader(r.Body, o.UploadSize))
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strconv.FormatInt(n, 10)))
		return n
	}))
	return mux
}

// LimitListener caps the number of simultaneously open connections. Accept
// blocks once max connections are open and resumes as they close; clients
// wait rather than fail, and are bounded by their own MaxDuration. Close
// releases an Accept waiting at the cap, so http.Server.Serve returns.
func LimitListener(l net.Listener, max int) net.Listener {
	if max <= 0 {
		return l
	}
	return &limitListener{Listener: l, sem: make(chan struct{}, max), done: make(chan struct{})}
}

type limitListener struct {
	net.Listener
	sem       chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func (l *limitListener) Accept() (net.Conn, error) {
	select {
	case l.sem <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.sem }}, nil
}

func (l *limitListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return l.Listener.Close()
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
