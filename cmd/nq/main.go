// Command nq measures throughput, latency and responsiveness (RPM) against a
// responsiveness test server.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/korya/netquality"
	"github.com/korya/netquality/internal/buildinfo"
)

const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "version" {
		fmt.Fprintln(stdout, "nq", buildinfo.String())
		return exitOK
	}
	fs := flag.NewFlagSet("nq", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		target       = fs.String("target", "cloudflare", "named target: cloudflare | apple")
		configURL    = fs.String("config-url", "", "explicit config document URL")
		wellKnown    = fs.String("well-known", "", "host[:port] serving /.well-known/nq")
		downloadOnly = fs.Bool("download-only", false, "run only the download phase")
		uploadOnly   = fs.Bool("upload-only", false, "run only the upload phase")
		maxDuration  = fs.Duration("max-duration", netquality.DefaultMaxDuration, "per-direction time cap")
		maxBytes     = fs.String("max-bytes", "250MB", "per-direction byte cap (e.g. 100MB, 1GB)")
		maxFlows     = fs.Int("max-flows", netquality.DefaultMaxFlows, "maximum concurrent load connections")
		idleProbes   = fs.Int("idle-probes", netquality.DefaultIdleProbes, "number of idle latency probes")
		interval     = fs.Duration("interval", 0, "stability interval (default 1s; draft says 5s)")
		insecure     = fs.Bool("insecure", false, "skip TLS certificate verification (self-hosted dev servers)")
		authToken    = fs.String("auth-token", os.Getenv("NQ_AUTH_TOKEN"), "bearer token for a protected server (env NQ_AUTH_TOKEN)")
		jsonOut      = fs.Bool("json", false, "print the Result as JSON")
		events       = fs.Bool("events", false, "stream progress events as JSON lines to stderr")
		verbose      = fs.Bool("v", false, "verbose logging to stderr")
	)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: nq [flags]\n       nq version\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "nq: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}
	if *downloadOnly && *uploadOnly {
		fmt.Fprintln(stderr, "nq: --download-only and --upload-only are mutually exclusive")
		return exitUsage
	}
	var t netquality.Target
	switch {
	case *configURL != "":
		t = netquality.Target{ConfigURL: *configURL}
	case *wellKnown != "":
		t = netquality.WellKnown(*wellKnown)
	default:
		switch strings.ToLower(*target) {
		case "cloudflare", "cf":
			t = netquality.Cloudflare
		case "apple":
			t = netquality.Apple
		default:
			fmt.Fprintf(stderr, "nq: unknown target %q (want cloudflare or apple)\n", *target)
			return exitUsage
		}
	}
	mb, err := parseBytes(*maxBytes)
	if err != nil {
		fmt.Fprintf(stderr, "nq: --max-bytes: %v\n", err)
		return exitUsage
	}
	opts := netquality.Options{
		MaxDuration: *maxDuration,
		MaxBytes:    mb,
		MaxFlows:    *maxFlows,
		IdleProbes:  *idleProbes,
	}
	opts.Stability.Interval = *interval
	if *authToken != "" {
		opts.Header = http.Header{"Authorization": {"Bearer " + *authToken}}
	}
	switch {
	case *downloadOnly:
		opts.Directions = netquality.Download
	case *uploadOnly:
		opts.Directions = netquality.Upload
	}
	if *insecure {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in flag
		opts.HTTPClient = &http.Client{Transport: tr}
	}
	level := slog.LevelError
	if *verbose {
		level = slog.LevelDebug
	}
	opts.Logger = slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var sink func(netquality.Event)
	if *events {
		// Events arrive from several goroutines; serialise writes.
		var mu sync.Mutex
		enc := json.NewEncoder(stderr)
		sink = func(e netquality.Event) {
			mu.Lock()
			defer mu.Unlock()
			_ = enc.Encode(e)
		}
	} else if !*jsonOut {
		sink = progressPrinter(stderr)
	}
	res, err := netquality.RunWithEvents(ctx, t, opts, sink)
	if res == nil {
		fmt.Fprintln(stderr, "nq:", err)
		return exitFail
	}
	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	} else {
		printHuman(stdout, res)
	}
	if err != nil {
		fmt.Fprintln(stderr, "nq:", err)
		return exitFail
	}
	return exitOK
}

