package mcpserver

import (
	"context"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/agentbridge"
	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/deviceidentity"
	"github.com/LE-saber/Local-Probe/internal/operations"
	"github.com/LE-saber/Local-Probe/internal/peertransport"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/rootfs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPairedMCPFileOperationRequiresLocalApprovalAndPreservesConflicts(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	f.profileOneTools = append([]string{ToolServerInfo, ToolReadFile}, operationTools...)
	_ = newTestServer(t, f, []Credential{{ConnectionID: "connection-one", Token: testTokenOne}})
	initial := f.store.Snapshot().Config()
	connections := initial.Connections()
	var err error
	connections[0], err = connections[0].WithPurpose(config.PurposeAgentRemote)
	if err != nil {
		t.Fatal(err)
	}
	connections[0], err = connections[0].WithExecutionPolicy(config.ExecutionTrustedHost)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := commandprofile.NewDeveloperMode(true, []string{"connection-one"}, commandprofile.ConfirmationPerCall, commandprofile.NetworkDeny)
	if err != nil {
		t.Fatal(err)
	}
	profiles := initial.Profiles()
	profiles[0] = profiles[0].WithWriteAccess()
	cfg, err := config.NewWithCommandProfiles(config.SchemaVersionV2, initial.Roots(), profiles, connections, initial.Credentials(), nil, mode, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.source.Close()
	f.store, err = config.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.manager, err = policy.NewManager(f.store)
	if err != nil {
		t.Fatal(err)
	}
	f.source, err = rootfs.New(f.store)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := deviceidentity.CreateStore(filepath.Join(t.TempDir(), "identity"))
	if err != nil {
		t.Fatal(err)
	}
	defer identity.Close()
	a, err := deviceidentity.NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if err = identity.SaveAuthority(a); err != nil {
		t.Fatal(err)
	}
	agent, request, err := deviceidentity.NewAgent()
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := request.Fingerprint()
	grant, err := identity.IssueGrant(request, fp, "connection-one", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := deviceidentity.NewRegistry(identity)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := operations.OpenJournal(filepath.Join(t.TempDir(), "journal"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if err = journal.Initialize(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	engine, err := operations.New(ctx, operations.Options{Journal: journal, Config: f.store, Manager: f.manager, Workspace: f.source, Catalog: operations.Catalog{Version: operations.Version}, Acquire: func(parent context.Context, s operations.Subject) (context.Context, func(), error) {
		b, err := hex.DecodeString(s.Principal)
		if err != nil || len(b) != 32 {
			return nil, nil, operations.ErrDenied
		}
		var pin peertransport.Pin
		copy(pin[:], b)
		lease, release, err := registry.Acquire(parent, pin)
		if err != nil {
			return nil, nil, err
		}
		g, ok := deviceidentity.GrantFromContext(lease)
		if !ok || g.ConnectionID != s.Connection {
			release()
			return nil, nil, operations.ErrDenied
		}
		return lease, release, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	server, err := New(Options{Manager: f.manager, Source: f.source, PeerRegistry: registry, Operations: engine})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := registry.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	target, err := peertransport.NewEndpoint(inner, server.Handler(), func(*http.Request) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	outer := httptest.NewTLSServer(target)
	defer outer.Close()
	roots := x509.NewCertPool()
	roots.AddCert(outer.Certificate())
	bundle := a.Bundle()
	bf, _ := bundle.Fingerprint()
	clientTLS, err := agent.ClientTLS(bundle, grant, bf)
	if err != nil {
		t.Fatal(err)
	}
	hc, closeTransport, err := peertransport.HTTPClientWithRoots("wss"+strings.TrimPrefix(outer.URL, "https"), clientTLS, nil, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTransport()
	bridge, remote, err := agentbridge.New(ctx, hc)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	left, right := mcp.NewInMemoryTransports()
	ss, err := bridge.Connect(ctx, right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "operation-native-agent", Version: "test"}, nil)
	session, err := client.Connect(ctx, left, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range list.Tools {
		if strings.Contains(tool.Name, "approve") {
			t.Fatal("remote approval tool exposed")
		}
		if tool.Name == "prepare_file_change" {
			found = true
		}
	}
	if !found {
		t.Fatal("authorized operation tool missing")
	}
	var info serverInfoOutput
	decodeToolJSON(t, callTool(t, ctx, session, "server_info", map[string]any{}), &info)
	if info.ReadOnly || info.ExecutionPolicy != "trusted_host" {
		t.Fatal("operation target incorrectly advertised read-only or isolated execution")
	}
	bad, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "prepare_file_change", Arguments: map[string]any{"nonce": "forged", "root_id": "workspace", "path": "new.txt", "content": "bad", "confirmed": true}})
	if err == nil && !bad.IsError {
		t.Fatal("remote confirmation boolean was accepted")
	}
	for _, scenario := range []string{"new", "conflict"} {
		path := scenario + ".txt"
		if scenario == "conflict" {
			if err = os.WriteFile(filepath.Join(f.root, path), []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		result := callTool(t, ctx, session, "prepare_file_change", map[string]any{"nonce": scenario, "root_id": "workspace", "path": path, "content": "approved"})
		var prepared operations.Summary
		decodeToolJSON(t, result, &prepared)
		if prepared.State != "prepared" {
			t.Fatal("operation preparation failed")
		}
		result = callTool(t, ctx, session, "commit_file_change", map[string]any{"id": prepared.ID, "digest": prepared.Digest})
		var submitted operations.Summary
		decodeToolJSON(t, result, &submitted)
		if submitted.State != "awaiting_approval" {
			t.Fatal("job skipped local approval")
		}
		if scenario == "new" {
			if _, err = os.Stat(filepath.Join(f.root, path)); !os.IsNotExist(err) {
				t.Fatal("unapproved write happened")
			}
		} else {
			if err = os.WriteFile(filepath.Join(f.root, path), []byte("user-edit"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err = journal.Decide(prepared.ID, prepared.Digest, true, true); err != nil {
			t.Fatal(err)
		}
		var status operations.Summary
		for {
			decodeToolJSON(t, callTool(t, ctx, session, "operation_status", map[string]any{"id": prepared.ID}), &status)
			if status.State == "succeeded" || status.State == "failed" {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("operation did not finish")
			case <-time.After(10 * time.Millisecond):
			}
		}
		want := "approved"
		if scenario == "conflict" {
			want = "user-edit"
			if status.State != "failed" || status.ErrorCode != "file_conflict" {
				t.Fatalf("conflict outcome=%+v", status)
			}
		} else if status.State != "succeeded" {
			t.Fatalf("write outcome=%+v", status)
		}
		content, err := os.ReadFile(filepath.Join(f.root, path))
		if err != nil || string(content) != want {
			t.Fatalf("actual content=%q, want %q: %v", content, want, err)
		}
	}
	var pin peertransport.Pin
	decoded, _ := hex.DecodeString(grant.KeyPin)
	copy(pin[:], decoded)
	if err = registry.Revoke(pin); err != nil {
		t.Fatal(err)
	}
	if result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "operation_status", Arguments: map[string]any{"id": strings.Repeat("0", 64)}}); err == nil && !result.IsError {
		t.Fatal("revoked identity retained operation access")
	}
}
