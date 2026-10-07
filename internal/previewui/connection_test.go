package previewui

import (
	"context"
	"strings"
	"testing"
)

func TestConnectionDisplayMapsSafeFailureAndRemediation(t *testing.T) {
	state := ConnectionControlState{Result: ConnectionResult{
		Phase:           ConnectionFailed,
		Code:            ConnectionTokenRejected,
		PublicHost:      "mcp.example.test",
		TokenConfigured: true,
	}}
	display := state.Display()
	if display.StatusLabel != "连接失败" || display.PhaseLabel != "连接失败" {
		t.Fatalf("display status = %+v", display)
	}
	if display.Title != "Tunnel token 无效或已撤销" || !strings.Contains(display.Remediation, "重新生成") {
		t.Fatalf("display remediation = %+v", display)
	}
	if display.PublicHost != "mcp.example.test" || display.TokenStatus != "已配置" {
		t.Fatalf("safe connection details = %+v", display)
	}
}

func TestConnectionDisplayNeverEchoesUnsafeHost(t *testing.T) {
	state := ConnectionControlState{Result: ConnectionResult{
		Phase:      ConnectionFailed,
		Code:       ConnectionTokenMissing,
		PublicHost: "https://user:secret@example.test/path",
	}}
	display := state.Display()
	if display.PublicHost != "" {
		t.Fatalf("unsafe host leaked: %+v", display)
	}
	if strings.Contains(display.PublicHost, "secret") {
		t.Fatalf("secret leaked in public host: %+v", display)
	}
}

func TestConnectionButtonLabelsAndBusyState(t *testing.T) {
	if got := ConnectionButtonLabel(ConnectionControlState{}, true); got != "一键连接" {
		t.Fatalf("initial button = %q", got)
	}
	if got := ConnectionButtonLabel(ConnectionControlState{Busy: true}, true); got != "连接中…" {
		t.Fatalf("busy button = %q", got)
	}
	if got := ConnectionButtonLabel(ConnectionControlState{Result: ConnectionResult{Phase: ConnectionReady, Code: ConnectionOK}}, true); got != "重新连接" {
		t.Fatalf("ready button = %q", got)
	}
	if got := ConnectionButtonLabel(ConnectionControlState{}, false); got != "连接不可用" {
		t.Fatalf("missing connector button = %q", got)
	}
}

func TestConnectionProgressNormalization(t *testing.T) {
	result := NormalizeConnectionResult(ConnectionResult{
		Phase:      ConnectionReady,
		Code:       ConnectionCode("unexpected value"),
		PublicHost: " mcp.example.test ",
	})
	if result.Code != ConnectionOK || result.PublicHost != "mcp.example.test" {
		t.Fatalf("normalized result = %+v", result)
	}
	for _, phase := range []ConnectionPhase{ConnectionPreflight, ConnectionStartingMCP, ConnectionStartingTunnel, ConnectionVerifying} {
		if got := (ConnectionControlState{Busy: true, Result: ConnectionResult{Phase: phase}}).Display().PhaseLabel; got == "未开始" {
			t.Fatalf("busy phase %q was not rendered", phase)
		}
	}
}

func TestConnectionProgressChangedCoalescesRepeatedHealthPolls(t *testing.T) {
	progress := ConnectionResult{
		Phase:           ConnectionVerifying,
		Code:            ConnectionCode(""),
		PublicHost:      "mcp.example.test",
		TokenConfigured: true,
	}
	if connectionProgressChanged(progress, progress) {
		t.Fatal("identical health-poll progress should not trigger a repaint")
	}
	if !connectionProgressChanged(progress, ConnectionResult{Phase: ConnectionReady, PublicHost: "mcp.example.test", TokenConfigured: true}) {
		t.Fatal("phase transition must remain visible")
	}
	if !connectionProgressChanged(progress, ConnectionResult{Phase: ConnectionVerifying, PublicHost: "other.example.test", TokenConfigured: true}) {
		t.Fatal("public-host change must remain visible")
	}
}

func TestConnectionDisplayMapsStopFailure(t *testing.T) {
	display := (ConnectionControlState{Result: ConnectionResult{
		Phase: ConnectionFailed,
		Code:  ConnectionStopFailed,
	}}).Display()
	if display.Title != "旧连接清理失败" || !strings.Contains(display.Remediation, "端口") {
		t.Fatalf("stop failure display = %+v", display)
	}
}

func TestConnectionDisplayDistinguishesOpenAITunnel(t *testing.T) {
	ready := (ConnectionControlState{Result: ConnectionResult{
		Phase: ConnectionReady, Code: ConnectionOK, Transport: "openai_runtime", TokenConfigured: true,
	}}).Display()
	if ready.Title != "官方 Tunnel 本机已就绪" || !strings.Contains(ready.Remediation, "ChatGPT workspace") {
		t.Fatalf("OpenAI ready display = %+v", ready)
	}
	failed := (ConnectionControlState{Result: ConnectionResult{
		Phase: ConnectionFailed, Code: ConnectionOpenAIKeyMissing, Transport: "openai_runtime",
	}}).Display()
	if !strings.Contains(failed.Title, "Runtime API key") || strings.Contains(failed.Detail, "Cloudflare") {
		t.Fatalf("OpenAI failure display = %+v", failed)
	}
}

type fakeConnector struct{}

func (fakeConnector) Connect(context.Context) ConnectionResult {
	return ConnectionResult{Phase: ConnectionReady, Code: ConnectionOK, PublicHost: "mcp.example.test", TokenConfigured: true}
}

func TestConnectorSeamIsMinimalAndTyped(t *testing.T) {
	var connector Connector = fakeConnector{}
	result := connector.Connect(context.Background())
	if result.Phase != ConnectionReady || result.Code != ConnectionOK {
		t.Fatalf("connector result = %+v", result)
	}
}
