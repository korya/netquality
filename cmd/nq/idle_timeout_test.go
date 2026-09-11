package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/korya/netquality"
	"github.com/korya/netquality/server"
)

func idleStallServer(t *testing.T, h2, body bool) string {
	t.Helper()
	skipIfShort(t)
	var loading atomic.Bool
	var small atomic.Int64
	h := server.Handler(server.Options{MaxClientBytes: -1})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == server.LargePath {
			loading.Store(true)
		}
		if r.URL.Path == server.SmallPath && !loading.Load() && small.Add(1) > 1 {
			if body {
				w.Header().Set("Content-Length", "1")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
			}
			<-r.Context().Done()
			return
		}
		h.ServeHTTP(w, r)
	}))
	srv.EnableHTTP2 = h2
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.URL + server.ConfigPath
}

func checkIdleTimeoutOutput(t *testing.T, out []byte) {
	t.Helper()
	var res netquality.Result
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if res.Idle == nil || res.Idle.Samples != 1 || res.Cancelled || res.Download == nil || res.Upload != nil {
		t.Fatalf("idle samples and selected load must survive expiry: %+v", res)
	}
	var warnings int
	for _, w := range res.Warnings {
		if strings.Contains(w, "idle timeout (500ms; 1/1000 probes completed)") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Errorf("timeout warning missing or repeated: %v", res.Warnings)
	}
}

func TestIdleTimeoutOutput(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, body := range []bool{false, true} {
			t.Run(fmt.Sprintf("h2=%v/body=%v", h2, body), func(t *testing.T) {
				var out, errb bytes.Buffer
				args := base(idleStallServer(t, h2, body), "--json", "--download-only", "--max-bytes", "1MB", "--idle-probes", "1000", "--idle-timeout", "500ms")
				if code := run(args, &out, &errb); code != exitOK {
					t.Fatalf("idle expiry must not fail the run: exit=%d stderr=%s", code, errb.String())
				}
				if errb.Len() != 0 {
					t.Errorf("JSON run must keep stderr quiet: %s", errb.String())
				}
				checkIdleTimeoutOutput(t, out.Bytes())
			})
		}
	}
}

func TestIdleTimeoutBinary(t *testing.T) {
	skipIfShort(t)
	bin := filepath.Join(t.TempDir(), "nq.exe")
	buildCtx, stopBuild := context.WithTimeout(context.Background(), time.Minute)
	defer stopBuild()
	if out, err := exec.CommandContext(buildCtx, "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := base(idleStallServer(t, true, true), "--json", "--download-only", "--max-bytes", "1MB", "--idle-probes", "1000", "--idle-timeout", "500ms")
	cmd := exec.CommandContext(ctx, bin, args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("binary did not finish successfully: %v; watchdog=%v stderr=%s", err, ctx.Err(), errb.String())
	}
	checkIdleTimeoutOutput(t, out)
}
