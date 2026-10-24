// local-probe-relay-edge is a narrow TLS front end for a loopback Relay. It
// forwards only the three Relay routes and never logs headers or payloads.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "local-probe-relay-edge:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("local-probe-relay-edge", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	listen := f.String("listen", ":8443", "explicit public TLS listener")
	upstream := f.String("upstream", "http://127.0.0.1:8792", "fixed loopback Relay origin")
	cert := f.String("cert-file", "", "TLS leaf certificate file")
	key := f.String("key-file", "", "TLS private key file")
	mss := f.Int("tcp-max-segment", 0, "optional Linux per-listener TCP MSS (0 uses network default)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *cert == "" || *key == "" {
		return errors.New("explicit certificate and key required")
	}
	handler, closeTransport, err := edgeHandler(*upstream)
	if err != nil {
		return err
	}
	defer closeTransport()
	identity, err := tls.LoadX509KeyPair(*cert, *key)
	if err != nil {
		return errors.New("TLS identity unavailable")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{identity}, NextProtos: []string{"http/1.1"}}}
	listenConfig, err := edgeListenConfig(*mss)
	if err != nil {
		return err
	}
	listener, err := listenConfig.Listen(ctx, "tcp", *listen)
	if err != nil {
		return errors.New("TLS listener unavailable")
	}
	defer listener.Close()
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- server.ServeTLS(listener, "", "") }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("TLS front end stopped")
	}
}

func edgeHandler(upstream string) (http.Handler, func(), error) {
	u, err := url.Parse(upstream)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		return nil, nil, errors.New("fixed loopback HTTP upstream required")
	}
	transport := &http.Transport{Proxy: nil, MaxConnsPerHost: 128, MaxIdleConnsPerHost: 8, ResponseHeaderTimeout: 10 * time.Second, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext}
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(u); r.Out.Host = u.Host }, Transport: transport, ErrorLog: log.New(io.Discard, "", 0), ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "Relay origin unavailable", http.StatusBadGateway)
	}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RawQuery != "" || (r.URL.Path != "/healthz" && r.URL.Path != "/v1/target" && r.URL.Path != "/v1/agent") {
			http.NotFound(w, r)
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			http.Error(w, "request body not supported", http.StatusBadRequest)
			return
		}
		proxy.ServeHTTP(w, r)
	}), transport.CloseIdleConnections, nil
}
