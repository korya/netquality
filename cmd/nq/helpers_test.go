package main

import (
	"strings"
	"testing"
)

// skipIfShort marks a test that starts a listener or a process: by this repo's definition it is
// not a unit test, and `go test -short` skips it.
func skipIfShort(tb testing.TB) {
	tb.Helper()
	if testing.Short() {
		tb.Skip("starts a listener or process; skipped under -short")
	}
}

func base(url string, extra ...string) []string {
	return append([]string{"--config-url", url, "--insecure", "--max-duration", "300ms", "--interval", "100ms", "--idle-probes", "2"}, extra...)
}

func lineWith(s, prefix string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}
