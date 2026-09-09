// local-probe-mcp starts the read-only Local-Probe MCP endpoint.
//
// The command intentionally requires an explicit JSON config and reads the
// local hop token from an environment variable or protected file. It binds to
// loopback by default; the command refuses non-loopback listeners.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/mcpserver"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/rootfs"
)

const defaultListenAddr = "127.0.0.1:8787"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "local-probe-mcp:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr *os.File) error {
	flags := flag.NewFlagSet("local-probe-mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "validated Local-Probe JSON configuration")
	connectionID := flags.String("connection-id", "", "configured connection to bind to the local hop token")
	tokenEnv := flags.String("token-env", "", "environment variable containing the local MCP hop token")
	tokenFile := flags.String("token-file", "", "protected file containing the local MCP hop token")
	listenAddr := flags.String("listen-addr", defaultListenAddr, "loopback listen address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *connectionID == "" {
		return errors.New("provide -config and -connection-id; no positional arguments are accepted")
	}
	if err := validateLoopbackAddr(*listenAddr); err != nil {
		return err
	}
	token, err := mcpserver.ResolveToken(*tokenEnv, *tokenFile)
	if err != nil {
		return fmt.Errorf("resolve local MCP token: %w", err)
	}

	configFile, err := os.Open(*configPath)
	if err != nil {
		return errors.New("cannot open Local-Probe configuration")
	}
	cfg, loadErr := config.Load(configFile)
	closeErr := configFile.Close()
	if loadErr != nil || closeErr != nil {
		return errors.New("cannot load Local-Probe configuration")
	}
	store, err := config.NewStore(cfg)
	if err != nil {
		return errors.New("configuration store is invalid")
	}
	manager, err := policy.NewManager(store)
	if err != nil {
		return errors.New("policy manager is unavailable")
	}
	source, err := rootfs.New(store)
	if err != nil {
		return errors.New("configured filesystem roots cannot be opened safely")
	}
	defer source.Close()
	server, err := mcpserver.New(mcpserver.Options{
		Manager: manager,
		Source:  source,
		Credentials: []mcpserver.Credential{{
			ConnectionID: *connectionID,
			Token:        token,
		}},
	})
	if err != nil {
		return fmt.Errorf("create MCP server: %w", err)
	}

	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		return errors.New("cannot bind loopback MCP listener")
	}
	defer listener.Close()
	httpServer := &http.Server{
		Handler:           routeMCP(server.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverErr := make(chan error, 1)
	go func() { serverErr <- httpServer.Serve(listener) }()
	fmt.Fprintf(stdout, "Local-Probe MCP listening on http://%s/mcp\n", listener.Addr().String())

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return errors.New("MCP server shutdown failed")
		}
		return nil
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("MCP HTTP server stopped unexpectedly")
	}
}

// routeMCP deliberately leaves every non-MCP path as 404. In particular,
// tunnel-client uses 404 on /.well-known OAuth metadata candidates to
// recognize an HTTP MCP server that intentionally has no OAuth/DCR contract.
// Applying the local authentication handler to those paths would turn them
// into 401 responses and incorrectly trigger OAuth metadata discovery.
func routeMCP(handler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	return mux
}

func validateLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		return errors.New("-listen-addr must be host:port")
	}
	if strings.HasPrefix(host, "[") || strings.HasSuffix(host, "]") {
		host = strings.Trim(host, "[]")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("-listen-addr must be an explicit loopback IP (127.0.0.1 or ::1)")
	}
	return nil
}
