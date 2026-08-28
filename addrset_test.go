package netquality

import (
	"reflect"
	"sync"
	"testing"
)

func TestAddrSet(t *testing.T) {
	var s addrSet
	if s.list() != nil {
		t.Error("empty set must list nil")
	}
	for _, a := range []string{"10.0.0.1", "", "fe80::1%en0", "10.0.0.1", "fe80::1%en1"} {
		s.add(a)
	}
	if got := s.list(); !reflect.DeepEqual(got, []string{"10.0.0.1", "fe80::1%en0", "fe80::1%en1"}) {
		t.Errorf("got %v", got)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.add("192.168.1.1")
		}()
	}
	wg.Wait()
	if got := s.list(); len(got) != 4 {
		t.Errorf("concurrent adds must dedupe: %v", got)
	}
}
