// local-probe-target is a paired Agent ingress, read-only by default, with an
// optional explicitly configured trusted-host operation engine.
// A configured Tunnel supplies public WSS; inner mTLS terminates here. This is
// independent from the user's existing desktop/Tunnel process.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/LE-saber/Local-Probe/internal/audit"
	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/deviceidentity"
	"github.com/LE-saber/Local-Probe/internal/mcpserver"
	"github.com/LE-saber/Local-Probe/internal/operations"
	"github.com/LE-saber/Local-Probe/internal/peertransport"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/rootfs"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "local-probe-target:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("local-probe-target", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	identityPath := f.String("store", "", "absolute target identity directory")
	configPath := f.String("config", "", "absolute target configuration")
	listen := f.String("listen", "127.0.0.1:8791", "loopback Tunnel origin only")
	relayEndpoint := f.String("relay-endpoint", "", "optional wss:// self-hosted Relay /v1/target endpoint; outbound only")
	outerCA := f.String("outer-ca-file", "", "optional absolute public CA PEM for private Relay TLS")
	auditPath := f.String("audit-dir", "", "explicit private audit directory")
	operationJournal := f.String("operations-journal", "", "optional owner-private initialized journal outside all workspaces")
	operationCatalog := f.String("operations-catalog", "", "optional absolute trusted runner catalog; requires operation journal")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	host, _, err := net.SplitHostPort(*listen)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return errors.New("explicit loopback listener required")
	}
	if !filepath.IsAbs(*auditPath) {
		return errors.New("explicit absolute private audit directory required")
	}
	tokenEnv := "LOCAL_PROBE_TARGET_ACCESS_TOKEN"
	if *relayEndpoint != "" {
		tokenEnv = "LOCAL_PROBE_RELAY_TARGET_TOKEN"
	}
	token := os.Getenv(tokenEnv)
	if len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, "\r\n") {
		return errors.New("set the selected transport's strong admission credential locally")
	}
	expected := sha256.Sum256([]byte("Bearer " + token))
	identity, err := deviceidentity.OpenStore(*identityPath)
	if err != nil {
		return err
	}
	defer identity.Close()
	registry, err := deviceidentity.NewRegistry(identity)
	if err != nil {
		return err
	}
	fileStore, err := config.NewFileStore(*configPath)
	if err != nil {
		return errors.New("invalid config path")
	}
	snapshot, err := fileStore.Load()
	if err != nil {
		return errors.New("config unavailable")
	}
	store, err := config.NewStore(snapshot.Config())
	if err != nil {
		return err
	}
	manager, err := policy.NewManager(store)
	if err != nil {
		return err
	}
	source, err := rootfs.New(store)
	if err != nil {
		return errors.New("cannot safely open roots")
	}
	defer source.Close()
	sink, err := audit.New(audit.Config{Directory: *auditPath})
	if err != nil {
		return errors.New("private audit directory unavailable")
	}
	defer sink.Close()
	var operationEngine *operations.Engine
	if (*operationJournal == "") != (*operationCatalog == "") {
		return errors.New("operation journal and catalog required together")
	}
	if *operationJournal != "" {
		if runtime.GOOS != "windows" {
			return errors.New("trusted-host operation target currently requires Windows; Linux Agent and Relay remain supported")
		}
		catalog, err := operations.LoadCatalog(*operationCatalog)
		if err != nil {
			return err
		}
		journal, err := operations.OpenJournal(*operationJournal, false)
		if err != nil {
			return err
		}
		defer journal.Close()
		operationEngine, err = operations.New(ctx, operations.Options{Journal: journal, Config: store, Manager: manager, Workspace: source, Catalog: catalog, Acquire: func(parent context.Context, subject operations.Subject) (context.Context, func(), error) {
			decoded, err := hex.DecodeString(subject.Principal)
			if err != nil || len(decoded) != 32 {
				return nil, nil, operations.ErrDenied
			}
			var pin peertransport.Pin
			copy(pin[:], decoded)
			lease, release, err := registry.Acquire(parent, pin)
			if err != nil {
				return nil, nil, err
			}
			g, ok := deviceidentity.GrantFromContext(lease)
			if !ok || g.ConnectionID != subject.Connection {
				release()
				return nil, nil, operations.ErrDenied
			}
			return lease, release, nil
		}})
		if err != nil {
			return err
		}
		defer operationEngine.Close()
	}
	server, err := mcpserver.New(mcpserver.Options{Manager: manager, Source: source, PeerRegistry: registry, Audit: sink, Operations: operationEngine})
	if err != nil {
		return err
	}
	inner, err := registry.ServerTLS()
	if err != nil {
		return err
	}
	endpoint, err := peertransport.NewEndpoint(inner, server.Handler(), func(r *http.Request) bool {
		values := r.Header.Values("Authorization")
		if len(values) != 1 || len(values[0]) > 4103 {
			return false
		}
		actual := sha256.Sum256([]byte(values[0]))
		return subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
	})
	if err != nil {
		return err
	}
	defer endpoint.Close()
	if *relayEndpoint != "" {
		roots, err := peertransport.LoadPublicRoots(*outerCA)
		if err != nil {
			return err
		}
		return endpoint.ServeRelay(ctx, *relayEndpoint, http.Header{"Authorization": []string{"Bearer " + token}}, roots)
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/peer", endpoint)
	outer := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return errors.New("cannot bind loopback origin")
	}
	defer listener.Close()
	defer outer.Close()
	done := make(chan error, 1)
	go func() { done <- outer.Serve(listener) }()
	select {
	case <-ctx.Done():
		return nil
	case <-done:
		return errors.New("paired target stopped")
	}
}
