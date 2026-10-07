package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/rootfs"
	"github.com/LE-saber/Local-Probe/internal/search"
	"github.com/LE-saber/Local-Probe/internal/workspaceadmin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This exercise uses the official MCP SDK client and server over httptest's
// local loopback listener. It validates FileStore-backed scope changes and
// fixture recreation; it does not launch or verify a Windows MCP child.
func TestDesktopPreviewFileStorePauseResumeInvalidatesOldCursor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	rootPath := filepath.Join(t.TempDir(), "authorized-fixture")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"alpha.txt": "temporary marker alpha\n",
		"beta.txt":  "temporary marker beta\n",
	} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configDir := filepath.Join(filepath.Dir(rootPath), "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}

	root, err := config.NewRoot("workspace", rootPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := config.NewWorkspaceRootMetadata("workspace", "Temporary fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfileWithWorkspaceRoots(
		"read_only", []string{"workspace"},
		[]string{ToolServerInfo, ToolReadFile, ToolListDirectory}, nil, nil,
		[]config.WorkspaceRootMetadata{metadata},
	)
	if err != nil {
		t.Fatal(err)
	}
	credentialRef := config.NewCredentialRef("test-token", "runtime")
	connection := config.NewConnection("desktop-preview-test", "Temporary SDK fixture", profile.ID(), credentialRef.ID(), true)
	cfg, err := config.New(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profile}, []config.Connection{connection}, []config.CredentialRef{credentialRef})
	if err != nil {
		t.Fatal(err)
	}

	fileStore, err := config.NewFileStore(filepath.Join(configDir, "local-probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	fileSnapshot, err := fileStore.Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := workspaceadmin.New(fileStore)
	if err != nil {
		t.Fatal(err)
	}
	memoryStore, err := config.NewStore(fileSnapshot.Config())
	if err != nil {
		t.Fatal(err)
	}
	var randomToken [32]byte
	if _, err := rand.Read(randomToken[:]); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(randomToken[:])
	var cursorKey [32]byte
	if _, err := rand.Read(cursorKey[:]); err != nil {
		t.Fatal(err)
	}

	current := startDesktopPreviewMCPFixture(t, ctx, memoryStore, token, cursorKey[:])
	firstPageResult := callTool(t, ctx, current.session, ToolListDirectory, map[string]any{
		"root_id": "workspace", "page_size": 1,
	})
	var firstPage search.ListDirectoryResult
	decodeToolJSON(t, firstPageResult, &firstPage)
	if len(firstPage.Entries) != 1 || firstPage.Continuation == "" {
		t.Fatalf("first directory page = %#v; want one entry and a continuation", firstPage)
	}
	secondPageResult := callTool(t, ctx, current.session, ToolListDirectory, map[string]any{
		"root_id": "workspace", "page_size": 1, "cursor": firstPage.Continuation,
	})
	var secondPage search.ListDirectoryResult
	decodeToolJSON(t, secondPageResult, &secondPage)
	if len(secondPage.Entries) != 1 || secondPage.Entries[0].Path == firstPage.Entries[0].Path {
		t.Fatalf("continued directory page = %#v; want the other fixture entry", secondPage)
	}

	paused := false
	pausedChange, err := admin.Update(ctx, connection.ID(), fileSnapshot.Revision(), "workspace", workspaceadmin.RootUpdate{Enabled: &paused})
	if err != nil {
		t.Fatalf("pause workspace: %v", err)
	}
	if len(pausedChange.Workspace.Roots) != 1 || pausedChange.Workspace.Roots[0].Enabled {
		t.Fatalf("paused workspace projection = %+v", pausedChange.Workspace.Roots)
	}
	fileSnapshot, err = fileStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memoryStore.Replace(fileSnapshot.Config()); err != nil {
		t.Fatalf("reload paused config into the runtime store: %v", err)
	}
	if _, err := current.manager.BindAuthenticated(connection.ID()); err == nil {
		t.Fatal("an empty paused root set was bound instead of remaining explicitly unauthorized")
	}
	pausedRead, pausedReadErr := current.session.CallTool(ctx, &mcp.CallToolParams{Name: ToolReadFile, Arguments: map[string]any{
		"root_id": "workspace", "path": "alpha.txt", "offset": 0, "max_bytes": 64,
	}})
	if pausedReadErr == nil && (pausedRead == nil || !pausedRead.IsError) {
		t.Fatal("old MCP fixture continued reading after the workspace scope was paused")
	}
	if err := current.close(); err != nil {
		t.Fatalf("close paused MCP fixture: %v", err)
	}

	resumed := true
	resumedChange, err := admin.Update(ctx, connection.ID(), fileSnapshot.Revision(), "workspace", workspaceadmin.RootUpdate{Enabled: &resumed})
	if err != nil {
		t.Fatalf("resume workspace: %v", err)
	}
	if len(resumedChange.Workspace.Roots) != 1 || !resumedChange.Workspace.Roots[0].Enabled {
		t.Fatalf("resumed workspace projection = %+v", resumedChange.Workspace.Roots)
	}
	fileSnapshot, err = fileStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memoryStore.Replace(fileSnapshot.Config()); err != nil {
		t.Fatalf("reload resumed config into the runtime store: %v", err)
	}

	resumedRuntime := startDesktopPreviewMCPFixture(t, ctx, memoryStore, token, cursorKey[:])
	oldCursorResult, oldCursorErr := resumedRuntime.session.CallTool(ctx, &mcp.CallToolParams{Name: ToolListDirectory, Arguments: map[string]any{
		"root_id": "workspace", "page_size": 1, "cursor": firstPage.Continuation,
	}})
	if oldCursorErr == nil && (oldCursorResult == nil || !oldCursorResult.IsError) {
		t.Fatal("cursor from before pause/resume was accepted by the restored scope")
	}
	readResult := callTool(t, ctx, resumedRuntime.session, ToolReadFile, map[string]any{
		"root_id": "workspace", "path": "alpha.txt", "offset": 0, "max_bytes": 64,
	})
	var read struct {
		Item struct {
			Content string `json:"content"`
		} `json:"item"`
	}
	decodeToolJSON(t, readResult, &read)
	if read.Item.Content != "temporary marker alpha\n" {
		t.Fatalf("read_file after resume returned %q", read.Item.Content)
	}
	if err := resumedRuntime.close(); err != nil {
		t.Fatalf("close resumed MCP fixture: %v", err)
	}
}

type desktopPreviewMCPFixture struct {
	session *mcp.ClientSession
	server  *httptest.Server
	source  *rootfs.Source
	manager *policy.Manager
	closed  bool
}

func startDesktopPreviewMCPFixture(t *testing.T, ctx context.Context, store *config.Store, token string, cursorKey []byte) *desktopPreviewMCPFixture {
	t.Helper()
	manager, err := policy.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	source, err := rootfs.New(store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{
		Manager:         manager,
		Source:          source,
		Credentials:     []Credential{{ConnectionID: "desktop-preview-test", Token: token}},
		SearchCursorKey: append([]byte(nil), cursorKey...),
	})
	if err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	if _, err := server.verifyToken(ctx, token, nil); err != nil {
		_ = source.Close()
		t.Fatalf("verify random test token: %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	client := mcp.NewClient(&mcp.Implementation{Name: "desktop-preview-integration-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL,
		HTTPClient:           &http.Client{Transport: &testTokenTransport{token: token}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		httpServer.Close()
		_ = source.Close()
		t.Fatalf("connect official MCP SDK client: %v", err)
	}
	fixture := &desktopPreviewMCPFixture{session: session, server: httpServer, source: source, manager: manager}
	t.Cleanup(func() {
		if !fixture.closed {
			_ = fixture.close()
		}
	})
	return fixture
}

func (f *desktopPreviewMCPFixture) close() error {
	if f == nil || f.closed {
		return nil
	}
	f.closed = true
	var closeErr error
	if f.session != nil {
		closeErr = f.session.Close()
	}
	if f.server != nil {
		f.server.Close()
	}
	if f.source != nil {
		if err := f.source.Close(); closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}
