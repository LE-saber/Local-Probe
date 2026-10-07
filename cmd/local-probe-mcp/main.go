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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/LE-saber/Local-Probe/internal/audit"
	"github.com/LE-saber/Local-Probe/internal/cfaccess"
	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/environment"
	"github.com/LE-saber/Local-Probe/internal/mcpserver"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/rootfs"
	"github.com/LE-saber/Local-Probe/internal/workspacesnapshot"
)

const defaultListenAddr = "127.0.0.1:8787"

const (
	ingressLocalToken       = "local-token"
	ingressCloudflareAccess = "cloudflare-access"
)

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
	ingress := flags.String("ingress", ingressLocalToken, "authentication ingress: local-token or cloudflare-access")
	connectionID := flags.String("connection-id", "", "configured connection to bind to the local hop token (local-token only)")
	tokenEnv := flags.String("token-env", "", "environment variable containing the local MCP hop token")
	tokenFile := flags.String("token-file", "", "protected file containing the local MCP hop token")
	cloudflareAccessConfig := flags.String("cloudflare-access-config", "", "external Cloudflare Access JSON config (cloudflare-access only)")
	listenAddr := flags.String("listen-addr", defaultListenAddr, "loopback listen address")
	auditDir := flags.String("audit-dir", "", "audit directory (default: user config Local-Probe/audit)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" {
		return errors.New("provide -config; no positional arguments are accepted")
	}
	if err := validateLoopbackAddr(*listenAddr); err != nil {
		return err
	}
	mode := strings.ToLower(strings.TrimSpace(*ingress))
	if mode != ingressLocalToken && mode != ingressCloudflareAccess {
		return errors.New("-ingress must be local-token or cloudflare-access")
	}
	if mode == ingressLocalToken && *connectionID == "" {
		return errors.New("-connection-id is required for local-token ingress")
	}
	if mode == ingressCloudflareAccess && (*connectionID != "" || *tokenEnv != "" || *tokenFile != "") {
		if *connectionID != "" {
			return errors.New("-connection-id is only valid for local-token ingress")
		}
		if *tokenEnv != "" || *tokenFile != "" {
			return errors.New("local MCP token flags are only valid for local-token ingress")
		}
	}
	if mode == ingressLocalToken && *cloudflareAccessConfig != "" {
		return errors.New("-cloudflare-access-config is only valid for cloudflare-access ingress")
	}

	var token string
	if mode == ingressLocalToken {
		var err error
		token, err = mcpserver.ResolveToken(*tokenEnv, *tokenFile)
		if err != nil {
			return fmt.Errorf("resolve local MCP token: %w", err)
		}
	} else if strings.TrimSpace(*cloudflareAccessConfig) == "" {
		return errors.New("-cloudflare-access-config is required for cloudflare-access ingress")
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
	auditSink, err := openAuditSink(*auditDir)
	if err != nil {
		return err
	}
	auditClosed := false
	defer func() {
		if !auditClosed {
			_ = auditSink.Close()
		}
	}()
	options := mcpserver.Options{
		Manager:                 manager,
		Source:                  source,
		EnvironmentTools:        environmentToolSpecs(cfg),
		WorkspaceSnapshotLimits: workspacesnapshot.DefaultLimits(),
		Audit:                   auditSink,
	}
	if mode == ingressLocalToken {
		options.Credentials = []mcpserver.Credential{{ConnectionID: *connectionID, Token: token}}
	} else {
		accessConfig, loadErr := cfaccess.LoadFile(*cloudflareAccessConfig)
		if loadErr != nil {
			return errors.New("cannot load Cloudflare Access configuration")
		}
		accessVerifier, verifierErr := cfaccess.New(accessConfig)
		if verifierErr != nil {
			return errors.New("Cloudflare Access configuration is invalid")
		}
		options.CloudflareAccess = accessVerifier
		options.CloudflarePublicHosts = accessConfig.PublicHosts
	}
	server, err := mcpserver.New(options)
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
	fmt.Fprintf(stdout, "Local-Probe MCP listening on http://%s/mcp (ingress=%s)\n", listener.Addr().String(), mode)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := httpServer.Shutdown(shutdownCtx)
		closeAuditErr := auditSink.Close()
		auditClosed = true
		if shutdownErr != nil {
			return errors.New("MCP server shutdown failed")
		}
		if closeAuditErr != nil {
			return errors.New("audit sink shutdown failed")
		}
		return nil
	case err := <-serverErr:
		closeAuditErr := auditSink.Close()
		auditClosed = true
		if closeAuditErr != nil {
			return errors.New("audit sink shutdown failed")
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("MCP HTTP server stopped unexpectedly")
	}
}

var newAuditSink = func(directory string) (*audit.Sink, error) {
	return audit.New(audit.Config{Directory: directory})
}

func openAuditSink(override string) (*audit.Sink, error) {
	directory, err := resolveAuditDir(override)
	if err != nil {
		return nil, err
	}
	sink, err := newAuditSink(directory)
	if err != nil {
		return nil, errors.New("cannot initialize audit sink")
	}
	return sink, nil
}

func resolveAuditDir(override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		return override, nil
	}
	base, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(base) == "" {
		return "", errors.New("cannot determine user config directory for audit")
	}
	return filepath.Join(base, "Local-Probe", "audit"), nil
}

func environmentToolSpecs(cfg config.Config) []environment.ToolSpec {
	configured := cfg.EnvironmentTools()
	if configured == nil {
		return nil
	}
	specs := make([]environment.ToolSpec, len(configured))
	for i, tool := range configured {
		specs[i] = environment.ToolSpec{
			ID:             tool.ID(),
			CandidateFiles: tool.CandidateFiles(),
			CandidateDirs:  tool.CandidateDirs(),
		}
	}
	return specs
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
