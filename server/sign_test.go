package server

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func TestSignURLVector(t *testing.T) {
	// Fixed vector so other-language issuers can check their implementation.
	got, err := SignURL(testKey, "https://nq.example/nq/large?bytes=4000000000", time.Unix(1800000000, 0), "device-42")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://nq.example/nq/large?bytes=4000000000&exp=1800000000&sig=" +
		base64.RawURLEncoding.EncodeToString(signature(testKey, "/nq/large", 1800000000, "device-42")) + "&sub=device-42"
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
	if _, err := SignURL(nil, "https://x/nq/large", time.Now(), ""); err == nil {
		t.Error("empty key must fail")
	}
	if _, err := SignURL(testKey, "https://x", time.Now(), ""); err == nil {
		t.Error("no path must fail")
	}
	if _, err := SignURL(testKey, "https://x/p", time.Now(), strings.Repeat("s", 300)); err == nil {
		t.Error("oversized subject must fail")
	}
}

func TestVerifySignature(t *testing.T) {
	now := time.Unix(1800000000, 0)
	exp := now.Add(time.Hour)
	signed, _ := SignURL(testKey, "https://nq.example/nq/large?bytes=10", exp, "dev")
	u, _ := url.Parse(signed)
	q := u.Query()
	req := func(path, rawq string) *http.Request {
		return httptest.NewRequest(http.MethodGet, "https://nq.example"+path+"?"+rawq, nil)
	}
	reordered := "sub=" + q.Get("sub") + "&sig=" + q.Get("sig") + "&bytes=10&exp=" + q.Get("exp")
	cases := []struct {
		name    string
		r       *http.Request
		keys    [][]byte
		at      time.Time
		wantSub string
		ok      bool
	}{
		{"valid", req(u.Path, u.RawQuery), [][]byte{testKey}, now, "dev", true},
		{"reordered params", req(u.Path, reordered), [][]byte{testKey}, now, "dev", true},
		{"extra unsigned param", req(u.Path, u.RawQuery+"&x=1"), [][]byte{testKey}, now, "dev", true},
		{"rotated key (second in list)", req(u.Path, u.RawQuery), [][]byte{[]byte("another-key-of-16b"), testKey}, now, "dev", true},
		{"within leeway", req(u.Path, u.RawQuery), [][]byte{testKey}, exp.Add(SignatureLeeway - time.Second), "dev", true},
		{"expired", req(u.Path, u.RawQuery), [][]byte{testKey}, exp.Add(SignatureLeeway + time.Second), "", false},
		{"ttl beyond maximum", req(u.Path, u.RawQuery), [][]byte{testKey}, now.Add(-MaxSignatureTTL - time.Hour), "", false},
		{"wrong key", req(u.Path, u.RawQuery), [][]byte{[]byte("another-key-of-16b")}, now, "", false},
		{"no keys", req(u.Path, u.RawQuery), nil, now, "", false},
		{"tampered path", req("/nq/upload", u.RawQuery), [][]byte{testKey}, now, "", false},
		{"tampered sub", req(u.Path, strings.Replace(u.RawQuery, "sub=dev", "sub=other", 1)), [][]byte{testKey}, now, "", false},
		{"tampered exp", req(u.Path, strings.Replace(u.RawQuery, "exp=1800003600", "exp=1800009999", 1)), [][]byte{testKey}, now, "", false},
		{"missing sig", req(u.Path, "exp="+q.Get("exp")), [][]byte{testKey}, now, "", false},
		{"missing exp", req(u.Path, "sig="+q.Get("sig")), [][]byte{testKey}, now, "", false},
		{"bad base64", req(u.Path, "exp="+q.Get("exp")+"&sig=%%%"), [][]byte{testKey}, now, "", false},
		{"short sig", req(u.Path, "exp="+q.Get("exp")+"&sig=AAAA"), [][]byte{testKey}, now, "", false},
		{"non-numeric exp", req(u.Path, "exp=soon&sig="+q.Get("sig")), [][]byte{testKey}, now, "", false},
		{"oversized sub", req(u.Path, "exp="+q.Get("exp")+"&sig="+q.Get("sig")+"&sub="+strings.Repeat("s", 300)), [][]byte{testKey}, now, "", false},
		{"no params", req(u.Path, ""), [][]byte{testKey}, now, "", false},
		// Encodings and canonicalisation.
		{"padded base64url sig", req(u.Path, "exp="+q.Get("exp")+"&sub=dev&sig="+base64.URLEncoding.EncodeToString(signature(testKey, u.Path, 1800003600, "dev"))), [][]byte{testKey}, now, "dev", true},
		{"standard base64 sig", req(u.Path, "exp="+q.Get("exp")+"&sub=dev&sig="+url.QueryEscape(base64.StdEncoding.EncodeToString(signature(testKey, u.Path, 1800003600, "dev")))), [][]byte{testKey}, now, "dev", true},
		{"percent-encoded path verifies against decoded path", req("/nq/%6Carge", u.RawQuery), [][]byte{testKey}, now, "dev", true},
		{"duplicate exp: first wins", req(u.Path, u.RawQuery+"&exp=1900000000"), [][]byte{testKey}, now, "dev", true},
		{"duplicate sig: first wins", req(u.Path, u.RawQuery+"&sig=AAAA"), [][]byte{testKey}, now, "dev", true},
		{"duplicate sig: forged first", req(u.Path, "sig=AAAA&"+u.RawQuery), [][]byte{testKey}, now, "", false},
		{"exp exactly now", req(u.Path, func() string {
			s, _ := SignURL(testKey, "https://x"+u.Path, now, "dev")
			p, _ := url.Parse(s)
			return p.RawQuery
		}()), [][]byte{testKey}, now, "dev", true},
		{"exp zero", req(u.Path, "exp=0&sub=dev&sig="+base64.RawURLEncoding.EncodeToString(signature(testKey, u.Path, 0, "dev"))), [][]byte{testKey}, now, "", false},
		{"exp negative", req(u.Path, "exp=-5&sub=dev&sig="+base64.RawURLEncoding.EncodeToString(signature(testKey, u.Path, -5, "dev"))), [][]byte{testKey}, now, "", false},
		{"exp overflow", req(u.Path, "exp=99999999999999999999&sig="+q.Get("sig")), [][]byte{testKey}, now, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub, ok := verifySignature(tc.r, tc.keys, tc.at)
			if ok != tc.ok || sub != tc.wantSub {
				t.Errorf("got (%q,%v) want (%q,%v)", sub, ok, tc.wantSub, tc.ok)
			}
		})
	}
	// Subjects with reserved characters round-trip when percent-encoded by
	// SignURL; a raw "+" from a careless issuer decodes to a space and fails
	// closed.
	for _, sub := range []string{"a+b", "a b", "x&y=z", "ünïcødé", "dev/7#1"} {
		s, err := SignURL(testKey, "https://x"+u.Path, exp, sub)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := url.Parse(s)
		if got, ok := verifySignature(req(p.Path, p.RawQuery), [][]byte{testKey}, now); !ok || got != sub {
			t.Errorf("sub %q: (%q,%v)", sub, got, ok)
		}
	}
	rawPlus := "exp=" + q.Get("exp") + "&sub=a+b&sig=" + base64.RawURLEncoding.EncodeToString(signature(testKey, u.Path, 1800003600, "a+b"))
	if _, ok := verifySignature(req(u.Path, rawPlus), [][]byte{testKey}, now); ok {
		t.Error("unencoded '+' in sub must fail closed (decodes to a space)")
	}
	// A URL signed without a subject verifies with an empty subject.
	nosub, _ := SignURL(testKey, "https://nq.example/nq/small", exp, "")
	nu, _ := url.Parse(nosub)
	if sub, ok := verifySignature(req(nu.Path, nu.RawQuery), [][]byte{testKey}, now); !ok || sub != "" {
		t.Errorf("no-subject URL: (%q,%v)", sub, ok)
	}
}

