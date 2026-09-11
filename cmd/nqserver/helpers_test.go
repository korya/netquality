package main

import "testing"

// skipIfShort marks a test that starts a listener or a process: by this repo's definition it is
// not a unit test, and `go test -short` skips it.
func skipIfShort(tb testing.TB) {
	tb.Helper()
	if testing.Short() {
		tb.Skip("starts a listener or process; skipped under -short")
	}
}
