package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/korya/netquality"
	"github.com/korya/netquality/server"
)

func startServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(server.Handler(server.Options{MaxClientBytes: -1}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.URL + server.ConfigPath
}

func base(url string, extra ...string) []string {
	return append([]string{"--config-url", url, "--insecure", "--max-duration", "300ms", "--interval", "100ms", "--idle-probes", "2"}, extra...)
}

func TestJSONOutput(t *testing.T) {
	var out, errb bytes.Buffer
	if c := run(base(startServer(t), "--json"), &out, &errb); c != exitOK {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	var res netquality.Result
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("stdout is not a Result: %v\n%s", err, out.String())
	}
	if res.Download == nil || res.Upload == nil || res.Idle == nil || res.Download.Bytes == 0 {
		t.Errorf("%+v", res)
	}
	if errb.Len() != 0 {
		t.Errorf("--json must keep stderr quiet, got %q", errb.String())
	}
}

func TestHumanOutput(t *testing.T) {
	var out, errb bytes.Buffer
	if c := run(base(startServer(t), "--download-only"), &out, &errb); c != exitOK {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	s := out.String()
	for _, want := range []string{"Target     127.0.0.1", "(HTTP/2.0)", "Idle       ", "Download   ", "Mbps", "RPM", "Upload     not run", "Cost       ", "moved in"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	// Two idle probes support no percentile: the tail shows the max, never a fake p95.
	if idleLine := lineWith(s, "Idle"); !strings.Contains(idleLine, " max,") || strings.Contains(idleLine, "p95") {
		t.Errorf("idle tail at 2 probes: %q", idleLine)
	}
	if !strings.Contains(errb.String(), "== download") || !strings.Contains(errb.String(), "flows=") {
		t.Errorf("progress on stderr missing: %q", errb.String())
	}
	out.Reset()
	if c := run(base(startServer(t), "--upload-only"), &out, &errb); c != exitOK || !strings.Contains(out.String(), "Download   not run") {
		t.Errorf("upload-only: %d %s", c, out.String())
	}
}

func TestEventsOutput(t *testing.T) {
	var out, errb bytes.Buffer
	if c := run(base(startServer(t), "--events", "--json", "--download-only"), &out, &errb); c != exitOK {
		t.Fatalf("exit %d", c)
	}
	kinds := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(errb.String()), "\n") {
		var e netquality.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		kinds[string(e.Kind)]++
	}
	if kinds["phase"] < 4 || kinds["interval"] == 0 || kinds["probe"] == 0 || kinds["flow"] == 0 {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestCancelledExitCode(t *testing.T) {
	// Interrupt handling is wired to the context; simulate via a target that is
	// slow enough for the run to be cut off by the config timeout path? No:
	// exercise the library contract directly so the CLI mapping is covered.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := netquality.Run(ctx, netquality.Target{ConfigURL: startServer(t)}, netquality.Options{})
	if res != nil || err == nil {
		t.Errorf("pre-cancelled ctx: res=%v err=%v", res, err)
	}
	var out, errb bytes.Buffer
	if c := run([]string{"--config-url", "https://127.0.0.1:1/nq", "--max-duration", "1s"}, &out, &errb); c != exitFail || !strings.Contains(errb.String(), "nq:") {
		t.Errorf("unreachable: %d %q", c, errb.String())
	}
}

func TestTruncatedStillExitsZero(t *testing.T) {
	var out, errb bytes.Buffer
	if c := run(base(startServer(t), "--download-only", "--max-bytes", "1MB"), &out, &errb); c != exitOK {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	if !strings.Contains(out.String(), "TRUNCATED: bytes_cap") || !strings.Contains(out.String(), "Warning    download: byte cap") {
		t.Errorf("%s", out.String())
	}
}

func TestVerboseLogging(t *testing.T) {
	var out, errb bytes.Buffer
	if c := run(base(startServer(t), "--download-only", "-v"), &out, &errb); c != exitOK || !strings.Contains(errb.String(), "level=") {
		t.Errorf("verbose: %d %q", c, errb.String())
	}
	errb.Reset()
	if c := run(base(startServer(t), "--download-only"), &out, &errb); c != exitOK || strings.Contains(errb.String(), "level=") {
		t.Errorf("non-verbose must not log: %d %q", c, errb.String())
	}
	_ = http.StatusOK
}

func TestHelpers(t *testing.T) {
	if got := ms(1500 * time.Microsecond); got != "1.5ms" {
		t.Error(got)
	}
	for in, want := range map[int64]string{500: "500 B", 1500: "1.5 kB", 250e6: "250.0 MB", 3.2e9: "3.2 GB"} {
		if got := humanBytes(in); got != want {
			t.Errorf("%d: %s", in, got)
		}
	}
	var out bytes.Buffer
	printHuman(&out, &netquality.Result{Cancelled: true, Warnings: []string{"w1"},
		Target: netquality.ResolvedTarget{Host: "h", Proxy: &netquality.ProxyInfo{Explicit: true, URL: "http://p", TLSInterception: true, Issuer: "I"}}})
	for _, want := range []string{"Proxy      explicit http://p; TLS interception by I", "Idle       not measured", "Download   not run", "Cancelled  partial results", "Warning    w1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	printHuman(&out, &netquality.Result{Download: &netquality.DirectionResult{Truncated: true, Reason: netquality.ReasonDurationCap,
		Loaded: netquality.LoadedLatency{Combined: &netquality.LatencyStats{Median: time.Millisecond}}}})
	if !strings.Contains(out.String(), "TRUNCATED: duration_cap") || !strings.Contains(out.String(), "loaded 1.0ms median") {
		t.Error(out.String())
	}
}

func TestAuthTokenFlagAndEnv(t *testing.T) {
	srv := httptest.NewUnstartedServer(server.Handler(server.Options{AuthToken: "s3cret", MaxClientBytes: -1}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	url := srv.URL + server.ConfigPath
	var out, errb bytes.Buffer
	if c := run(base(url, "--download-only", "--json"), &out, &errb); c != exitFail || !strings.Contains(errb.String(), "401") {
		t.Errorf("no token: exit %d %q", c, errb.String())
	}
	errb.Reset()
	if c := run(base(url, "--download-only", "--json", "--auth-token", "wrong"), &out, &errb); c != exitFail || !strings.Contains(errb.String(), "401") {
		t.Errorf("wrong token: exit %d %q", c, errb.String())
	}
	out.Reset()
	if c := run(base(url, "--download-only", "--json", "--auth-token", "s3cret"), &out, &errb); c != exitOK {
		t.Fatalf("flag token: exit %d %s", c, errb.String())
	}
	var res netquality.Result
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Download == nil || res.Download.Bytes == 0 {
		t.Errorf("flag token run: %v %+v", err, res.Download)
	}
	t.Setenv("NQ_AUTH_TOKEN", "s3cret")
	out.Reset()
	if c := run(base(url, "--download-only", "--json"), &out, &errb); c != exitOK {
		t.Errorf("env token: exit %d %s", c, errb.String())
	}
}

func TestEdgeFlags(t *testing.T) {
	url := startServer(t)
	var out, errb bytes.Buffer
	// Zero duration means the library default (12s), too long for a test; a
	// tiny duration plus an interval longer than it must still terminate and
	// report duration_cap with zero intervals.
	if c := run([]string{"--config-url", url, "--insecure", "--download-only", "--json", "--max-duration", "150ms", "--interval", "1s", "--idle-probes", "-1"}, &out, &errb); c != exitOK {
		t.Fatalf("short duration: exit %d %s", c, errb.String())
	}
	var res netquality.Result
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Idle != nil || res.Download == nil || res.Download.Intervals != 0 || res.Download.Reason != netquality.ReasonDurationCap || res.Download.ThroughputBPS == 0 {
		t.Errorf("edge run: idle=%v download=%+v", res.Idle, res.Download)
	}
	// Invalid sizes and durations are usage errors, not runs.
	for _, args := range [][]string{
		{"--config-url", url, "--max-bytes", "-5MB"},
		{"--config-url", url, "--max-duration", "soon"},
		{"--config-url", url, "--max-flows", "many"},
	} {
		if c := run(args, &out, &errb); c != exitUsage {
			t.Errorf("%v: exit %d", args, c)
		}
	}
	// --max-flows 0 and --idle-probes 0 fall back to defaults rather than
	// running with nothing.
	out.Reset()
	if c := run(base(url, "--download-only", "--json", "--max-flows", "0", "--idle-probes", "0"), &out, &errb); c != exitOK {
		t.Fatalf("zero flags: exit %d %s", c, errb.String())
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Idle == nil || res.Idle.Samples != netquality.DefaultIdleProbes || res.Download.Flows == 0 {
		t.Errorf("zero flags must mean defaults: %v idle=%+v flows=%d", err, res.Idle, res.Download.Flows)
	}
}

func lineWith(s, prefix string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}
