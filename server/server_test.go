package server

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestHandlerBaseURLAndMethods(t *testing.T) {
	srv := httptest.NewTLSServer(Handler(Options{BaseURL: "https://nq.example.test", LargeSize: 3}))
	defer srv.Close()
	client := srv.Client()

	resp, err := client.Get(srv.URL + ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		URLs         map[string]string `json:"urls"`
		TestEndpoint *string           `json:"test_endpoint"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&cfg)
	_ = resp.Body.Close()
	if cfg.URLs["small_download_url"] != "https://nq.example.test"+SmallPath || cfg.TestEndpoint != nil {
		t.Errorf("%+v", cfg)
	}

	resp, err = client.Get(srv.URL + LargePath)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if len(b) != 3 || resp.Header.Get("Content-Length") != "3" || resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("large: %d bytes, headers %v", len(b), resp.Header)
	}

	for _, tc := range []struct{ method, path string }{{http.MethodPost, LargePath}, {http.MethodGet, UploadPath}, {http.MethodDelete, SmallPath}} {
		req, _ := http.NewRequest(tc.method, srv.URL+tc.path, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %s", tc.method, tc.path, resp.Status)
		}
	}
	resp, err = client.Head(srv.URL + SmallPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD small: %s", resp.Status)
	}
}

func TestAuthorize(t *testing.T) {
	req := func(h string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if h != "" {
			r.Header.Set("Authorization", h)
		}
		return r
	}
	long := strings.Repeat("x", 5000)
	cases := []struct {
		name  string
		token string
		hdr   string
		want  bool
	}{
		{"anonymous server accepts anything", "", "", true},
		{"anonymous server ignores tokens", "", "Bearer whatever", true},
		{"exact", "s3cret", "Bearer s3cret", true},
		{"scheme is case-insensitive", "s3cret", "bearer s3cret", true},
		{"surrounding whitespace tolerated", "s3cret", "Bearer   s3cret  ", true},
		{"missing header", "s3cret", "", false},
		{"empty credential", "s3cret", "Bearer ", false},
		{"wrong token", "s3cret", "Bearer s3cre", false},
		{"prefix of token", "s3cret", "Bearer s3", false},
		{"token with suffix", "s3cret", "Bearer s3cret1", false},
		{"wrong scheme", "s3cret", "Basic s3cret", false},
		{"no scheme", "s3cret", "s3cret", false},
		{"very long credential", "s3cret", "Bearer " + long, false},
		{"very long token configured", long, "Bearer " + long, false}, // beyond maxTokenLength: never matches
		{"unicode token", "pässwörd", "Bearer pässwörd", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := authorize(req(tc.hdr), tc.token); got != tc.want {
				t.Errorf("authorize = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHandlerAuthOnEveryRoute(t *testing.T) {
	srv := httptest.NewTLSServer(Handler(Options{AuthToken: "tok", LargeSize: 16}))
	defer srv.Close()
	client := srv.Client()
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, ConfigPath}, {http.MethodGet, SmallPath}, {http.MethodGet, LargePath}, {http.MethodPost, UploadPath},
	} {
		for _, hdr := range []string{"", "Bearer nope", "Bearer tok"} {
			req, _ := http.NewRequest(tc.method, srv.URL+tc.path, strings.NewReader("abc"))
			if hdr != "" {
				req.Header.Set("Authorization", hdr)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if hdr == "Bearer tok" {
				if resp.StatusCode != http.StatusOK {
					t.Errorf("%s %s with token: %s", tc.method, tc.path, resp.Status)
				}
				continue
			}
			if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != `Bearer realm="nq"` || len(b) > 64 {
				t.Errorf("%s %s hdr=%q: %s %q body=%d bytes", tc.method, tc.path, hdr, resp.Status, resp.Header.Get("WWW-Authenticate"), len(b))
			}
		}
	}
}

func TestClientBudget(t *testing.T) {
	now := time.Unix(0, 0)
	b := newClientBudget(1000, time.Minute)
	b.now = func() time.Time { return now }
	if ok, _ := b.allow("a"); !ok {
		t.Fatal("fresh client must be allowed")
	}
	b.charge("a", 1500) // one request may overshoot
	ok, wait := b.allow("a")
	if ok || wait <= 0 || wait > time.Minute {
		t.Errorf("exhausted: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := b.allow("b"); !ok {
		t.Error("budgets are per client")
	}
	now = now.Add(31 * time.Second) // refill: -500 + 516 > 0
	if ok, _ := b.allow("a"); !ok {
		t.Error("budget must refill over the window")
	}
	now = now.Add(time.Hour)
	b.charge("a", 10)
	if b.seen["a"].tokens > 1000 {
		t.Error("bucket must not exceed max")
	}
}

func TestHandlerBudgetAndUploadCap(t *testing.T) {
	// Budget of 100 bytes: the first large download (64 B) is allowed, the
	// next metered request is refused with 429 + Retry-After; small is exempt.
	srv := httptest.NewTLSServer(Handler(Options{LargeSize: 64, UploadSize: 10, MaxClientBytes: 100, ClientWindow: time.Hour}))
	defer srv.Close()
	client := srv.Client()
	get := func(path string) *http.Response {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp
	}
	if r := get(LargePath); r.StatusCode != http.StatusOK {
		t.Fatalf("first large: %s", r.Status)
	}
	if r := get(LargePath); r.StatusCode != http.StatusOK {
		t.Fatalf("second large (36 B budget left, allowed once): %s", r.Status)
	}
	r := get(LargePath)
	if r.StatusCode != http.StatusTooManyRequests || r.Header.Get("Retry-After") == "" {
		t.Errorf("third large: %s retry-after=%q", r.Status, r.Header.Get("Retry-After"))
	}
	if ra, _ := strconv.Atoi(r.Header.Get("Retry-After")); ra <= 0 || ra > 3600 {
		t.Errorf("Retry-After = %q", r.Header.Get("Retry-After"))
	}
	for i := 0; i < 5; i++ {
		if r := get(SmallPath); r.StatusCode != http.StatusOK {
			t.Errorf("small must be exempt from the budget: %s", r.Status)
		}
	}
	if r := get(ConfigPath); r.StatusCode != http.StatusOK {
		t.Errorf("config must be exempt: %s", r.Status)
	}
	// Upload cap: only UploadSize bytes are read; the client sees a clean 200.
	srv2 := httptest.NewTLSServer(Handler(Options{UploadSize: 10, MaxClientBytes: -1}))
	defer srv2.Close()
	resp, err := srv2.Client().Post(srv2.URL+UploadPath, "application/octet-stream", strings.NewReader(strings.Repeat("x", 1000)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "10" {
		t.Errorf("upload cap: %s %q", resp.Status, body)
	}
}

func TestLimitListener(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := LimitListener(inner, 2)
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	dial := func() net.Conn {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c1, c2, c3 := dial(), dial(), dial()
	defer func() { _ = c1.Close() }()
	defer func() { _ = c2.Close() }()
	defer func() { _ = c3.Close() }()
	a1, a2 := <-accepted, <-accepted
	select {
	case <-accepted:
		t.Fatal("third connection accepted beyond the limit")
	case <-time.After(150 * time.Millisecond):
	}
	_ = a1.Close()
	select {
	case a3 := <-accepted:
		_ = a3.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("slot not released on close")
	}
	_ = a2.Close()
	_ = a2.Close() // double close must not release twice
	if LimitListener(inner, 0) != inner {
		t.Error("0 must mean unlimited")
	}
}
