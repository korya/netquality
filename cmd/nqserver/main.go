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
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, "nqserver:", err)
		return exitFail
	}
	srv := &http.Server{
		Handler:           server.Handler(server.Options{BaseURL: *baseURL, LargeSize: *largeSize, TestEndpoint: *endpoint}),
		TLSConfig:         server.TLSConfig(cert),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Fprintf(stderr, "nqserver %s listening on %s (config at https://<host>%s)\n", buildinfo.String(), ln.Addr(), server.ConfigPath)
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