func progressPrinter(w io.Writer) func(netquality.Event) {
	var mu sync.Mutex
	return func(e netquality.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch e.Kind {
		case netquality.EventPhase:
			if e.Phase != "done" {
				fmt.Fprintf(w, "== %s %s\n", e.Phase, e.Message)
			}
		case netquality.EventInterval:
			fmt.Fprintf(w, "   %-8s t=%-2d flows=%-2d %8.1f Mbps", e.Direction, e.Interval, e.Flows, e.ThroughputBPS/1e6)
			if e.RPM > 0 {
				fmt.Fprintf(w, "  %5.0f RPM", e.RPM)
			}
			fmt.Fprintln(w)
		case netquality.EventWarning:
			fmt.Fprintf(w, "   warning: %s\n", e.Message)
		}
	}
}

func printHuman(w io.Writer, r *netquality.Result) {
	fmt.Fprintf(w, "Target     %s", r.Target.Host)
	if r.Target.HTTPVersion != "" {
		fmt.Fprintf(w, " (%s)", r.Target.HTTPVersion)
	}
	if len(r.Target.ResolvedIPs) > 0 {
		fmt.Fprintf(w, " %s", strings.Join(r.Target.ResolvedIPs, ","))
	}
	if len(r.Target.LocalIPs) > 0 {
		fmt.Fprintf(w, " via %s", strings.Join(r.Target.LocalIPs, ","))
	}
	fmt.Fprintln(w)
	if p := r.Target.Proxy; p != nil {
		var parts []string
		if p.Explicit {
			parts = append(parts, "explicit "+p.URL)
		}
		if p.TLSInterception {
			parts = append(parts, "TLS interception by "+p.Issuer)
		}
		fmt.Fprintf(w, "Proxy      %s (numbers cover the client→proxy leg)\n", strings.Join(parts, "; "))
	}
	if r.Idle != nil {
		fmt.Fprintf(w, "Idle       %s median, %s p95, jitter %s (%d probes)\n",
			ms(r.Idle.Median), ms(r.Idle.P95), ms(r.Idle.Jitter), r.Idle.Samples)
	} else {
		fmt.Fprintln(w, "Idle       not measured")
	}
	printDir(w, "Download", r.Download)
	printDir(w, "Upload", r.Upload)
	var bytes int64
	for _, d := range []*netquality.DirectionResult{r.Download, r.Upload} {
		if d != nil {
			bytes += d.Bytes
		}
	}
	fmt.Fprintf(w, "Cost       %s moved in %.1fs\n", humanBytes(bytes), r.Duration.Seconds())
	if r.Cancelled {
		fmt.Fprintln(w, "Cancelled  partial results")
	}
	for _, wn := range r.Warnings {
		fmt.Fprintf(w, "Warning    %s\n", wn)
	}
}

func printDir(w io.Writer, name string, d *netquality.DirectionResult) {
	if d == nil {
		fmt.Fprintf(w, "%-10s not run\n", name)
		return
	}
	fmt.Fprintf(w, "%-10s %8.1f Mbps  %5.0f RPM", name, d.ThroughputBPS/1e6, d.RPM)
	if d.Loaded.Combined != nil {
		fmt.Fprintf(w, "  loaded %s median, %s p95", ms(d.Loaded.Combined.Median), ms(d.Loaded.Combined.P95))
	}
	fmt.Fprintf(w, "  [%d flows, %s/%s confidence", d.Flows, d.ThroughputConfidence, d.ResponsivenessConfidence)
	if d.Truncated {
		fmt.Fprintf(w, ", TRUNCATED: %s", d.Reason)
	}
	fmt.Fprintln(w, "]")
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond)) }

func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

// parseBytes accepts "250MB", "1GB", "100MiB", "5000000".
func parseBytes(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := int64(1)
	for _, suf := range []struct {
		s string
		m int64
	}{{"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"B", 1}} {
		if strings.HasSuffix(s, suf.s) {
			s = strings.TrimSuffix(s, suf.s)
			mult = suf.m
			break
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return int64(v * float64(mult)), nil
}
