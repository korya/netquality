//go:build !windows

package netquality

import "time"

// instant is one reading of the monotonic clock probe timings are measured
// with. It is meaningful only as the left operand of sub; it is not a wall
// time and never reaches a Result.
//
// Everywhere except Windows, time.Now carries a nanosecond-resolution
// monotonic reading and Sub uses it in preference to the wall clock, so there
// is nothing to work around. See clock_windows.go for the platform that needs
// more (LAT-10).
type instant struct{ t time.Time }

func monoNow() instant { return instant{t: time.Now()} }

// sub returns a - b. a must not be earlier than b.
func (a instant) sub(b instant) time.Duration { return a.t.Sub(b.t) }

func (a instant) isZero() bool { return a.t.IsZero() }

// monoHighResolution reports whether monoNow resolves finely enough to time
// the probes we send (LAT-10).
func monoHighResolution() bool { return true }
