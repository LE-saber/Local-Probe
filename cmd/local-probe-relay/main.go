// local-probe-relay runs behind an HTTPS/WSS reverse proxy. It binds loopback
// only and loads role-separated credentials from environment references.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/LE-saber/Local-Probe/internal/deviceidentity"
	"github.com/LE-saber/Local-Probe/internal/relay"
)

type settings struct {
	Version string        `json:"version"`
	Routes  []routeConfig `json:"routes"`
}
type routeConfig struct {
	DeviceID       string `json:"device_id"`
	TargetTokenEnv string `json:"target_token_env"`
	AgentTokenEnv  string `json:"agent_token_env"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "local-probe-relay:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("local-probe-relay", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	path := f.String("config", "", "absolute Relay route config with environment references")
	listen := f.String("listen", "127.0.0.1:8792", "loopback HTTPS reverse-proxy origin")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || !filepath.IsAbs(*path) {
		return errors.New("explicit absolute Relay config required")
	}
	host, _, err := net.SplitHostPort(*listen)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return errors.New("Relay must bind explicit loopback behind HTTPS")
	}
	file, err := os.Open(*path)
	if err != nil {
		return errors.New("Relay config unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(file, deviceidentity.MaxArtifactBytes+1))
	_ = file.Close()
	if err != nil {
		return errors.New("Relay config unavailable")
	}
	var cfg settings
	if deviceidentity.DecodePublic(data, &cfg) != nil || cfg.Version != "local-probe.relay.v1" {
		return errors.New("invalid Relay configuration")
	}
	var routes []relay.Route
	for _, r := range cfg.Routes {
		if !validEnv(r.TargetTokenEnv) || !validEnv(r.AgentTokenEnv) {
			return errors.New("invalid credential environment reference")
		}
		routes = append(routes, relay.Route{DeviceID: r.DeviceID, TargetToken: os.Getenv(r.TargetTokenEnv), AgentToken: os.Getenv(r.AgentTokenEnv)})
	}
	service, err := relay.New(routes)
	if err != nil {
		return err
	}
	defer service.Close()
	server := &http.Server{Handler: service, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	defer server.Close()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return errors.New("cannot bind Relay loopback origin")
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		return nil
	case <-done:
		return errors.New("Relay stopped")
	}
}
func validEnv(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i, c := range s {
		if !(c >= 'A' && c <= 'Z' || c == '_' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
