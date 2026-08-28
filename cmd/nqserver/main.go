// Command nqserver is a minimal, self-hostable responsiveness test server.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/korya/netquality/internal/buildinfo"
	"github.com/korya/netquality/server"
)

const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, nil))
}

// run parses args and serves until ctx is done. onListen, if non-nil, is
// called with the bound address once the listener is open.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, onListen func(net.Addr)) int {
	fs := flag.NewFlagSet("nqserver", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		listen     = fs.String("listen", ":8443", "address to listen on")
		certFile   = fs.String("cert", "", "TLS certificate file (PEM)")
		keyFile    = fs.String("key", "", "TLS private key file (PEM)")
		selfSigned = fs.Bool("self-signed", false, "generate a self-signed certificate (dev only)")
		baseURL    = fs.String("base-url", "", "external URL prefix advertised in the config (default: derived from Host header)")
		largeSize  = fs.Int64("large-size", 8<<30, "size of the large download body in bytes")
		endpoint   = fs.String("test-endpoint", "", "advertise this host as test_endpoint")
		authToken  = fs.String("auth-token", os.Getenv("NQSERVER_AUTH_TOKEN"), "bearer token required on every endpoint (env NQSERVER_AUTH_TOKEN)")
		anonymous  = fs.Bool("allow-anonymous", false, "serve without a token (implied by --self-signed)")
		uploadSize = fs.Int64("upload-size", 16<<30, "maximum bytes accepted by one upload request")
		maxConns   = fs.Int("max-connections", 256, "maximum simultaneous connections (0 = unlimited)")
		clientMax  = fs.Int64("client-bytes", 0, "bytes one client IP may move per --client-window before 429 (-1 = unlimited; default 8 GiB, unlimited with --self-signed)")
		clientWin  = fs.Duration("client-window", server.DefaultClientWindow, "window for --client-bytes")
		version    = fs.Bool("version", false, "print version and exit")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if *version {
		fmt.Fprintln(stdout, "nqserver", buildinfo.String())
		return exitOK
	}
	var cert tls.Certificate
	var err error
	switch {
	case *selfSigned:
		host, _, _ := net.SplitHostPort(*listen)
		cert, err = server.SelfSignedCert(host)
	case *certFile != "" && *keyFile != "":
		cert, err = tls.LoadX509KeyPair(*certFile, *keyFile)
	default:
		err = errors.New("TLS is required: pass --cert/--key or --self-signed")
	}
	if err != nil {
		fmt.Fprintln(stderr, "nqserver:", err)
		return exitUsage
	}
	if *authToken == "" && !*anonymous && !*selfSigned {
		fmt.Fprintln(stderr, "nqserver: refusing to serve anonymously with a real certificate; pass --auth-token (or NQSERVER_AUTH_TOKEN), or --allow-anonymous")
		return exitUsage
	}
	if *clientMax == 0 {
		*clientMax = server.DefaultMaxClientBytes
		if *selfSigned {
			*clientMax = -1 // dev mode on loopback moves gigabytes per run
		}
	}
	if *maxConns > 0 && *maxConns < 64 {
		fmt.Fprintf(stderr, "nqserver: warning: --max-connections %d is below a single client's flows plus probes; tests may stall\n", *maxConns)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, "nqserver:", err)
		return exitFail
	}
	ln = server.LimitListener(ln, *maxConns)
	srv := &http.Server{
		Handler: server.Handler(server.Options{BaseURL: *baseURL, LargeSize: *largeSize, TestEndpoint: *endpoint,
			AuthToken: *authToken, UploadSize: *uploadSize, MaxClientBytes: *clientMax, ClientWindow: *clientWin}),
		TLSConfig:         server.TLSConfig(cert),
		ReadHeaderTimeout: 10 * time.Second,
	}
	mode := "token auth"
	if *authToken == "" {
		mode = "ANONYMOUS"
	}
	fmt.Fprintf(stderr, "nqserver %s listening on %s (config at https://<host>%s) %s, upload cap %d B, client budget %d B/%s, max connections %d\n",
		buildinfo.String(), ln.Addr(), server.ConfigPath, mode, *uploadSize, *clientMax, *clientWin, *maxConns)
	if onListen != nil {
		onListen(ln.Addr())
	}
	done := make(chan error, 1)
	go func() { done <- srv.ServeTLS(ln, "", "") }()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(stderr, "nqserver:", err)
			return exitFail
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}
	return exitOK
}
