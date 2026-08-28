package netquality

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseServerConfigFixtures(t *testing.T) {
	tests := []struct {
		file, small, large, upload, endpoint string
	}{
		{"apple.json", "https://mensura.cdn-apple.com/api/v1/gm/small", "https://mensura.cdn-apple.com/api/v1/gm/large", "https://mensura.cdn-apple.com/api/v1/gm/slurp", "cator4-edge-fx-015.aaplimg.com"},
		{"cloudflare.json", "https://h3.speed.cloudflare.com/__down?bytes=10", "https://h3.speed.cloudflare.com/__down?bytes=4000000000", "https://h3.speed.cloudflare.com/__up?bytes=4000000000", "speed.cloudflare.com"},
		{"draft.json", "https://example.com/nq/small", "https://example.com/nq/large", "https://example.com/nq/upload", "nq1.example.com"},
		{"server_spec.json", "https://networkquality.example.com/api/v1/small", "https://networkquality.example.com/api/v1/large", "https://networkquality.example.com/api/v1/upload", ""},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "config", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := ParseServerConfig(data)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if cfg.Version != 1 || cfg.SmallDownloadURL != tc.small || cfg.LargeDownloadURL != tc.large || cfg.UploadURL != tc.upload || cfg.TestEndpoint != tc.endpoint {
				t.Errorf("got %+v", cfg)
			}
		})
	}
}

func TestParseServerConfigInvalid(t *testing.T) {
	tests := map[string]string{
		"version 2":       `{"version":2,"urls":{"small_download_url":"https://a/s","large_download_url":"https://a/l","upload_url":"https://a/u"}}`,
		"missing version": `{"urls":{"small_download_url":"https://a/s","large_download_url":"https://a/l","upload_url":"https://a/u"}}`,
		"missing upload":  `{"version":1,"urls":{"small_download_url":"https://a/s","large_download_url":"https://a/l"}}`,
		"missing urls":    `{"version":1}`,
		"host mismatch":   `{"version":1,"urls":{"small_download_url":"https://a/s","large_download_url":"https://b/l","upload_url":"https://a/u"}}`,
		"bad scheme":      `{"version":1,"urls":{"small_download_url":"ftp://a/s","large_download_url":"https://a/l","upload_url":"https://a/u"}}`,
		"duplicate key":   `{"version":1,"version":1,"urls":{"small_download_url":"https://a/s","large_download_url":"https://a/l","upload_url":"https://a/u"}}`,
		"duplicate url":   `{"version":1,"urls":{"small_download_url":"https://a/s","small_download_url":"https://a/s","large_download_url":"https://a/l","upload_url":"https://a/u"}}`,
		"duplicate endpt": `{"version":1,"test_endpoint":"x","test_endpoint":"y","urls":{"small_download_url":"https://a/s","large_download_url":"https://a/l","upload_url":"https://a/u"}}`,
		"not json":        `hello`,
		"array":           `[]`,
		"float version":   `{"version":1.5,"urls":{"small_download_url":"https://a/s","large_download_url":"https://a/l","upload_url":"https://a/u"}}`,
	}
	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParseServerConfig([]byte(doc))
			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("want ErrInvalidConfig, got %v", err)
			}
		})
	}
}

func TestParseServerConfigIgnoresUnknown(t *testing.T) {
	doc := `{"version":1,"future":true,"urls":{"small_download_url":"https://a:8443/s","large_download_url":"https://a:8443/l","upload_url":"https://a:8443/u","extra":"x"}}`
	cfg, err := ParseServerConfig([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SmallDownloadURL != "https://a:8443/s" {
		t.Errorf("got %+v", cfg)
	}
}

func TestWellKnown(t *testing.T) {
	if got := WellKnown("host:8443").ConfigURL; got != "https://host:8443/.well-known/nq" {
		t.Error(got)
	}
}
