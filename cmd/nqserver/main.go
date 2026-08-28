// Command nqserver is a minimal, self-hostable responsiveness test server.
package main

import (
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/korya/netquality/internal/buildinfo"
	"github.com/korya/netquality/server"
)

func main() {
	var (
		listen     = flag.String("listen", ":8443", "address to listen on")
		certFile   = flag.String("cert", "", "TLS certificate file (PEM)")
		keyFile    = flag.String("key", "", "TLS private key file (PEM)")
		selfSigned = flag.Bool("self-signed", false, "generate a self-signed certificate (dev only)")
		baseURL    = flag.String("base-url", "", "external URL prefix advertised in the config (default: derived from Host header)")
		largeSize  = flag.Int64("large-size", 8<<30, "size of the large download body in bytes")
		endpoint   = flag.String("test-endpoint", "", "advertise this host as test_endpoint")
		version    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *version {
		fmt.Println("nqserver", buildinfo.String())
		return
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
		fmt.Fprintln(os.Stderr, "nqserver:", err)
		os.Exit(2)
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           server.Handler(server.Options{BaseURL: *baseURL, LargeSize: *largeSize, TestEndpoint: *endpoint}),
		TLSConfig:         server.TLSConfig(cert),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("nqserver %s listening on %s (config at https://<host>%s)", buildinfo.Version, *listen, server.ConfigPath)
	if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
