package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseBytes(t *testing.T) {
	tests := map[string]int64{"250MB": 250e6, "1GB": 1e9, "100MiB": 100 << 20, "5000": 5000, "1.5gb": 1.5e9}
	for in, want := range tests {
		got, err := parseBytes(in)
		if err != nil || got != want {
			t.Errorf("%s: got %d, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "abc", "-1MB", "0"} {
		if _, err := parseBytes(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestExitCodes(t *testing.T) {
	var out, errb bytes.Buffer
	if c := run([]string{"version"}, &out, &errb); c != exitOK || !strings.HasPrefix(out.String(), "nq ") {
		t.Errorf("version: %d %q", c, out.String())
	}
	for _, args := range [][]string{{"--bogus"}, {"--target", "nope"}, {"--download-only", "--upload-only"}, {"--max-bytes", "x"}, {"extra"}} {
		if c := run(args, &out, &errb); c != exitUsage {
			t.Errorf("%v: exit %d", args, c)
		}
	}
	if c := run([]string{"--config-url", "https://127.0.0.1:1/nq", "--max-duration", "1s"}, &out, &errb); c != exitFail {
		t.Errorf("unreachable target: exit %d", c)
	}
}
