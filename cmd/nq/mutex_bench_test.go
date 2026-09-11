package main

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/korya/netquality"
)

func BenchmarkProgressPrinter(b *testing.B) {
	p := progressPrinter(io.Discard)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p(netquality.Event{Kind: netquality.EventInterval, Direction: "download", ThroughputBPS: 1e9})
		}
	})
}

func BenchmarkEventJSONSink(b *testing.B) {
	var mu sync.Mutex
	enc := json.NewEncoder(&bytes.Buffer{})
	sink := func(e netquality.Event) {
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(e)
	}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			sink(netquality.Event{Kind: netquality.EventInterval, Direction: "download", ThroughputBPS: 1e9})
		}
	})
}
