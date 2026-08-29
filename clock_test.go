package netquality

import "time"

// fakeClock drives the interval ticker manually; Now, Mono and After use real
// time so probes and network I/O keep flowing. coarse makes it claim the
// high-resolution timer is unavailable, which is otherwise reachable only on a
// Windows machine without QueryPerformanceCounter.
type fakeClock struct {
	ch     chan time.Time
	coarse bool
}

func newFakeClock() *fakeClock { return &fakeClock{ch: make(chan time.Time, 16)} }

func (f *fakeClock) Now() time.Time                         { return time.Now() }
func (f *fakeClock) Mono() instant                          { return monoNow() }
func (f *fakeClock) HighResolution() bool                   { return !f.coarse }
func (f *fakeClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (f *fakeClock) NewTicker(time.Duration) ticker         { return f }
func (f *fakeClock) C() <-chan time.Time                    { return f.ch }
func (f *fakeClock) Stop()                                  {}
func (f *fakeClock) tick()                                  { f.ch <- time.Now() }
