package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/korya/netquality/server"
)

func TestClientConcurrencyDisabledWarning(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		warn bool
	}{
		{"implicit self-signed", nil, false},
		{"explicit disabled budget only", []string{"--client-bytes", "-1"}, false},
		{"explicit concurrency self-signed", []string{"--client-concurrency", "1"}, true},
		{"explicit concurrency disabled budget", []string{"--client-bytes", "-1", "--client-concurrency", "4"}, true},
		{"normalized concurrency disabled budget", []string{"--client-bytes", "-2", "--client-concurrency", "0"}, true},
		{"explicit concurrency enabled budget", []string{"--client-bytes", "1048576", "--client-concurrency", "1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var out bytes.Buffer
			args := append([]string{"--self-signed", "--listen", "127.0.0.1:0"}, tc.args...)
			code := run(ctx, args, &out, &out, func(net.Addr) { cancel() })
			if code != exitOK || !strings.Contains(out.String(), "listening on") {
				t.Fatalf("startup: %d %s", code, &out)
			}
			warning := "warning: --client-concurrency is ignored because the client byte budget is disabled; set --client-bytes to a positive value to enable both limits"
			if got := strings.Contains(out.String(), warning); got != tc.warn {
				t.Fatalf("warning = %v, want %v: %s", got, tc.warn, &out)
			}
		})
	}
}

func TestClientConcurrencyFlag(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &out, &out, nil); code != exitOK || !strings.Contains(out.String(), "-client-concurrency") || !strings.Contains(out.String(), "default 32") {
		t.Fatalf("help: %d %s", code, &out)
	}
	for _, enabled := range []bool{false, true} {
		name := "self-signed unlimited"
		if enabled {
			name = "enabled budget"
		}
		t.Run(name, func(t *testing.T) {
			args := []string{"--self-signed", "--client-concurrency", "1", "--large-size", "1"}
			if enabled {
				args = append(args, "--client-bytes", "1048576")
			}
			addr := serve(t, args...)
			tr := http.DefaultTransport.(*http.Transport).Clone()
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // loopback self-signed fixture
			tr.ForceAttemptHTTP2 = true
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			pr, pw := io.Pipe()
			defer func() { _ = pr.Close() }()
			defer func() { _ = pw.Close() }()
			r, err := http.NewRequestWithContext(ctx, "POST", "https://"+addr.String()+server.UploadPath, pr)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				resp, err := client.Do(r)
				if err == nil {
					_, err = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
				done <- err
			}()
			if _, err := pw.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			// The producer has reached the transport; observe admission via 429,
			// rather than assuming that its first byte has reached the server yet.
			for {
				req, err := http.NewRequestWithContext(ctx, "GET", "https://"+addr.String()+server.LargePath, nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if !enabled {
					if resp.StatusCode != 200 {
						t.Fatalf("disabled: %d", resp.StatusCode)
					}
					break
				}
				if resp.StatusCode == 429 {
					if !strings.Contains(string(body), "concurrency") || resp.Header.Get("Retry-After") != "1" {
						t.Fatalf("refusal: %v %s", resp.Header, body)
					}
					break
				}
				if resp.StatusCode != 200 {
					t.Fatalf("unexpected status: %d", resp.StatusCode)
				}
				time.Sleep(10 * time.Millisecond)
			}
			_ = pw.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			resp, err := client.Get("https://" + addr.String() + server.LargePath)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatal("completed upload did not release the CLI admission slot")
			}
		})
	}
}
