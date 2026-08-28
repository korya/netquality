package main

import "testing"

// FuzzParseBytes: never panics; accepted values are positive.
func FuzzParseBytes(f *testing.F) {
	for _, s := range []string{"250MB", "1GB", "100MiB", "5000", "1.5gb", "", "abc", "-1MB", "0", "9999999999999GB", "1e3MB", "MB"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, err := parseBytes(s)
		if err == nil && n <= 0 {
			t.Fatalf("%q accepted with %d", s, n)
		}
	})
}
