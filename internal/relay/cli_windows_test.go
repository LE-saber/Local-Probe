//go:build windows

package relay

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/deviceidentity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Builds and launches the REAL target and Agent CLIs. The Agent is controlled
// through the official SDK's stdio CommandTransport, not an in-memory stand-in.
func TestWindowsRealCLIReadsFileThroughRelay(t *testing.T) {
	base := t.TempDir()
	binaries := map[string]string{}
	for _, name := range []string{"local-probe-target", "local-probe-agent"} {
		bin := filepath.Join(base, name+".exe")
		command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-o", bin, "../../cmd/"+name)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v: %s", name, err, output)
		}
		binaries[name] = bin
	}
	workspace := filepath.Join(base, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "evidence.txt"), []byte("real-cli-relay-evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := config.NewRoot("workspace", workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfile("profile-a", []string{"workspace"}, []string{"server_info", "read_file"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := config.NewConnection("connection-a", "test", profile.ID(), "credential-a", true).WithPurpose(config.PurposeAgentRemote)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.New(config.SchemaVersionV2, []config.Root{root}, []config.Profile{profile}, []config.Connection{connection}, []config.CredentialRef{config.NewCredentialRef("credential-a", "runtime")})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(base, "config.json")
	fileStore, err := config.NewFileStore(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fileStore.Save(cfg); err != nil {
		t.Fatal(err)
	}
	targetPath, agentPath := filepath.Join(base, "target-identity"), filepath.Join(base, "agent-identity")
	targetStore, err := deviceidentity.CreateStore(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer targetStore.Close()
	a, err := deviceidentity.NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if err = targetStore.SaveAuthority(a); err != nil {
		t.Fatal(err)
	}
	agentStore, err := deviceidentity.CreateStore(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer agentStore.Close()
	agent, request, err := deviceidentity.NewAgent()
	if err != nil {
		t.Fatal(err)
	}
	if err = agentStore.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	fp, _ := request.Fingerprint()
	g, err := targetStore.IssueGrant(request, fp, connection.ID(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	bundle := a.Bundle()
	bf, _ := bundle.Fingerprint()
	if err = agentStore.InstallPairing(deviceidentity.Pairing{Version: deviceidentity.Version, Bundle: bundle, Grant: g, Fingerprint: bf}, bf); err != nil {
		t.Fatal(err)
	}
	service, err := New([]Route{{bundle.DeviceID, targetToken, agentToken}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(service)
	defer srv.Close()
	defer service.Close()
	wss := "wss" + strings.TrimPrefix(srv.URL, "https")
	caFile := filepath.Join(base, "outer-ca.pem")
	if err = os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	target := exec.CommandContext(ctx, binaries["local-probe-target"], "-store", targetPath, "-config", configPath, "-audit-dir", filepath.Join(base, "audit"), "-relay-endpoint", wss+"/v1/target", "-outer-ca-file", caFile)
	target.Env = append(os.Environ(), "LOCAL_PROBE_RELAY_TARGET_TOKEN="+targetToken)
	if err = target.Start(); err != nil {
		t.Fatal(err)
	}
	targetDone := make(chan error, 1)
	go func() { targetDone <- target.Wait() }()
	defer func() {
		cancel()
		select {
		case <-targetDone:
		case <-time.After(2 * time.Second):
			t.Error("target did not exit")
		}
	}()
	registered := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		service.mu.Lock()
		for _, slot := range service.routes[0].slots {
			if slot.ws != nil {
				registered = true
			}
		}
		service.mu.Unlock()
		if registered {
			break
		}
		select {
		case err := <-targetDone:
			t.Fatalf("target exited: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !registered {
		t.Fatal("real target did not connect")
	}
	agentCommand := exec.CommandContext(ctx, binaries["local-probe-agent"], "-store", agentPath, "-transport", "relay", "-endpoint", wss+"/v1/agent", "-outer-ca-file", caFile)
	agentCommand.Env = append(os.Environ(), "LOCAL_PROBE_AGENT_ACCESS_TOKEN="+agentToken)
	client := mcp.NewClient(&mcp.Implementation{Name: "cli-test-native-agent", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: agentCommand, TerminateDuration: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_file", Arguments: map[string]any{"root_id": "workspace", "path": "evidence.txt", "max_bytes": 64}})
	if err != nil || result.IsError {
		t.Fatalf("real read failed: %v", err)
	}
	data, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(data), "real-cli-relay-evidence") {
		t.Fatal("real file bytes missing")
	}
}
