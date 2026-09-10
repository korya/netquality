package server

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func mustAdmit(t *testing.T, b *clientBudget, key string) admission {
	t.Helper()
	a, reason, _ := b.admit(key)
	if reason != "" {
		t.Fatalf("admit %s: %s", key, reason)
	}
	return a
}

func TestClientBudget(t *testing.T) {
	now := time.Unix(0, 0)
	b := newClientBudget(1000, time.Minute, 2)
	b.now = func() time.Time { return now }
	mustAdmit(t, b, "a").settle(1500)
	_, reason, wait := b.admit("a")
	if reason != budgetExhausted || wait != 30*time.Second {
		t.Fatalf("exhausted: %s %s", reason, wait)
	}
	mustAdmit(t, b, "b").settle(0)
	now = now.Add(31 * time.Second)
	mustAdmit(t, b, "a").settle(0)
	now = now.Add(time.Hour)
	a := mustAdmit(t, b, "a")
	if a.bucket.tokens != 1000 {
		t.Fatalf("refill = %g", a.bucket.tokens)
	}
	a.settle(10)
	if a.bucket.tokens != 990 || a.bucket.active != 0 {
		t.Fatalf("settlement = %+v", a.bucket)
	}
}

func TestClientBudgetConcurrentAdmission(t *testing.T) {
	const limit, attempts = 4, 40
	now := time.Unix(0, 0)
	b := newClientBudget(1, time.Hour, limit)
	b.now = func() time.Time { return now }
	ready := make(chan admission, attempts)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, reason, _ := b.admit("same")
			if reason != "" && reason != concurrencyExhausted {
				t.Errorf("refusal: %s", reason)
			}
			ready <- a
			if a.bucket != nil {
				<-release
				a.settle(1024)
			}
		}()
	}
	admitted := 0
	for i := 0; i < attempts; i++ {
		if (<-ready).bucket != nil {
			admitted++
		}
	}
	close(release)
	wg.Wait()
	if admitted != limit {
		t.Fatalf("admitted %d, want %d", admitted, limit)
	}
	bk := b.seen["same"]
	if bk.active != 0 || bk.tokens != 1-limit*1024 {
		t.Fatalf("settled state: %+v", bk)
	}
	_, reason, _ := b.admit("same")
	if reason != budgetExhausted {
		t.Fatalf("free slots must not erase debt: %s", reason)
	}
}

func TestClientBudgetCleanupAndRefill(t *testing.T) {
	now := time.Unix(0, 0)
	b := newClientBudget(100, time.Hour, 1)
	b.now = func() time.Time { return now }
	active := mustAdmit(t, b, "active")
	mustAdmit(t, b, "debt").settle(500)
	mustAdmit(t, b, "partial").settle(50)
	mustAdmit(t, b, "full").settle(0)
	now = now.Add(2 * time.Hour)
	// Refill must not reset outstanding slots.
	if _, reason, _ := b.admit("active"); reason != concurrencyExhausted {
		t.Fatalf("active after refill: %s", reason)
	}
	// Fill the table without triggering cleanup until the admission below.
	for i := 0; i < 10000; i++ {
		b.seen[fmt.Sprint(i)] = &bucket{tokens: 100, last: now}
	}
	mustAdmit(t, b, "trigger").settle(0)
	if b.seen["active"] != active.bucket || b.seen["debt"] == nil {
		t.Fatal("cleanup forgot active work or unpaid debt")
	}
	if b.seen["partial"] != nil || b.seen["full"] != nil {
		t.Fatal("fully refilled inactive buckets not evicted")
	}
	active.settle(25)
	if b.seen["active"].active != 0 || b.seen["active"].tokens != 75 {
		t.Fatal("late settlement lost ownership")
	}
	if _, reason, _ := b.admit("debt"); reason != budgetExhausted {
		t.Fatal("cleanup erased debt")
	}
	now = now.Add(4 * time.Hour)
	mustAdmit(t, b, "debt").settle(0)
	if b.seen["debt"].tokens != 100 {
		t.Fatal("debt did not fully recover")
	}
}

func TestClientBudgetRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name           string
		budget, charge int64
		window, want   time.Duration
	}{
		{"zero balance", 100, 100, time.Second, 0},
		{"fractional", 100, 101, time.Second, 10 * time.Millisecond},
		{"one second", 100, 200, time.Second, time.Second},
		{"overflow", 1, 16 << 30, time.Hour, maxRetryWait},
		{"largest charge", 1, 1<<63 - 1, maxRetryWait, maxRetryWait},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newClientBudget(tc.budget, tc.window, 1)
			b.now = func() time.Time { return time.Unix(0, 0) }
			mustAdmit(t, b, "a").settle(tc.charge)
			_, reason, wait := b.admit("a")
			if reason != budgetExhausted || wait != tc.want {
				t.Fatalf("got %s %s, want %s", reason, wait, tc.want)
			}
		})
	}
	// Byte debt wins over a simultaneously full active limit.
	b := newClientBudget(100, time.Hour, 2)
	b.now = func() time.Time { return time.Unix(0, 0) }
	a := mustAdmit(t, b, "a")
	other := mustAdmit(t, b, "a")
	a.settle(200)
	b.concurrency = 1
	if _, reason, _ := b.admit("a"); reason != budgetExhausted {
		t.Fatal("concurrency hid debt")
	}
	other.settle(0)
}
