package buildinfo

import (
	"strings"
	"testing"
)

func TestBuildInfoString(t *testing.T) {
	defer func(v, c, b string) { Version, Commit, BuildTime = v, c, b }(Version, Commit, BuildTime)

	Version, Commit, BuildTime = "", "", ""
	if s := String(); !strings.HasPrefix(s, "devel") && !strings.HasPrefix(s, "v") {
		t.Errorf("unset: %q", s)
	}
	Version, Commit, BuildTime = "v1.2.3", "0123456789abcdefdeadbeef", "2026-01-01T00:00:00Z"
	if s := String(); s != "v1.2.3 (0123456789ab, built 2026-01-01T00:00:00Z)" {
		t.Errorf("full: %q", s)
	}
	Version, Commit, BuildTime = "v1.2.3", "", ""
	if s := String(); s != "v1.2.3" {
		t.Errorf("version only: %q", s)
	}
	Version, Commit, BuildTime = "v1.2.3", "abc", ""
	if s := String(); s != "v1.2.3 (abc)" {
		t.Errorf("no build time: %q", s)
	}
}
