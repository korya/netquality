package netquality

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

func TestIPv6Loopback(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback unavailable:", err)
	}
	srv := httptest.NewUnstartedServer(server.Handler(server.Options{MaxClientBytes: -1}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	res, err := Run(context.Background(), Target{ConfigURL: srv.URL + server.ConfigPath}, Options{
		HTTPClient: insecureClient(), Directions: Download, IdleProbes: 2,
		MaxDuration: 400 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Target.ResolvedIPs, []string{"::1"}) || !reflect.DeepEqual(res.Target.LocalIPs, []string{"::1"}) {
		t.Errorf("resolved=%v local=%v", res.Target.ResolvedIPs, res.Target.LocalIPs)
	}
	if res.Target.Host != "[::1]:"+srv.URL[len("https://[::1]:"):] || res.Download.Bytes == 0 || res.Idle == nil {
		t.Errorf("target=%+v download=%+v", res.Target, res.Download)
	}
}

func TestFullRampToMaxFlows(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	p := fastStability()
	p.StdDevTolerance = 1e-9 // never stable: ramp all the way
	var maxSeen int
	res, err := RunWithEvents(context.Background(), target, Options{
		HTTPClient: client, Directions: Download, IdleProbes: -1, MaxFlows: DefaultMaxFlows,
		MaxDuration: 2 * time.Second, MaxBytes: 1 << 40, Stability: p,
	}, func(e Event) {
		if e.Kind == EventFlow && e.Flows > maxSeen {
			maxSeen = e.Flows
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Download.Flows != DefaultMaxFlows || maxSeen != DefaultMaxFlows {
		t.Errorf("flows=%d maxSeen=%d, want exactly %d", res.Download.Flows, maxSeen, DefaultMaxFlows)
	}
	if res.Download.Intervals < DefaultMaxFlows {
		t.Errorf("only %d intervals; the ramp needs %d to reach the cap", res.Download.Intervals, DefaultMaxFlows)
	}
}

func TestResultJSONRoundTrip(t *testing.T) {
	target, client := newTestServer(t, server.Options{})
	res, err := Run(context.Background(), target, Options{HTTPClient: client, IdleProbes: 2,
		MaxDuration: 300 * time.Millisecond, MaxBytes: 1 << 40, Stability: fastStability()})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var back Result
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	// StartedAt loses monotonic clock reading on the wire; compare by value.
	res.StartedAt = res.StartedAt.Round(0)
	back.StartedAt = back.StartedAt.Round(0)
	if !reflect.DeepEqual(res, &back) {
		again, _ := json.Marshal(&back)
		t.Errorf("round trip changed the result:\n%s\n%s", data, again)
	}
	_ = http.StatusOK
}
