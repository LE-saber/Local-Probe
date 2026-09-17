package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/audit"
	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/environment"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/rootfs"
	"github.com/LE-saber/Local-Probe/internal/search"
	"github.com/LE-saber/Local-Probe/internal/workspacesnapshot"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	testTokenOne = "local-probe-test-token-one-0123456789"
	testTokenTwo = "local-probe-test-token-two-0123456789"
)

func TestStreamableHTTPClientLifecycleAndReadTools(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	defer fixture.close()
	server := newTestServer(t, fixture, []Credential{
		{ConnectionID: "connection-one", Token: testTokenOne},
		{ConnectionID: "connection-two", Token: testTokenTwo},
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := &testTokenTransport{token: testTokenOne}
	client := mcp.NewClient(&mcp.Implementation{Name: "local-probe-test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL,
		HTTPClient:           &http.Client{Transport: transport},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer session.Close()

	// The MCP 2026-07-28 protocol removes the protocol-level ping method;
	// exercise the legacy ping only when negotiation selected that protocol.
	if session.InitializeResult().ProtocolVersion < modernMCPProtocolVersion {
		if err := session.Ping(ctx, nil); err != nil {
			t.Fatalf("Ping() error = %v", err)
		}
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	gotTools := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		gotTools = append(gotTools, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
			t.Errorf("tool %q missing read-only annotations: %#v", tool.Name, tool.Annotations)
		}
	}
	sort.Strings(gotTools)
	wantTools := []string{ToolBatchRead, ToolPing, ToolReadFile, ToolServerInfo}
	if len(gotTools) != len(wantTools) {
		t.Fatalf("tools = %v, want %v", gotTools, wantTools)
	}
	for i := range wantTools {
		if gotTools[i] != wantTools[i] {
			t.Fatalf("tools = %v, want %v", gotTools, wantTools)
		}
	}

	infoResult := callTool(t, ctx, session, ToolServerInfo, map[string]any{})
	var info serverInfoOutput
	decodeToolJSON(t, infoResult, &info)
	if info.Server != ServerName || info.ConnectionID != "connection-one" || !info.ReadOnly {
		t.Fatalf("server_info = %#v", info)
	}
	if !reflect.DeepEqual(info.RootIDs, []string{"workspace"}) {
		t.Fatalf("server_info root ids = %v", info.RootIDs)
	}

	readResult := callTool(t, ctx, session, ToolReadFile, map[string]any{
		"root_id":   "workspace",
		"path":      "hello.txt",
		"offset":    0,
		"max_bytes": 64,
	})
	var read readFileOutput
	decodeToolJSON(t, readResult, &read)
	if read.Item.Content != "hello from Local-Probe\n" || read.Item.Error != nil || read.RequestID == "" {
		t.Fatalf("read_file = %#v", read)
	}

	batchResult := callTool(t, ctx, session, ToolBatchRead, map[string]any{
		"requests": []any{
			map[string]any{"root_id": "workspace", "path": "hello.txt", "max_bytes": 6},
			map[string]any{"root_id": "workspace", "path": "missing.txt", "max_bytes": 6},
		},
	})
	var batch batchReadOutput
	decodeToolJSON(t, batchResult, &batch)
	if len(batch.Items) != 2 || batch.Items[0].Content != "hello " || batch.Items[1].Error == nil || batch.Failed != 1 {
		t.Fatalf("batch_read = %#v", batch)
	}
}

func TestSearchToolsAreMCPScopedAndUseStructuredResults(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	fixture.profileOneTools = []string{ToolServerInfo, ToolPing, ToolReadFile, ToolBatchRead, ToolListDirectory, ToolFindFiles, ToolSearchText, ToolTreeDirectory}
	if err := os.MkdirAll(fixture.root+"/src/nested", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.root+"/src/main.go", []byte("package main\nneedle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.root+"/src/nested/util.go", []byte("package nested\nneedle in utf8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.root+"/ignored.tmp", []byte("needle ignored by the profile test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, fixture, []Credential{{ConnectionID: "connection-one", Token: testTokenOne}})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "search-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL,
		HTTPClient:           &http.Client{Transport: &testTokenTransport{token: testTokenOne}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 8 {
		t.Fatalf("tools = %d, want 8", len(tools.Tools))
	}
	listing := callTool(t, ctx, session, ToolListDirectory, map[string]any{"root_id": "workspace", "page_size": 32})
	var list struct {
		RequestID string          `json:"request_id"`
		Entries   []search.Entry  `json:"entries"`
		Coverage  search.Coverage `json:"coverage"`
	}
	decodeToolJSON(t, listing, &list)
	if list.RequestID == "" || len(list.Entries) < 2 {
		t.Fatalf("list_directory = %#v", list)
	}
	found := callTool(t, ctx, session, ToolFindFiles, map[string]any{"root_id": "workspace", "pattern": "**/*.go", "page_size": 16})
	var find struct {
		Entries []search.Entry `json:"entries"`
	}
	decodeToolJSON(t, found, &find)
	if len(find.Entries) != 2 {
		t.Fatalf("find_files = %#v", find)
	}
	textResult := callTool(t, ctx, session, ToolSearchText, map[string]any{"root_id": "workspace", "query": "needle", "page_size": 16})
	var text struct {
		Matches []search.Match `json:"matches"`
	}
	decodeToolJSON(t, textResult, &text)
	if len(text.Matches) != 3 {
		t.Fatalf("search_text = %#v", text)
	}
	treeResult := callTool(t, ctx, session, ToolTreeDirectory, map[string]any{"root_id": "workspace", "page_size": 16, "max_depth": 1})
	var tree struct {
		Entries  []search.TreeEntry `json:"entries"`
		Coverage search.Coverage    `json:"coverage"`
	}
	decodeToolJSON(t, treeResult, &tree)
	if len(tree.Entries) < 2 || tree.Entries[0].Path != "" || tree.Entries[0].Depth != 0 || tree.Entries[1].Path != "hello.txt" || tree.Entries[1].Depth != 1 {
		t.Fatalf("tree_directory = %#v", tree)
	}
}

func TestR2ReadRangesAndWorkspaceSnapshotAreScoped(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	fixture.profileOneTools = []string{ToolServerInfo, ToolPing, ToolReadFile, ToolBatchRead, ToolWorkspaceSnapshot}
	fixture.profileTwoTools = []string{ToolServerInfo}
	if err := os.MkdirAll(filepath.Join(fixture.root, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	lineData := []byte("one\r\n你好\r\nthree\r\nlast")
	if err := os.WriteFile(filepath.Join(fixture.root, "lines.txt"), lineData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "go.mod"), []byte("module example.test\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "src", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := newTestServer(t, fixture, []Credential{
		{ConnectionID: "connection-one", Token: testTokenOne},
		{ConnectionID: "connection-two", Token: testTokenTwo},
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "r2-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL, HTTPClient: &http.Client{Transport: &testTokenTransport{token: testTokenOne}},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !containsTool(tools.Tools, ToolWorkspaceSnapshot) {
		t.Fatalf("tools/list did not expose workspace_snapshot: %v", toolNames(tools.Tools))
	}

	lineResult := callTool(t, ctx, session, ToolReadFile, map[string]any{
		"root_id": "workspace", "path": "lines.txt", "max_bytes": 128,
		"range": map[string]any{"kind": "lines", "start_line": 2, "max_lines": 2},
	})
	var lineOutput readFileOutput
	decodeToolJSON(t, lineResult, &lineOutput)
	if lineOutput.Item.Content != "你好\r\nthree\r\n" || lineOutput.Item.RangeKind != "lines" ||
		lineOutput.Item.StartLine != 2 || lineOutput.Item.EndLine != 3 ||
		!lineOutput.Item.Complete || lineOutput.Item.Error != nil || lineOutput.Item.ScannedBytes == 0 {
		t.Fatalf("line range = %#v", lineOutput.Item)
	}

	tailResult := callTool(t, ctx, session, ToolReadFile, map[string]any{
		"root_id": "workspace", "path": "lines.txt", "max_bytes": 128,
		"range": map[string]any{"kind": "tail", "tail_lines": 2},
	})
	var tailOutput readFileOutput
	decodeToolJSON(t, tailResult, &tailOutput)
	if tailOutput.Item.Content != "three\r\nlast" || tailOutput.Item.RangeKind != "tail" ||
		!tailOutput.Item.Complete || tailOutput.Item.Error != nil || tailOutput.Item.ScannedBytes == 0 {
		t.Fatalf("tail range = %#v", tailOutput.Item)
	}

	batchResult := callTool(t, ctx, session, ToolBatchRead, map[string]any{
		"requests": []any{
			map[string]any{"root_id": "workspace", "path": "lines.txt", "max_bytes": 5},
			map[string]any{"root_id": "workspace", "path": "lines.txt", "max_bytes": 128,
				"range": map[string]any{"kind": "tail", "tail_lines": 1}},
		},
	})
	var batch batchReadOutput
	decodeToolJSON(t, batchResult, &batch)
	if len(batch.Items) != 2 || batch.Items[0].Content != "one\r\n" || batch.Items[1].Content != "last" ||
		batch.Failed != 0 || batch.Complete || batch.Items[0].Complete || !batch.Items[1].Complete || batch.ScannedBytes == 0 {
		t.Fatalf("ranged batch_read = %#v", batch)
	}

	snapshotResult := callTool(t, ctx, session, ToolWorkspaceSnapshot, map[string]any{
		"root_id": "workspace", "max_depth": 2, "max_entries": 64,
	})
	var snapshot workspaceSnapshotOutput
	decodeToolJSON(t, snapshotResult, &snapshot)
	if snapshot.RequestID == "" || snapshot.RootID != "workspace" || !snapshot.Coverage.Complete ||
		!containsSnapshotPath(snapshot.ManifestCandidates, "go.mod") || !containsSnapshotEvidence(snapshot.EvidencePaths, "go.mod") {
		t.Fatalf("workspace_snapshot = %#v", snapshot)
	}
	if resultContainsString(snapshotResult, fixture.root) || resultContainsString(snapshotResult, filepath.Join(fixture.root, "lines.txt")) {
		t.Fatalf("workspace_snapshot exposed an absolute path: %s", structuredResultBytes(snapshotResult))
	}

	badRange := callTool(t, ctx, session, ToolReadFile, map[string]any{
		"root_id": "workspace", "path": "lines.txt", "range": map[string]any{
			"kind": "lines", "start_line": 1, "max_lines": 1, "unexpected": "nope",
		},
	})
	if !badRange.IsError || resultContainsString(badRange, fixture.root) {
		t.Fatalf("invalid range was accepted or leaked a path: %#v", badRange)
	}

	secondClient := mcp.NewClient(&mcp.Implementation{Name: "r2-scoped-client", Version: "test"}, nil)
	secondSession, err := secondClient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL, HTTPClient: &http.Client{Transport: &testTokenTransport{token: testTokenTwo}},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer secondSession.Close()
	secondSnapshot := callTool(t, ctx, secondSession, ToolWorkspaceSnapshot, map[string]any{"root_id": "workspace"})
	if !secondSnapshot.IsError || !strings.Contains(toolText(t, secondSnapshot), "denied") {
		t.Fatalf("workspace_snapshot escaped profile allowlist: %#v", secondSnapshot)
	}
}

func TestWorkspaceSnapshotBudgetErrorIsStable(t *testing.T) {
	result := workspaceSnapshotErrorResult(workspacesnapshot.ErrBudgetExceeded)
	if !result.IsError {
		t.Fatal("budget error result is not marked as an error")
	}
	var envelope struct {
		SchemaVersion string `json:"schema_version"`
		Error         struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(toolText(t, result)), &envelope); err != nil {
		t.Fatalf("decode budget error result: %v", err)
	}
	if envelope.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %q, want %q", envelope.SchemaVersion, SchemaVersion)
	}
	if envelope.Error.Code != "budget_exhausted" {
		t.Fatalf("error code = %q, want budget_exhausted", envelope.Error.Code)
	}
	if envelope.Error.Message != "workspace snapshot exceeded its bounded output budget" {
		t.Fatalf("error message = %q, want stable redacted message", envelope.Error.Message)
	}
	if strings.Contains(envelope.Error.Message, "workspace snapshot output budget exceeded") {
		t.Fatal("budget error leaked the internal error text")
	}
}

func containsTool(tools []*mcp.Tool, name string) bool {
	for _, tool := range tools {
		if tool != nil && tool.Name == name {
			return true
		}
	}
	return false
}

func toolNames(tools []*mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool != nil {
			names = append(names, tool.Name)
		}
	}
	sort.Strings(names)
	return names
}

func containsSnapshotPath(entries []workspacesnapshot.ManifestCandidate, path string) bool {
	for _, entry := range entries {
		if entry.Path == path {
			return true
		}
	}
	return false
}

func containsSnapshotEvidence(entries []workspacesnapshot.EvidencePath, path string) bool {
	for _, entry := range entries {
		if entry.Path == path {
			return true
		}
	}
	return false
}

func TestEnvironmentToolsAreScopedAndPathFree(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	candidatePath := filepath.Join(fixture.root, "git-probe")
	if err := os.WriteFile(candidatePath, []byte("not a command"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.environmentTools = []environment.ToolSpec{{
		ID:             "git",
		CandidateFiles: []string{candidatePath},
	}}
	fixture.profileOneTools = []string{ToolServerInfo, ToolGetEnvironment, ToolDiscoverTools}
	fixture.profileTwoTools = []string{ToolServerInfo}
	server := newTestServer(t, fixture, []Credential{
		{ConnectionID: "connection-one", Token: testTokenOne},
		{ConnectionID: "connection-two", Token: testTokenTwo},
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "environment-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL,
		HTTPClient:           &http.Client{Transport: &testTokenTransport{token: testTokenOne}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	wantNames := []string{ToolDiscoverTools, ToolGetEnvironment, ToolServerInfo}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("environment tools/list = %v, want %v", names, wantNames)
	}

	infoResult := callTool(t, ctx, session, ToolServerInfo, map[string]any{})
	var info serverInfoOutput
	decodeToolJSON(t, infoResult, &info)
	if !containsString(info.Tools, ToolGetEnvironment) || !containsString(info.Tools, ToolDiscoverTools) {
		t.Fatalf("server_info tools = %v", info.Tools)
	}

	environmentResult := callTool(t, ctx, session, ToolGetEnvironment, map[string]any{})
	var environmentOutput getEnvironmentOutput
	decodeToolJSON(t, environmentResult, &environmentOutput)
	if environmentOutput.OS == "" || environmentOutput.Arch == "" || environmentOutput.RequestID == "" {
		t.Fatalf("get_environment = %#v", environmentOutput)
	}
	if resultContainsString(environmentResult, fixture.root) {
		t.Fatalf("get_environment exposed local root: %s", environmentResult.StructuredContent)
	}

	discoveryResult := callTool(t, ctx, session, ToolDiscoverTools, map[string]any{})
	var discoveryOutput discoverToolsOutput
	decodeToolJSON(t, discoveryResult, &discoveryOutput)
	if len(discoveryOutput.Tools) != 1 || discoveryOutput.Tools[0].ApprovedLogicalID != "git" || !discoveryOutput.Tools[0].Exists || discoveryOutput.Tools[0].CandidateCount != 1 {
		t.Fatalf("discover_tools = %#v", discoveryOutput)
	}
	if resultContainsString(discoveryResult, fixture.root) || bytes.Contains(structuredResultBytes(discoveryResult), []byte("candidate_paths")) || bytes.Contains(structuredResultBytes(discoveryResult), []byte("diagnostics_notice")) {
		t.Fatalf("discover_tools exposed local diagnostics: %s", discoveryResult.StructuredContent)
	}

	var fakeCalled bool
	server.discoverEnvironment = func(specs []environment.ToolSpec, options environment.DiscoveryOptions) ([]environment.ToolDiscoveryResult, error) {
		fakeCalled = true
		if options.LocalDiagnostics {
			t.Error("remote discovery enabled local diagnostics")
		}
		if len(specs) != 1 || specs[0].ID != "git" {
			t.Errorf("selected specs = %#v", specs)
		}
		return []environment.ToolDiscoveryResult{{
			ApprovedLogicalID: "git",
			Exists:            true,
			CandidateCount:    1,
			CandidatePaths:    []string{candidatePath},
			DiagnosticsNotice: environment.DiagnosticsNotice,
		}}, nil
	}
	fakeResult := callTool(t, ctx, session, ToolDiscoverTools, map[string]any{"logical_ids": []string{"git"}})
	if !fakeCalled || resultContainsString(fakeResult, fixture.root) || bytes.Contains(structuredResultBytes(fakeResult), []byte("candidate_paths")) || bytes.Contains(structuredResultBytes(fakeResult), []byte("diagnostics_notice")) {
		t.Fatalf("injected discovery was not redacted: %s", structuredResultBytes(fakeResult))
	}
	server.discoverEnvironment = environment.DiscoverTools

	server.environmentTools = nil
	emptyResult := callTool(t, ctx, session, ToolDiscoverTools, map[string]any{})
	var emptyOutput discoverToolsOutput
	decodeToolJSON(t, emptyResult, &emptyOutput)
	if len(emptyOutput.Tools) != 0 {
		t.Fatalf("discover_tools without configured tools = %#v", emptyOutput)
	}
	server.environmentTools = fixture.environmentTools

	badArguments := callTool(t, ctx, session, ToolDiscoverTools, map[string]any{"paths": []string{fixture.root}})
	if !badArguments.IsError || resultContainsString(badArguments, fixture.root) {
		t.Fatalf("discover_tools accepted path argument: %#v", badArguments)
	}

	unknown := callTool(t, ctx, session, ToolDiscoverTools, map[string]any{
		"logical_ids": []string{"not_configured"},
	})
	if !unknown.IsError || strings.Contains(toolText(t, unknown), "not_configured") || resultContainsString(unknown, fixture.root) {
		t.Fatalf("unknown logical ID response = %#v, text=%q", unknown, toolText(t, unknown))
	}

	secondClient := mcp.NewClient(&mcp.Implementation{Name: "environment-scoped-client", Version: "test"}, nil)
	secondSession, err := secondClient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL,
		HTTPClient:           &http.Client{Transport: &testTokenTransport{token: testTokenTwo}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer secondSession.Close()
	secondTools, err := secondSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondTools.Tools) != 1 || secondTools.Tools[0].Name != ToolServerInfo {
		t.Fatalf("second connection tools = %#v", secondTools.Tools)
	}
	denied := callTool(t, ctx, secondSession, ToolDiscoverTools, map[string]any{})
	if !denied.IsError || !strings.Contains(toolText(t, denied), `"code":"denied"`) {
		t.Fatalf("second connection discover_tools = %#v, text=%q", denied, toolText(t, denied))
	}
}

func TestStreamableHTTPProtocolCompatibility(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	server := newTestServer(t, fixture, []Credential{
		{ConnectionID: "connection-one", Token: testTokenOne},
		{ConnectionID: "connection-two", Token: testTokenTwo},
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	type wireEnvelope struct {
		JSONRPC string          `json:"jsonrpc"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	doPost := func(t *testing.T, token, method, version, sessionID, body string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, httpServer.URL, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set(LocalTokenHeader, token)
		if version != "" {
			req.Header.Set("Mcp-Protocol-Version", version)
			req.Header.Set("Mcp-Method", method)
		}
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return resp, payload
	}
	decode := func(t *testing.T, payload []byte) wireEnvelope {
		t.Helper()
		var envelope wireEnvelope
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatalf("decode JSON-RPC envelope: %v; body=%q", err, payload)
		}
		if envelope.JSONRPC != "2.0" {
			t.Fatalf("jsonrpc = %q, want 2.0; body=%q", envelope.JSONRPC, payload)
		}
		return envelope
	}
	assertJSON := func(t *testing.T, response *http.Response) {
		t.Helper()
		if mediaType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]); mediaType != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", response.Header.Get("Content-Type"))
		}
	}

	legacyInitialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"legacy-probe","version":"test"}}}`
	legacyResponse, legacyPayload := doPost(t, testTokenOne, "initialize", "", "", legacyInitialize)
	if legacyResponse.StatusCode != http.StatusOK {
		t.Fatalf("legacy initialize status = %d, want 200; body=%q", legacyResponse.StatusCode, legacyPayload)
	}
	assertJSON(t, legacyResponse)
	legacyEnvelope := decode(t, legacyPayload)
	if legacyEnvelope.Error != nil {
		t.Fatalf("legacy initialize error = %#v", legacyEnvelope.Error)
	}
	var legacyResult struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(legacyEnvelope.Result, &legacyResult); err != nil {
		t.Fatal(err)
	}
	if legacyResult.ProtocolVersion != "2025-06-18" {
		t.Fatalf("legacy protocolVersion = %q", legacyResult.ProtocolVersion)
	}
	legacySessionID := legacyResponse.Header.Get("Mcp-Session-Id")
	if legacySessionID == "" {
		t.Fatal("legacy initialize did not return Mcp-Session-Id")
	}

	initializedResponse, initializedPayload := doPost(t, testTokenOne, "notifications/initialized", "", legacySessionID, `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)
	if initializedResponse.StatusCode != http.StatusAccepted || len(initializedPayload) != 0 {
		t.Fatalf("legacy initialized response = %d body=%q, want 202 with no body", initializedResponse.StatusCode, initializedPayload)
	}
	legacyToolsResponse, legacyToolsPayload := doPost(t, testTokenOne, "tools/list", "", legacySessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if legacyToolsResponse.StatusCode != http.StatusOK {
		t.Fatalf("legacy tools/list status = %d; body=%q", legacyToolsResponse.StatusCode, legacyToolsPayload)
	}
	assertJSON(t, legacyToolsResponse)
	legacyToolsEnvelope := decode(t, legacyToolsPayload)
	var legacyTools struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(legacyToolsEnvelope.Result, &legacyTools); err != nil {
		t.Fatal(err)
	}
	if len(legacyTools.Tools) != 4 {
		t.Fatalf("legacy tools/list returned %d tools, want 4", len(legacyTools.Tools))
	}
	wrongIdentityResponse, _ := doPost(t, testTokenTwo, "tools/list", "", legacySessionID, `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`)
	if wrongIdentityResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("legacy session with another token status = %d, want 403", wrongIdentityResponse.StatusCode)
	}
	deleteRequest, err := http.NewRequest(http.MethodDelete, httpServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleteRequest.Header.Set(LocalTokenHeader, testTokenOne)
	deleteRequest.Header.Set("Mcp-Session-Id", legacySessionID)
	deleteResponse, err := http.DefaultClient.Do(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	deleteResponse.Body.Close()
	if deleteResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("legacy DELETE status = %d, want 204", deleteResponse.StatusCode)
	}

	modernDiscover := `{"jsonrpc":"2.0","id":4,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"modern-probe","version":"test"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	modernResponse, modernPayload := doPost(t, testTokenOne, "server/discover", modernMCPProtocolVersion, "", modernDiscover)
	if modernResponse.StatusCode != http.StatusOK {
		t.Fatalf("modern discover status = %d; body=%q", modernResponse.StatusCode, modernPayload)
	}
	assertJSON(t, modernResponse)
	if modernResponse.Header.Get("Mcp-Session-Id") != "" {
		t.Fatal("modern discover unexpectedly returned a session ID")
	}
	modernEnvelope := decode(t, modernPayload)
	var discovery struct {
		SupportedVersions []string `json:"supportedVersions"`
	}
	if err := json.Unmarshal(modernEnvelope.Result, &discovery); err != nil {
		t.Fatal(err)
	}
	if len(discovery.SupportedVersions) == 0 || discovery.SupportedVersions[0] != modernMCPProtocolVersion {
		t.Fatalf("modern supportedVersions = %v, want %q first", discovery.SupportedVersions, modernMCPProtocolVersion)
	}
	modernTools := `{"jsonrpc":"2.0","id":5,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"modern-probe","version":"test"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	modernToolsResponse, modernToolsPayload := doPost(t, testTokenOne, "tools/list", modernMCPProtocolVersion, "", modernTools)
	if modernToolsResponse.StatusCode != http.StatusOK {
		t.Fatalf("modern tools/list status = %d; body=%q", modernToolsResponse.StatusCode, modernToolsPayload)
	}
	assertJSON(t, modernToolsResponse)
	modernToolsEnvelope := decode(t, modernToolsPayload)
	var modernToolsResult struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(modernToolsEnvelope.Result, &modernToolsResult); err != nil {
		t.Fatal(err)
	}
	if len(modernToolsResult.Tools) != 4 {
		t.Fatalf("modern tools/list returned %d tools, want 4", len(modernToolsResult.Tools))
	}
	modernGet, err := http.NewRequest(http.MethodGet, httpServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	modernGet.Header.Set(LocalTokenHeader, testTokenOne)
	modernGet.Header.Set("Mcp-Protocol-Version", modernMCPProtocolVersion)
	modernGet.Header.Set("Accept", "text/event-stream")
	modernGetResponse, err := http.DefaultClient.Do(modernGet)
	if err != nil {
		t.Fatal(err)
	}
	modernGetResponse.Body.Close()
	if modernGetResponse.StatusCode != http.StatusMethodNotAllowed || modernGetResponse.Header.Get("Allow") != http.MethodPost {
		t.Fatalf("modern GET response = %d Allow=%q, want 405 Allow POST", modernGetResponse.StatusCode, modernGetResponse.Header.Get("Allow"))
	}
	modernDelete, err := http.NewRequest(http.MethodDelete, httpServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	modernDelete.Header.Set(LocalTokenHeader, testTokenOne)
	modernDelete.Header.Set("Mcp-Protocol-Version", modernMCPProtocolVersion)
	modernDeleteResponse, err := http.DefaultClient.Do(modernDelete)
	if err != nil {
		t.Fatal(err)
	}
	modernDeleteResponse.Body.Close()
	if modernDeleteResponse.StatusCode != http.StatusMethodNotAllowed || modernDeleteResponse.Header.Get("Allow") != http.MethodPost {
		t.Fatalf("modern DELETE response = %d Allow=%q, want 405 Allow POST", modernDeleteResponse.StatusCode, modernDeleteResponse.Header.Get("Allow"))
	}

	invalidModernResponse, invalidModernPayload := doPost(t, testTokenOne, "server/discover", modernMCPProtocolVersion, "", `{"jsonrpc":"2.0","id":6,"method":"server/discover","params":{}}`)
	if invalidModernResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid modern discover status = %d, want 400; body=%q", invalidModernResponse.StatusCode, invalidModernPayload)
	}
	invalidEnvelope := decode(t, invalidModernPayload)
	if invalidEnvelope.Error == nil {
		t.Fatalf("invalid modern discover returned no JSON-RPC error; body=%q", invalidModernPayload)
	}
}

func TestToolListAndCallsAreConnectionScoped(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	fixture.profileTwoTools = []string{ToolServerInfo, ToolPing}
	server := newTestServer(t, fixture, []Credential{
		{ConnectionID: "connection-one", Token: testTokenOne},
		{ConnectionID: "connection-two", Token: testTokenTwo},
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := &testTokenTransport{token: testTokenTwo}
	client := mcp.NewClient(&mcp.Implementation{Name: "scoped-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL,
		HTTPClient:           &http.Client{Transport: transport},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	filteredNames := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		filteredNames = append(filteredNames, tool.Name)
	}
	sort.Strings(filteredNames)
	wantFilteredNames := []string{ToolPing, ToolServerInfo}
	if len(filteredNames) != len(wantFilteredNames) || filteredNames[0] != wantFilteredNames[0] || filteredNames[1] != wantFilteredNames[1] {
		t.Fatalf("filtered tools = %v", filteredNames)
	}
	disallowed := callTool(t, ctx, session, ToolReadFile, map[string]any{
		"root_id": "workspace",
		"path":    "hello.txt",
	})
	if !disallowed.IsError || toolText(t, disallowed) == "" {
		t.Fatalf("disallowed call = %#v", disallowed)
	}

	// Modern requests are sessionless, so each request is authenticated against
	// its current bearer token rather than reusing a stateful session identity.
	transport.setToken(testTokenOne)
	infoResult := callTool(t, ctx, session, ToolServerInfo, map[string]any{})
	var info serverInfoOutput
	decodeToolJSON(t, infoResult, &info)
	if info.ConnectionID != "connection-one" {
		t.Fatalf("modern request used connection %q after token switch, want connection-one", info.ConnectionID)
	}
}

func TestAuthenticationAndCrossOriginProtection(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	server := newTestServer(t, fixture, []Credential{{ConnectionID: "connection-one", Token: testTokenOne}})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	newRequest := func(token string) *http.Request {
		req, err := http.NewRequest(http.MethodPost, httpServer.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set(LocalTokenHeader, token)
		}
		return req
	}
	for _, test := range []struct {
		name   string
		token  string
		status int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "wrong", token: "wrong-token-with-enough-length", status: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			resp, err := http.DefaultClient.Do(newRequest(test.token))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, test.status)
			}
			body, _ := io.ReadAll(resp.Body)
			if string(body) == test.token && test.token != "" {
				t.Fatal("authentication response echoed the token")
			}
		})
	}

	valid := newRequest(testTokenOne)
	valid.Header.Set("Origin", "https://attacker.example")
	valid.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(valid)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestResolveToken(t *testing.T) {
	t.Setenv("LOCAL_PROBE_TEST_TOKEN", testTokenOne)
	got, err := ResolveToken("LOCAL_PROBE_TEST_TOKEN", "")
	if err != nil || got != testTokenOne {
		t.Fatalf("ResolveToken(env) = %q, %v", got, err)
	}
	file := t.TempDir() + "/token.txt"
	if err := os.WriteFile(file, []byte(testTokenTwo+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = ResolveToken("", file)
	if err != nil || got != testTokenTwo {
		t.Fatalf("ResolveToken(file) = %q, %v", got, err)
	}
	if _, err := ResolveToken("LOCAL_PROBE_TEST_TOKEN", file); err == nil {
		t.Fatal("ResolveToken() accepted two sources")
	}
}

func TestAuditMCPCallsContainActionOnly(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	fixture.profileOneTools = []string{ToolServerInfo, ToolPing, ToolReadFile, ToolBatchRead, ToolWorkspaceSnapshot}
	auditDir := t.TempDir()
	sink, err := audit.New(audit.Config{Directory: auditDir, MaxFileBytes: 64 << 10, MaxFiles: 4, QueueSize: 64, InstanceID: "mcp-audit-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	fixture.audit = sink
	server := newTestServer(t, fixture, []Credential{{ConnectionID: "connection-one", Token: testTokenOne}})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "audit-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL, HTTPClient: &http.Client{Transport: &testTokenTransport{token: testTokenOne}},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}
	callTool(t, ctx, session, ToolPing, map[string]any{"path": "sentinel-path-query-token"})
	callTool(t, ctx, session, ToolWorkspaceSnapshot, map[string]any{"root_id": "workspace", "max_entries": 1})
	invalid := callTool(t, ctx, session, ToolReadFile, map[string]any{
		"path":       "sentinel-invalid-path",
		"unexpected": "sentinel-invalid-argument",
	})
	if !invalid.IsError {
		t.Fatalf("invalid read arguments unexpectedly succeeded: %#v", invalid)
	}
	callTool(t, ctx, session, "unknown_tool", map[string]any{"query": "sentinel-query", "token": "sentinel-token"})
	callTool(t, ctx, session, "C:/private/sentinel-tool", map[string]any{"query": "sentinel-unknown-tool"})
	if err := sink.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	data := bytes.Join(readAuditFiles(t, auditDir), []byte{'\n'})
	if !bytes.Contains(data, []byte(`"action":"tools/list"`)) || !bytes.Contains(data, []byte(`"action":"ping"`)) || !bytes.Contains(data, []byte(`"action":"workspace_snapshot"`)) {
		t.Fatalf("audit actions missing: %s", data)
	}
	if !bytes.Contains(data, []byte(`"outcome":"rejected"`)) || !bytes.Contains(data, []byte(`"error_code":"denied"`)) {
		t.Fatalf("audit error outcomes missing: %s", data)
	}
	if !bytes.Contains(data, []byte(`"error_code":"invalid_request"`)) {
		t.Fatalf("audit did not preserve invalid_request code: %s", data)
	}
	for _, action := range []string{`"action":"unknown_tool"`, `"action":"C:/private/sentinel-tool"`} {
		if bytes.Contains(data, []byte(action)) {
			t.Fatalf("audit copied unregistered tool name %q: %s", action, data)
		}
	}
	for _, sentinel := range []string{"sentinel-path-query-token", "sentinel-query", "sentinel-token", "path", "query", "arguments"} {
		if bytes.Contains(data, []byte(sentinel)) || (sentinel == "path" && bytes.Contains(data, []byte(`"path"`))) || (sentinel == "query" && bytes.Contains(data, []byte(`"query"`))) || (sentinel == "arguments" && bytes.Contains(data, []byte(`"arguments"`))) {
			t.Fatalf("audit contains request detail %q: %s", sentinel, data)
		}
	}
}

func TestAuditToolErrorDoesNotCopyResponseContent(t *testing.T) {
	result := &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: `{"schema_version":"local-probe.mcp.v1","error":{"code":"invalid_request","message":"C:\\private\\sentinel.txt"}}`}},
	}
	outcome, code := auditToolError(result)
	if outcome != audit.OutcomeFailed || code != "invalid_request" {
		t.Fatalf("auditToolError() = (%q, %q), want failed/invalid_request", outcome, code)
	}
	unknown := &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: `{"schema_version":"local-probe.mcp.v1","error":{"code":"sentinel-unknown","message":"sentinel-body"}}`}},
	}
	if outcome, code := auditToolError(unknown); outcome != audit.OutcomeFailed || code != "unavailable" {
		t.Fatalf("unknown auditToolError() = (%q, %q), want failed/unavailable", outcome, code)
	}
}

func TestAuditAuthRejectIsSynchronousAndRedacted(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	auditDir := t.TempDir()
	sink, err := audit.New(audit.Config{Directory: auditDir, MaxFileBytes: 64 << 10, MaxFiles: 2, QueueSize: 1, InstanceID: "auth-audit-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	fixture.audit = sink
	server := newTestServer(t, fixture, []Credential{{ConnectionID: "connection-one", Token: testTokenOne}})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	request, err := http.NewRequest(http.MethodPost, httpServer.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	badToken := "Bearer sentinel-auth-token-123456"
	request.Header.Set(LocalTokenHeader, badToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad auth status = %d", response.StatusCode)
	}
	forbiddenRequest, err := http.NewRequest(http.MethodPost, httpServer.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	forbiddenRequest.Header.Set(LocalTokenHeader, testTokenOne)
	forbiddenRequest.Header.Set("Origin", "https://attacker.example")
	forbiddenRequest.Header.Set("Sec-Fetch-Site", "cross-site")
	forbiddenResponse, err := http.DefaultClient.Do(forbiddenRequest)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenResponse.Body.Close()
	if forbiddenResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", forbiddenResponse.StatusCode)
	}
	data := bytes.Join(readAuditFiles(t, auditDir), []byte{'\n'})
	if bytes.Count(data, []byte(`"type":"auth.reject"`)) != 2 || !bytes.Contains(data, []byte(`"class":"security"`)) {
		t.Fatalf("synchronous auth rejection missing: %s", data)
	}
	if bytes.Contains(data, []byte(badToken)) || bytes.Contains(data, []byte("sentinel-auth-token-123456")) {
		t.Fatalf("auth token reached audit: %s", data)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
}

func readAuditFiles(t *testing.T, dir string) [][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	files := make([][]byte, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, data)
	}
	return files
}

type testFixture struct {
	store            *config.Store
	manager          *policy.Manager
	source           *rootfs.Source
	root             string
	environmentTools []environment.ToolSpec
	audit            audit.Recorder
	profileTwoTools  []string
	profileOneTools  []string
}

func newFixture(t *testing.T) *testFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(root+"/hello.txt", []byte("hello from Local-Probe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &testFixture{root: root}
}

func (f *testFixture) close() {
	if f.source != nil {
		_ = f.source.Close()
	}
}

func newTestServer(t *testing.T, fixture *testFixture, credentials []Credential) *Server {
	t.Helper()
	root, err := config.NewRoot("workspace", fixture.root, nil)
	if err != nil {
		t.Fatal(err)
	}
	allTools := fixture.profileOneTools
	if allTools == nil {
		allTools = []string{ToolServerInfo, ToolPing, ToolReadFile, ToolBatchRead}
	}
	profileOne, err := config.NewProfile("profile-one", []string{"workspace"}, allTools, nil)
	if err != nil {
		t.Fatal(err)
	}
	profileTwoTools := fixture.profileTwoTools
	if profileTwoTools == nil {
		profileTwoTools = allTools
	}
	profileTwo, err := config.NewProfile("profile-two", []string{"workspace"}, profileTwoTools, nil)
	if err != nil {
		t.Fatal(err)
	}
	credentialOne := config.NewCredentialRef("credential-one", "local_token")
	credentialTwo := config.NewCredentialRef("credential-two", "local_token")
	connectionOne := config.NewConnection("connection-one", "one", profileOne.ID(), credentialOne.ID(), true)
	connectionTwo := config.NewConnection("connection-two", "two", profileTwo.ID(), credentialTwo.ID(), true)
	cfg, err := config.New(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profileOne, profileTwo}, []config.Connection{connectionOne, connectionTwo}, []config.CredentialRef{credentialOne, credentialTwo})
	if err != nil {
		t.Fatal(err)
	}
	fixture.store, err = config.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fixture.manager, err = policy.NewManager(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.source, err = rootfs.New(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Manager: fixture.manager, Source: fixture.source, Credentials: credentials, EnvironmentTools: fixture.environmentTools, Audit: fixture.audit})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func resultContainsString(result *mcp.CallToolResult, value string) bool {
	encoded, err := json.Marshal(value)
	if err != nil || result == nil || result.StructuredContent == nil {
		return false
	}
	return bytes.Contains(structuredResultBytes(result), encoded)
}

func structuredResultBytes(result *mcp.CallToolResult) []byte {
	if result == nil || result.StructuredContent == nil {
		return nil
	}
	data, _ := json.Marshal(result.StructuredContent)
	return data
}

func callTool(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%q) error = %v", name, err)
	}
	return result
}

func toolText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

func decodeToolJSON(t *testing.T, result *mcp.CallToolResult, dst any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool returned error: %s", toolText(t, result))
	}
	data := []byte(toolText(t, result))
	if result.StructuredContent != nil {
		var err error
		data, err = json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatalf("marshal structured tool result: %v", err)
		}
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decode tool result: %v; content=%q", err, toolText(t, result))
	}
}

type testTokenTransport struct {
	mu    sync.RWMutex
	token string
}

func (t *testTokenTransport) setToken(token string) {
	t.mu.Lock()
	t.token = token
	t.mu.Unlock()
}

func (t *testTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.RLock()
	token := t.token
	t.mu.RUnlock()
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set(LocalTokenHeader, token)
	return http.DefaultTransport.RoundTrip(clone)
}
