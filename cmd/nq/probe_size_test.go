package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/korya/netquality"
	"github.com/korya/netquality/server"
)

func TestInvalidProbeOutput(t *testing.T) {
	for _, mode := range []string{"json", "human", "events"} {
		t.Run(mode, func(t *testing.T) {
			var small atomic.Int64
			h := server.Handler(server.Options{MaxClientBytes: -1})
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == server.SmallPath && small.Add(1) > 1 {
					_, _ = io.WriteString(w, "01234567890")
					return
				}
				h.ServeHTTP(w, r)
			}))
			srv.EnableHTTP2 = true
			srv.StartTLS()
			defer srv.Close()
			args := base(srv.URL+server.ConfigPath, "--download-only", "--idle-probes", "3", "--max-bytes", "1MB")
			if mode != "human" {
				args = append(args, "--json")
			}
			if mode == "events" {
				args = append(args, "--events")
			}
			var out, errb bytes.Buffer
			if code := run(args, &out, &errb); code != exitOK {
				t.Fatalf("invalid probe must not fail load: exit=%d stderr=%s", code, errb.String())
			}
			const diagnostic = "invalid probe response size"
			if mode == "human" {
				if !strings.Contains(out.String(), "Warning    idle latency: "+diagnostic) || !strings.Contains(lineWith(out.String(), "Idle"), "(1 probes)") {
					t.Errorf("partial idle/warning missing:\n%s", out.String())
				}
				return
			}
			var res netquality.Result
			if err := json.Unmarshal(out.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
			if res.Idle == nil || res.Idle.Samples != 1 || res.Download == nil || res.Download.Bytes == 0 || res.Cancelled {
				t.Fatalf("partial idle and download missing: %+v", res)
			}
			var warnings int
			for _, w := range res.Warnings {
				if strings.HasPrefix(w, "idle latency: "+diagnostic) {
					warnings++
				}
			}
			if warnings != 1 {
				t.Errorf("idle warning missing/repeated: %v", res.Warnings)
			}
			if mode == "json" {
				if errb.Len() != 0 {
					t.Errorf("JSON stderr must be quiet: %s", errb.String())
				}
				return
			}
			var samples, warningEvents int
			for _, line := range strings.Split(strings.TrimSpace(errb.String()), "\n") {
				var e netquality.Event
				if err := json.Unmarshal([]byte(line), &e); err != nil {
					t.Fatalf("bad event %q: %v", line, err)
				}
				if e.Kind == netquality.EventProbe {
					samples++
				}
				if e.Kind == netquality.EventWarning && strings.HasPrefix(e.Message, "idle latency: "+diagnostic) {
					warningEvents++
				}
			}
			if samples != 1 || warningEvents != 1 {
				t.Errorf("invalid successes or repeated warnings: samples=%d warnings=%d", samples, warningEvents)
			}
		})
	}
}
