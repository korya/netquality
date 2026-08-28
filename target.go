package netquality

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Target identifies a responsiveness server by its configuration URL.
type Target struct {
	// ConfigURL is the URL of the JSON configuration document, e.g.
	// https://example.com/.well-known/nq or a vendor-specific path.
	ConfigURL string `json:"config_url"`
}

// WellKnown returns a Target for host's /.well-known/nq document (draft Section 8.1).
// host may include a port.
func WellKnown(host string) Target {
	return Target{ConfigURL: "https://" + host + "/.well-known/nq"}
}

// Well-known public targets.
var (
	// Apple is Apple's public responsiveness server (used by macOS `networkQuality`).
	Apple = Target{ConfigURL: "https://mensura.cdn-apple.com/api/v1/gm/config"}
	// Cloudflare is Cloudflare's public responsiveness server. Its `mach` CLI
	// hardcodes the h3.speed.cloudflare.com URLs; this config document returns
	// the same URLs.
	Cloudflare = Target{ConfigURL: "https://aim.cloudflare.com/responsiveness/api/v1/config"}
)

// ServerConfig is the parsed JSON configuration document served by a target.
type ServerConfig struct {
	Version          int    `json:"version"`
	TestEndpoint     string `json:"test_endpoint,omitempty"`
	SmallDownloadURL string `json:"small_download_url"`
	LargeDownloadURL string `json:"large_download_url"`
	UploadURL        string `json:"upload_url"`
}

// ErrInvalidConfig is returned (wrapped) when a configuration document must be
// ignored per draft Section 8.1.
var ErrInvalidConfig = errors.New("netquality: invalid server configuration")

// ParseServerConfig parses a configuration document. It accepts both the draft
// field names (small_download_url, large_download_url, upload_url) and the
// older Apple/Cloudflare *_https_* names, preferring the https-prefixed ones
// when both are present. Unknown fields are ignored. Duplicate keys, a missing
// mandatory field, a version other than 1, or mismatched hosts make the
// document invalid.
func ParseServerConfig(data []byte) (*ServerConfig, error) {
	var raw struct {
		Version      *json.Number    `json:"version"`
		TestEndpoint *string         `json:"test_endpoint"`
		URLs         json.RawMessage `json:"urls"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if err := checkDuplicateKeys(data); err != nil {
		return nil, err
	}
	if raw.Version == nil {
		return nil, fmt.Errorf("%w: missing version", ErrInvalidConfig)
	}
	v, err := raw.Version.Int64()
	if err != nil || v != 1 {
		return nil, fmt.Errorf("%w: unsupported version %q", ErrInvalidConfig, raw.Version.String())
	}
	if len(raw.URLs) == 0 {
		return nil, fmt.Errorf("%w: missing urls", ErrInvalidConfig)
	}
	var urls map[string]string
	if err := json.Unmarshal(raw.URLs, &urls); err != nil {
		return nil, fmt.Errorf("%w: urls: %v", ErrInvalidConfig, err)
	}
	if err := checkDuplicateKeys(raw.URLs); err != nil {
		return nil, err
	}
	pick := func(names ...string) (string, error) {
		for _, n := range names {
			if u, ok := urls[n]; ok && u != "" {
				return u, nil
			}
		}
		return "", fmt.Errorf("%w: missing %s", ErrInvalidConfig, names[len(names)-1])
	}
	cfg := &ServerConfig{Version: int(v)}
	if raw.TestEndpoint != nil {
		cfg.TestEndpoint = *raw.TestEndpoint
	}
	if cfg.SmallDownloadURL, err = pick("small_https_download_url", "small_download_url"); err != nil {
		return nil, err
	}
	if cfg.LargeDownloadURL, err = pick("large_https_download_url", "large_download_url"); err != nil {
		return nil, err
	}
	if cfg.UploadURL, err = pick("https_upload_url", "upload_url"); err != nil {
		return nil, err
	}
	var host string
	for _, u := range []string{cfg.SmallDownloadURL, cfg.LargeDownloadURL, cfg.UploadURL} {
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" {
			return nil, fmt.Errorf("%w: bad url %q", ErrInvalidConfig, u)
		}
		if host == "" {
			host = p.Host
		} else if !strings.EqualFold(host, p.Host) {
			return nil, fmt.Errorf("%w: urls must share a host (%s vs %s)", ErrInvalidConfig, host, p.Host)
		}
	}
	return cfg, nil
}

// checkDuplicateKeys rejects objects with repeated keys at the top level, as the
// draft requires the mandatory fields to appear exactly once.
func checkDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("%w: expected object", ErrInvalidConfig)
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		key, _ := tok.(string)
		if seen[key] {
			return fmt.Errorf("%w: duplicate key %q", ErrInvalidConfig, key)
		}
		seen[key] = true
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
	}
	return nil
}
