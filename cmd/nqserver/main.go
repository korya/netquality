// Command nqserver is a minimal, self-hostable responsiveness test server.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
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

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// runSign implements `nqserver sign`: mint signed URLs for testing an issuer
// flow without a backend, or generate a key.
func runSign(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("nqserver sign", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		key    = fs.String("key", os.Getenv("NQSERVER_SIGNING_KEY"), "signing key: hex, base64url, or file:<path> (env NQSERVER_SIGNING_KEY)")
		ttl    = fs.Duration("ttl", 10*time.Minute, "validity of the signed URLs")
		sub    = fs.String("sub", "", "subject (e.g. device id); keys the server's per-client budget")
		newKey = fs.Bool("new-key", false, "print a fresh random 32-byte key (hex) and exit")
	)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: nqserver sign [--key K] [--ttl D] [--sub S] URL...\n       nqserver sign --new-key\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if *newKey {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			fmt.Fprintln(stderr, "nqserver sign:", err)
			return exitFail
		}
		fmt.Fprintln(stdout, hex.EncodeToString(b))
		return exitOK
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "nqserver sign: at least one URL is required")
		return exitUsage
	}
	if *ttl <= 0 || *ttl > server.MaxSignatureTTL {
		fmt.Fprintf(stderr, "nqserver sign: --ttl must be between 1s and %s\n", server.MaxSignatureTTL)
		return exitUsage
	}
	k, err := server.ParseSigningKey(*key)
	if err != nil {
		fmt.Fprintln(stderr, "nqserver sign: --key:", err)
		return exitUsage
	}
	exp := time.Now().Add(*ttl)
	for _, u := range fs.Args() {
		signed, err := server.SignURL(k, u, exp, *sub)
		if err != nil {
			fmt.Fprintf(stderr, "nqserver sign: %s: %v\n", u, err)
			return exitUsage
		}
		fmt.Fprintln(stdout, signed)
	}
	return exitOK
}

// run parses args and serves until ctx is done. onListen, if non-nil, is
// called with the bound address once the listener is open.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, onListen func(net.Addr)) int {
	if len(args) > 0 && args[0] == "sign" {
		return runSign(args[1:], stdout, stderr)
	}
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
		anonymous  = fs.Bool("allow-anonymous", false, "serve without a token or signing key (implied by --self-signed)")
		signingKey multiFlag
		uploadSize = fs.Int64("upload-size", 16<<30, "maximum bytes accepted by one upload request")
		maxConns   = fs.Int("max-connections", 256, "maximum simultaneous connections (0 = unlimited)")
		clientMax  = fs.Int64("client-bytes", 0, "bytes one client IP may move per --client-window before 429 (-1 = unlimited; default 8 GiB, unlimited with --self-signed)")
		clientWin  = fs.Duration("client-window", server.DefaultClientWindow, "window for --client-bytes")
		version    = fs.Bool("version", false, "print version and exit")
	)
	fs.Var(&signingKey, "signing-key", "accept URLs signed with this key (hex, base64url, or file:<path>; repeatable for rotation; env NQSERVER_SIGNING_KEY)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if len(signingKey) == 0 && os.Getenv("NQSERVER_SIGNING_KEY") != "" {
		signingKey = multiFlag{os.Getenv("NQSERVER_SIGNING_KEY")}
	}
	var keys [][]byte
	for _, k := range signingKey {
		parsed, err := server.ParseSigningKey(k)
		if err != nil {
			fmt.Fprintln(stderr, "nqserver: --signing-key:", err)
			return exitUsage
		}
		keys = append(keys, parsed)
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
	if *authToken == "" && len(keys) == 0 && !*anonymous && !*selfSigned {
		fmt.Fprintln(stderr, "nqserver: refusing to serve anonymously with a real certificate; pass --auth-token (or NQSERVER_AUTH_TOKEN), --signing-key, or --allow-anonymous")
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
			AuthToken: *authToken, SigningKeys: keys, UploadSize: *uploadSize, MaxClientBytes: *clientMax, ClientWindow: *clientWin}),
		TLSConfig:         server.TLSConfig(cert),
		ReadHeaderTimeout: 10 * time.Second,
	}
	var modes []string
	if *authToken != "" {
		modes = append(modes, "token auth")
	}
	if len(keys) > 0 {
		modes = append(modes, fmt.Sprintf("signed URLs (%d key(s))", len(keys)))
	}
	mode := strings.Join(modes, " + ")
	if mode == "" {
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
