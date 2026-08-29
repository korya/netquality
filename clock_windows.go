//go:build windows

package netquality

import (
	"math/bits"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Windows needs a different clock for probe timings than the rest of the world.
//
// time.Now's monotonic reading comes from _INTERRUPT_TIME in KUSER_SHARED_DATA
// (runtime/time_windows_amd64.s), which advances only at the system timer tick
// — between 0.5 ms and 15.625 ms depending on what else is running on the
// machine, and not something this process controls. A self probe is a GET of
// one byte on an established HTTP/2 connection and takes tens of microseconds,
// so timing it with that clock quantises the result to 0 or to a whole tick.
// Means survive quantisation; jitter and the percentiles do not (LAT-10).
//
// QueryPerformanceCounter is Microsoft's interval clock and is unaffected. The
// standard library reaches for it in exactly this situation: see
// src/testing/testing_windows.go, whose highPrecisionTime carries the comment
// "time.Time on Windows has low system granularity, which is not suitable for
// measuring short time intervals". This mirrors that implementation, including
// the 128-bit conversion, so the two stay easy to compare.
var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procQueryPerfCounter   = kernel32.NewProc("QueryPerformanceCounter")
	procQueryPerfFrequency = kernel32.NewProc("QueryPerformanceFrequency")
)

// qpc is resolved exactly once. Availability is decided at initialisation and
// never re-tested, so every instant in a process is drawn from the same clock
// and a single sample can never take its start from one and its end from
// another — which would not merely be coarse, it would be wrong.
var qpc struct {
	once      sync.Once
	frequency int64
	available bool
}

func qpcInit() {
	if procQueryPerfCounter.Find() != nil || procQueryPerfFrequency.Find() != nil {
		return
	}
	var hz int64
	if r, _, _ := procQueryPerfFrequency.Call(uintptr(unsafe.Pointer(&hz))); r == 0 || hz <= 0 {
		return
	}
	// One trial read: this is the only place the counter's result is checked.
	var c int64
	if r, _, _ := procQueryPerfCounter.Call(uintptr(unsafe.Pointer(&c))); r == 0 {
		return
	}
	qpc.frequency, qpc.available = hz, true
}

// instant is one reading of the clock probe timings are measured with. It is
// meaningful only as the left operand of sub; it is not a wall time and never
// reaches a Result.
type instant struct {
	qpc  int64     // performance counts, when qpc.available
	wall time.Time // otherwise
}

func monoNow() instant {
	qpc.once.Do(qpcInit)
	if !qpc.available {
		return instant{wall: time.Now()}
	}
	// The result is not checked, matching the standard library: Microsoft
	// documents QueryPerformanceCounter as not failing on Windows XP and above
	// given a valid pointer, and qpcInit has already made one successful call.
	var c int64
	_, _, _ = procQueryPerfCounter.Call(uintptr(unsafe.Pointer(&c)))
	return instant{qpc: c}
}

// sub returns a - b. a must not be earlier than b.
func (a instant) sub(b instant) time.Duration {
	if !qpc.available {
		return a.wall.Sub(b.wall)
	}
	// counts * 1e9 / frequency, via a 128-bit intermediate so the
	// multiplication cannot overflow. Subtract first: converting absolute
	// counter values would throw away precision for no reason.
	delta := a.qpc - b.qpc
	hi, lo := bits.Mul64(uint64(delta), uint64(time.Second)/uint64(time.Nanosecond))
	quo, _ := bits.Div64(hi, lo, uint64(qpc.frequency))
	return time.Duration(quo)
}

func (a instant) isZero() bool { return a.qpc == 0 && a.wall.IsZero() }

// monoHighResolution reports whether monoNow resolves finely enough to time
// the probes we send (LAT-10). False means QueryPerformanceCounter could not
// be reached and timings fall back to the tick-quantised system clock.
func monoHighResolution() bool {
	qpc.once.Do(qpcInit)
	return qpc.available
}
