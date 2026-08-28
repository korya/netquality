package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// FuzzAuthorize: never panics; only the exact token authorises.
func FuzzAuthorize(f *testing.F) {
	for _, h := range []string{"", "Bearer s3cret", "bearer s3cret", "Bearer  s3cret ", "Basic s3cret", "Bearer", "Bearer s3cre", strings.Repeat("B", 5000)} {
		f.Add(h, "s3cret")
	}
	f.Fuzz(func(t *testing.T, header, token string) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", header)
		ok := authorize(r, token)
		if token == "" {
			if !ok {
				t.Fatal("anonymous server must accept")
			}
			return
		}
		scheme, cred, found := strings.Cut(header, " ")
		expect := found && strings.EqualFold(scheme, "Bearer") && strings.TrimSpace(cred) == token && len(header) <= maxTokenLength+16
		if ok != expect {
			t.Fatalf("header=%q token=%q: got %v want %v", header, token, ok, expect)
		}
	})
}

// FuzzVerifySignature: never panics; never verifies a query that was not
// produced by SignURL with the key (a forged sig for random inputs).
func FuzzVerifySignature(f *testing.F) {
	key := []byte("0123456789abcdef0123456789abcdef")
	good, _ := SignURL(key, "https://x/nq/large?bytes=1", time.Unix(1800000000, 0), "dev")
	f.Add("/nq/large", strings.TrimPrefix(good, "https://x/nq/large?"), "dev")
	f.Add("/nq/large", "exp=1800000000&sig=AAAA", "")
	f.Add("/nq/large", "", "")
	f.Add("/nq/%6Carge", "exp=1&sub=&sig=%%%", "")
	f.Fuzz(func(t *testing.T, path, query, sub string) {
		// Build the request directly: fuzzed paths need not be valid URLs.
		r := &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "x", Path: path, RawQuery: query}, Header: http.Header{}}
		gotSub, ok := verifySignature(r, [][]byte{key}, time.Unix(1799999000, 0))
		if !ok {
			return
		}
		// If it verified, re-signing the same fields must reproduce the sig.
		q := r.URL.Query()
		expect, _ := SignURL(key, "https://x"+r.URL.Path, mustUnix(q.Get("exp")), gotSub)
		if !strings.Contains(expect, "sig="+q.Get("sig")) && !equivalentSig(q.Get("sig"), expect) {
			t.Fatalf("verified a signature SignURL would not produce: path=%q query=%q", path, query)
		}
	})
}

func mustUnix(s string) time.Time {
	var n int64
	for _, c := range s {
		n = n*10 + int64(c-'0')
	}
	return time.Unix(n, 0)
}

// equivalentSig accepts the same signature in another base64 flavour.
func equivalentSig(got, signedURL string) bool {
	b, ok := decodeSig(got)
	if !ok {
		return false
	}
	i := strings.Index(signedURL, "sig=")
	if i < 0 {
		return false
	}
	want := signedURL[i+4:]
	if j := strings.IndexByte(want, '&'); j >= 0 {
		want = want[:j]
	}
	wb, ok := decodeSig(want)
	return ok && string(wb) == string(b)
}