func TestParseSigningKey(t *testing.T) {
	hexKey := "30313233343536373839616263646566" // "0123456789abcdef"
	if k, err := ParseSigningKey(hexKey); err != nil || string(k) != "0123456789abcdef" {
		t.Errorf("hex: %q %v", k, err)
	}
	if k, err := ParseSigningKey(base64.RawURLEncoding.EncodeToString(testKey)); err != nil || string(k) != string(testKey) {
		t.Errorf("base64: %q %v", k, err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "k")
	_ = os.WriteFile(p, []byte(hexKey+"\n"), 0o600)
	if k, err := ParseSigningKey("file:" + p); err != nil || string(k) != "0123456789abcdef" {
		t.Errorf("file hex: %q %v", k, err)
	}
	_ = os.WriteFile(p, []byte("raw-key-bytes-16!\n"), 0o600)
	if k, err := ParseSigningKey("file:" + p); err != nil || string(k) != "raw-key-bytes-16!" {
		t.Errorf("file raw: %q %v", k, err)
	}
	for _, bad := range []string{"", "abc", "zz", "file:/nonexistent", "dGlueQ"} { // last: "tiny" base64 -> too short
		if _, err := ParseSigningKey(bad); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
}

func TestHandlerSignedURLs(t *testing.T) {
	srv := httptest.NewTLSServer(Handler(Options{SigningKeys: [][]byte{testKey}, AuthToken: "tok", LargeSize: 64, MaxClientBytes: 100, ClientWindow: time.Hour}))
	defer srv.Close()
	client := srv.Client()
	exp := time.Now().Add(time.Minute)
	get := func(rawURL, token string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = readAll(resp)
		return resp
	}
	signed := func(path, sub string) string {
		s, err := SignURL(testKey, srv.URL+path, exp, sub)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	// Either credential suffices on test endpoints.
	if r := get(signed(SmallPath, ""), ""); r.StatusCode != http.StatusOK {
		t.Errorf("signed small: %s", r.Status)
	}
	if r := get(srv.URL+SmallPath, "tok"); r.StatusCode != http.StatusOK {
		t.Errorf("token small: %s", r.Status)
	}
	if r := get(srv.URL+SmallPath, ""); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("neither: %s", r.Status)
	}
	// The config document is never accepted on a signature alone.
	if r := get(signed(ConfigPath, ""), ""); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("signed config must be refused: %s", r.Status)
	}
	// Budget keyed by subject: two subjects from one IP have separate budgets;
	// the same subject shares one.
	if r := get(signed(LargePath, "a"), ""); r.StatusCode != http.StatusOK {
		t.Fatalf("a #1: %s", r.Status)
	}
	if r := get(signed(LargePath, "a"), ""); r.StatusCode != http.StatusOK {
		t.Fatalf("a #2: %s", r.Status)
	}
	if r := get(signed(LargePath, "a"), ""); r.StatusCode != http.StatusTooManyRequests {
		t.Errorf("a #3 should exhaust a's budget: %s", r.Status)
	}
	if r := get(signed(LargePath, "b"), ""); r.StatusCode != http.StatusOK {
		t.Errorf("b has its own budget: %s", r.Status)
	}
	// An invalid token does not veto a valid signature.
	if r := get(signed(SmallPath, ""), "wrong-token"); r.StatusCode != http.StatusOK {
		t.Errorf("invalid token + valid signature: %s", r.Status)
	}
	// Expired signature is plain 401.
	old, _ := SignURL(testKey, srv.URL+SmallPath, time.Now().Add(-time.Hour), "")
	if r := get(old, ""); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("expired: %s", r.Status)
	}
}

func TestHandlerSignedOnlyServer(t *testing.T) {
	// Signing keys without a token: test endpoints need a signature, the
	// config document is not reachable at all (serve it from the backend).
	srv := httptest.NewTLSServer(Handler(Options{SigningKeys: [][]byte{testKey}}))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = readAll(resp)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("config on a signed-only server: %s", resp.Status)
	}
	s, _ := SignURL(testKey, srv.URL+SmallPath, time.Now().Add(time.Minute), "d")
	resp, err = srv.Client().Get(s)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = readAll(resp)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("signed small: %s", resp.Status)
	}
}

func readAll(resp *http.Response) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	var buf strings.Builder
	b := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(b)
		buf.Write(b[:n])
		if err != nil {
			return []byte(buf.String()), nil
		}
	}
}

func TestHandlerAnonymousServerAcceptsSignedURLs(t *testing.T) {
	srv := httptest.NewTLSServer(Handler(Options{MaxClientBytes: -1}))
	defer srv.Close()
	s, _ := SignURL(testKey, srv.URL+SmallPath, time.Now().Add(time.Minute), "d")
	resp, err := srv.Client().Get(s)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = readAll(resp)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("anonymous server must ignore signatures: %s", resp.Status)
	}
}
