package server

import (
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkClientBudgetAdmission measures the short critical section used by
// admission and settlement. It is the baseline for sharding or lock-free
// budget experiments; transfer I/O is intentionally absent.
func BenchmarkClientBudgetAdmission(b *testing.B) {
	budget := newClientBudget(1<<30, time.Hour, 32)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ticket, _, _ := budget.admit("benchmark-client")
			ticket.settle(1)
		}
	})
}

func BenchmarkClientBudgetDistinctIdentities(b *testing.B) {
	budget := newClientBudget(1<<30, time.Hour, 32)
	var i atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			key := net.JoinHostPort("198.51.100.1", strconv.FormatInt(i.Add(1), 10))
			ticket, _, _ := budget.admit(key)
			ticket.settle(1)
		}
	})
}
