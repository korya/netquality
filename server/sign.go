package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Signed URLs let a backend authorise a client without giving it a reusable
// secret: the backend mints the config document with test URLs carrying
// exp (unix seconds), an optional sub (subject, e.g. a device id), and
// sig = base64url(HMAC-SHA256(key, path + "\n" + exp + "\n" + sub)).
//
// Only path, exp and sub are signed, so parameter order and other query
// parameters (a Cloudflare-style ?bytes=) do not matter — and are not
// protected: a server must never derive authorisation or limits from them.
// The config document's own URL is never signed (the well-known URI admits
// no query string); serve it from the backend or protect it with a token.
const (
	// SignatureLeeway tolerates clock skew between issuer and server.
	SignatureLeeway = 30 * time.Second
	// MaxSignatureTTL is the longest validity a server accepts, bounding the
	// blast radius of a leaked URL.
	MaxSignatureTTL = 24 * time.Hour
	maxSubjectLen   = 256
)

// SignURL returns rawURL with exp, sub and sig query parameters added.
func SignURL(key []byte, rawURL string, exp time.Time, sub string) (string, error) {
	if len(key) == 0 {
		return "", errors.New("empty signing key")
	}
	if len(sub) > maxSubjectLen {
		return "", fmt.Errorf("subject longer than %d bytes", maxSubjectLen)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Path == "" {
		return "", errors.New("url has no path")
	}
	e := exp.Unix()
	q := u.Query()
	q.Set("exp", strconv.FormatInt(e, 10))
	if sub != "" {
		q.Set("sub", sub)
	} else {
		q.Del("sub")
	}
	q.Set("sig", base64.RawURLEncoding.EncodeToString(signature(key, u.Path, e, sub)))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func signature(key []byte, path string, exp int64, sub string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(path + "\n" + strconv.FormatInt(exp, 10) + "\n" + sub))
	return mac.Sum(nil)
}

// verifySignature checks r's exp/sub/sig against keys (any key may match,
// which allows rotation). It returns the verified subject.
func verifySignature(r *http.Request, keys [][]byte, now time.Time) (string, bool) {
	if len(keys) == 0 {
		return "", false
	}
	q := r.URL.Query()
	expStr, sigStr := q.Get("exp"), q.Get("sig")
	if expStr == "" || sigStr == "" {
		return "", false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return "", false
	}
	expAt := time.Unix(exp, 0)
	if now.After(expAt.Add(SignatureLeeway)) || expAt.After(now.Add(MaxSignatureTTL+SignatureLeeway)) {
		return "", false
	}
	sub := q.Get("sub")
	if len(sub) > maxSubjectLen {
		return "", false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigStr)
	if err != nil || len(sig) != sha256.Size {
		return "", false
	}
	for _, key := range keys {
		if hmac.Equal(sig, signature(key, r.URL.Path, exp, sub)) {
			return sub, true
		}
	}
	return "", false
}

// ParseSigningKey decodes a key given as hex, base64url, or "file:<path>"
// (raw or hex contents, trailing whitespace ignored). Keys shorter than 16
// bytes are rejected.
func ParseSigningKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "file:"); ok {
		b, err := os.ReadFile(rest)
		if err != nil {
			return nil, err
		}
		s = strings.TrimSpace(string(b))
		if k, err := hex.DecodeString(s); err == nil {
			return checkKey(k)
		}
		return checkKey([]byte(s))
	}
	if k, err := hex.DecodeString(s); err == nil {
		return checkKey(k)
	}
	if k, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return checkKey(k)
	}
	return nil, errors.New("signing key must be hex, base64url, or file:<path>")
}

func checkKey(k []byte) ([]byte, error) {
	if len(k) < 16 {
		return nil, fmt.Errorf("signing key is %d bytes; need at least 16", len(k))
	}
	return k, nil
}
