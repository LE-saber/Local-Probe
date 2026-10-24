// local-probe-agent is started by an Agent's native MCP stdio client. Stdout is
// reserved for the official SDK; credentials are read from local environment.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/LE-saber/Local-Probe/internal/agentbridge"
	"github.com/LE-saber/Local-Probe/internal/deviceidentity"
	"github.com/LE-saber/Local-Probe/internal/peertransport"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "local-probe-agent:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("local-probe-agent", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	path := f.String("store", "", "absolute paired Agent identity directory")
	endpoint := f.String("endpoint", "", "explicit wss:// Tunnel or Relay data endpoint")
	transport := f.String("transport", "tunnel", "tunnel or relay")
	outerCA := f.String("outer-ca-file", "", "optional absolute public CA PEM for private Relay/edge TLS")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional argument")
	}
	store, err := deviceidentity.OpenStore(*path)
	if err != nil {
		return err
	}
	defer store.Close()
	inner, err := store.ClientTLS()
	if err != nil {
		return err
	}
	headers := http.Header{}
	if value := os.Getenv("LOCAL_PROBE_AGENT_ACCESS_TOKEN"); value != "" {
		headers.Set("Authorization", "Bearer "+value)
	}
	id, secret := os.Getenv("LOCAL_PROBE_CF_CLIENT_ID"), os.Getenv("LOCAL_PROBE_CF_CLIENT_SECRET")
	if (id == "") != (secret == "") {
		return errors.New("both Cloudflare credential environment references are required")
	}
	if id != "" {
		headers.Set("CF-Access-Client-Id", id)
		headers.Set("CF-Access-Client-Secret", secret)
	}
	if *transport != "tunnel" && *transport != "relay" {
		return errors.New("transport must be tunnel or relay")
	}
	roots, err := peertransport.LoadPublicRoots(*outerCA)
	if err != nil {
		return err
	}
	newClient := func(endpoint string, inner *tls.Config, headers http.Header) (*http.Client, func(), error) {
		return peertransport.HTTPClientWithRoots(endpoint, inner, headers, roots)
	}
	if *transport == "relay" {
		newClient = func(endpoint string, inner *tls.Config, headers http.Header) (*http.Client, func(), error) {
			return peertransport.RelayHTTPClient(endpoint, inner, headers, roots)
		}
	}
	client, closeTransport, err := newClient(*endpoint, inner, headers)
	if err != nil {
		return err
	}
	defer closeTransport()
	server, remote, err := agentbridge.New(ctx, client)
	if err != nil {
		return err
	}
	defer remote.Close()
	return server.Run(ctx, &mcp.StdioTransport{})
}
