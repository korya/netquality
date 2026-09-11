package netquality

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/korya/netquality/internal/engine"
)

type benchmarkConn struct{}

func (benchmarkConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (benchmarkConn) Write(p []byte) (int, error)      { return len(p), nil }
func (benchmarkConn) Close() error                     { return nil }
func (benchmarkConn) LocalAddr() net.Addr              { return benchmarkAddr(1) }
func (benchmarkConn) RemoteAddr() net.Addr             { return benchmarkAddr(2) }
func (benchmarkConn) SetDeadline(time.Time) error      { return nil }
func (benchmarkConn) SetReadDeadline(time.Time) error  { return nil }
func (benchmarkConn) SetWriteDeadline(time.Time) error { return nil }

type benchmarkAddr byte

func (a benchmarkAddr) Network() string { return "benchmark" }
func (a benchmarkAddr) String() string  { return string([]byte{byte(a)}) }

func BenchmarkOwnedTransportTrackForget(b *testing.B) {
	o := &ownedTransport{conns: make(map[net.Conn]struct{})}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c := o.track(benchmarkConn{})
			o.forget(c)
		}
	})
}

func BenchmarkProbeTimesState(b *testing.B) {
	pt := new(probeTimes)
	now := monoNow()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			pt.mu.Lock()
			pt.wrote = now
			pt.mu.Unlock()
		}
	})
}

func BenchmarkPhaseStateSamples(b *testing.B) {
	p := &phaseState{cancel: func() {}}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.addSample(false, engine.LatencySample{Total: time.Nanosecond})
			_, _ = p.take()
		}
	})
}

func BenchmarkPhaseStateFlowErrors(b *testing.B) {
	p := &phaseState{cancel: func() {}}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.flowFailed(context.Canceled)
			_, _ = p.flowErrors()
		}
	})
}

func BenchmarkPhaseStateFlowCount(b *testing.B) {
	p := new(phaseState)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = p.flowCount()
		}
	})
}

func BenchmarkRunnerWarnings(b *testing.B) {
	r := &runner{res: &Result{}, opts: Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r.warn("benchmark warning")
		}
	})
}
