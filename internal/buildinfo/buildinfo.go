// Package buildinfo carries version metadata injected at build time via
// -ldflags "-X github.com/korya/netquality/internal/buildinfo.Version=...".
package buildinfo

import "runtime/debug"

// Set with -ldflags -X; defaults are used for `go install` / `go run` builds.
var (
	Version   = ""
	Commit    = ""
	BuildTime = ""
)

// String renders "version (commit, built time)" with whatever is known.
func String() string {
	v := Version
	c := Commit
	if v == "" {
		v = "devel"
		if bi, ok := debug.ReadBuildInfo(); ok {
			if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
				v = bi.Main.Version
			}
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" && c == "" {
					c = s.Value
				}
			}
		}
	}
	if len(c) > 12 {
		c = c[:12]
	}
	out := v
	if c != "" {
		out += " (" + c
		if BuildTime != "" {
			out += ", built " + BuildTime
		}
		out += ")"
	}
	return out
}
