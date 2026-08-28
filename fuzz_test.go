package netquality

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzParseServerConfig: never panics, and whatever it accepts satisfies the
// same-host invariant (DISC-4).
func FuzzParseServerConfig(f *testing.F) {
	for _, name := range []string{"apple.json", "cloudflare.json", "draft.json", "server_spec.json"} {
		b, err := os.ReadFile(filepath.Join("testdata", "config", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Add([]byte(`{"version":"1","urls":{"small_download_url":"https://a/s","large_download_url":"https://a/l","upload_url":"https://a/u"}}`))
	f.Add([]byte(`{"version":1,"version":1,"urls":{}}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := ParseServerConfig(data)
		if err != nil {
			if cfg != nil {
				t.Fatal("error with non-nil config")
			}
			return
		}
		if cfg.Version != 1 || cfg.SmallDownloadURL == "" || cfg.LargeDownloadURL == "" || cfg.UploadURL == "" {
			t.Fatalf("accepted an invalid config: %+v", cfg)
		}
	})
}
