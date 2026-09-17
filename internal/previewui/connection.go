package previewui

import (
	"context"
	"strings"
)

// Connector is the deliberately small seam between the native Preview and a
// future local lifecycle owner. Connect must be idempotent: an already-ready
// connection may be reused, while a degraded connection may be restarted.
// Implementations must return a typed, secret-free result. They must not
// expose command lines, token values, raw process output, or arbitrary shell
// execution through this interface.
type Connector interface {
	Connect(context.Context) ConnectionResult
}

// Reconnector is optional. Preview uses it when a connector can distinguish
// an explicit reconnect from an idempotent connect. Keeping it optional lets a
// small backend implement only Connector while the UI remains usable.
type Reconnector interface {
	Reconnect(context.Context) ConnectionResult
}

// ProgressConnector is an optional extension for backends that can report
// typed phases while connecting. The callback may be invoked from the worker
// goroutine and must never contain raw error text or credentials.
type ProgressConnector interface {
	ConnectWithProgress(context.Context, func(ConnectionProgress)) ConnectionResult
}

// ProgressReconnector is optional and preserves typed progress during an
// explicit reconnect without forcing the UI to emulate lifecycle ownership.
type ProgressReconnector interface {
	ReconnectWithProgress(context.Context, func(ConnectionProgress)) ConnectionResult
}

type ConnectionPhase string

const (
	ConnectionIdle           ConnectionPhase = "idle"
	ConnectionPreflight      ConnectionPhase = "preflight"
	ConnectionStartingMCP    ConnectionPhase = "starting_local_mcp"
	ConnectionStartingTunnel ConnectionPhase = "starting_tunnel"
	ConnectionVerifying      ConnectionPhase = "verifying"
	ConnectionReady          ConnectionPhase = "ready"
	ConnectionFailed         ConnectionPhase = "failed"
)

type ConnectionCode string

const (
	ConnectionOK                 ConnectionCode = "ok"
	ConnectionConnectorMissing   ConnectionCode = "connector_unavailable"
	ConnectionTokenMissing       ConnectionCode = "token_missing"
	ConnectionTokenEmpty         ConnectionCode = "token_empty"
	ConnectionTokenInvalidFormat ConnectionCode = "token_invalid_format"
	ConnectionTokenRejected      ConnectionCode = "token_rejected"
	ConnectionCloudflaredMissing ConnectionCode = "cloudflared_missing"
	ConnectionMCPMissing         ConnectionCode = "mcp_binary_missing"
	ConnectionConfigMissing      ConnectionCode = "config_missing"
	ConnectionConfigInvalid      ConnectionCode = "config_invalid"
	ConnectionAccessInvalid      ConnectionCode = "access_config_invalid"
	ConnectionOriginStartFailed  ConnectionCode = "origin_start_failed"
	ConnectionOriginNotReady     ConnectionCode = "origin_not_ready"
	ConnectionPortInUse          ConnectionCode = "port_in_use"
	ConnectionEdgeUnreachable    ConnectionCode = "edge_unreachable"
	ConnectionTunnelNotReady     ConnectionCode = "tunnel_not_ready"
	ConnectionNoEdgeConnections  ConnectionCode = "tunnel_no_edge_connections"
	ConnectionDNSMismatch        ConnectionCode = "dns_route_mismatch"
	ConnectionTimeout            ConnectionCode = "connect_timeout"
	ConnectionStopFailed         ConnectionCode = "stop_failed"
	ConnectionUnknown            ConnectionCode = "unknown"
)

// ConnectionResult is the only data a lifecycle backend needs to hand to the
// Preview. PublicHost is deliberately limited to a public DNS name; token
// material is represented only by the configured boolean.
type ConnectionResult struct {
	Phase           ConnectionPhase
	Code            ConnectionCode
	PublicHost      string
	TokenConfigured bool
}

type ConnectionProgress struct {
	Phase           ConnectionPhase
	Code            ConnectionCode
	PublicHost      string
	TokenConfigured bool
}

// ConnectionControlState is local UI state, not a supervisor state machine.
// Busy disables the action button while the backend owns the attempt.
type ConnectionControlState struct {
	Busy   bool
	Result ConnectionResult
}

type ConnectionIssue struct {
	Code        ConnectionCode
	Title       string
	Detail      string
	Remediation string
	Healthy     bool
}

type ConnectionDisplay struct {
	PhaseLabel  string
	StatusLabel string
	Title       string
	Detail      string
	Remediation string
	PublicHost  string
	TokenStatus string
	Healthy     bool
	Busy        bool
}

// NormalizeConnectionResult is a presentation boundary. Unknown phases and
// codes are collapsed to stable safe values before they reach the painter.
func NormalizeConnectionResult(result ConnectionResult) ConnectionResult {
	result.Phase = normalizeConnectionPhase(result.Phase)
	result.Code = normalizeConnectionCode(result.Code)
	result.PublicHost = sanitizePublicHost(result.PublicHost)
	if result.Phase == ConnectionReady && result.Code == "" {
		result.Code = ConnectionOK
	}
	if result.Phase == ConnectionReady && result.Code != ConnectionOK {
		result.Code = ConnectionOK
	}
	if result.Phase == ConnectionIdle && result.Code != "" {
		result.Code = ""
	}
	return result
}

func (s ConnectionControlState) Display() ConnectionDisplay {
	result := NormalizeConnectionResult(s.Result)
	if s.Busy {
		result.Phase = normalizeConnectingPhase(result.Phase)
	}
	issue := ConnectionIssueFor(result.Code)
	phaseLabel := connectionPhaseLabel(result.Phase)
	if s.Busy {
		issue = ConnectionIssue{Code: result.Code, Title: "正在连接", Detail: "正在检查本地 MCP、Tunnel 和远端连接状态。", Remediation: "请稍候；连接过程中按钮会暂时禁用。"}
	}
	return ConnectionDisplay{
		PhaseLabel:  phaseLabel,
		StatusLabel: connectionStatusLabel(result, s.Busy),
		Title:       issue.Title,
		Detail:      issue.Detail,
		Remediation: issue.Remediation,
		PublicHost:  result.PublicHost,
		TokenStatus: tokenStatusLabel(result.TokenConfigured),
		Healthy:     issue.Healthy && !s.Busy,
		Busy:        s.Busy,
	}
}

func ConnectionIssueFor(code ConnectionCode) ConnectionIssue {
	switch normalizeConnectionCode(code) {
	case "":
		return ConnectionIssue{Title: "尚未连接", Detail: "Preview 尚未发起本地连接。", Remediation: "点击“一键连接”开始检查并启动本地 MCP 与 Tunnel。"}
	case ConnectionOK:
		return ConnectionIssue{Code: ConnectionOK, Title: "已连接", Detail: "本地 MCP 与 Tunnel 已通过连接检查。", Remediation: "网页端 GPT 现在可以尝试发现并调用本地工具。", Healthy: true}
	case ConnectionConnectorMissing:
		return ConnectionIssue{Code: ConnectionConnectorMissing, Title: "连接功能未接入", Detail: "当前 Preview 没有可用的本地连接器。", Remediation: "请使用包含连接后端的 Preview 构建，或检查应用启动配置。"}
	case ConnectionTokenMissing, ConnectionTokenEmpty:
		return ConnectionIssue{Code: normalizeConnectionCode(code), Title: "未填写 Tunnel token", Detail: "没有找到可用的 Cloudflare Tunnel token。", Remediation: "在项目旁的 .secrets\\cloudflared-tunnel-token.txt 填入 token 后重新连接；Preview 只显示配置状态，不会显示 token。"}
	case ConnectionTokenInvalidFormat:
		return ConnectionIssue{Code: ConnectionTokenInvalidFormat, Title: "Tunnel token 格式无效", Detail: "token 文件存在，但内容格式不符合 Cloudflare Tunnel 要求。", Remediation: "重新复制完整的 Tunnel token，保持单行内容，然后点击“重新连接”。"}
	case ConnectionTokenRejected:
		return ConnectionIssue{Code: ConnectionTokenRejected, Title: "Tunnel token 无效或已撤销", Detail: "Cloudflared 拒绝了当前 token。", Remediation: "到 Cloudflare 重新生成或复制有效 token，更新凭据后点击“重新连接”。"}
	case ConnectionCloudflaredMissing:
		return ConnectionIssue{Code: ConnectionCloudflaredMissing, Title: "未找到 cloudflared", Detail: "Tunnel 客户端不在 Preview 预期位置。", Remediation: "安装或恢复受支持的 cloudflared，再重新启动 Preview。"}
	case ConnectionMCPMissing:
		return ConnectionIssue{Code: ConnectionMCPMissing, Title: "未找到本地 MCP", Detail: "本地 MCP 可执行文件缺失。", Remediation: "重新构建 Preview 发行目录，确保同时包含 local-probe-mcp.exe。"}
	case ConnectionConfigMissing:
		return ConnectionIssue{Code: ConnectionConfigMissing, Title: "连接配置缺失", Detail: "Tunnel 或 Access 配置尚未完成。", Remediation: "先运行初始化配置并填写公开主机、来源地址和 Access 配置，再重新连接。"}
	case ConnectionConfigInvalid:
		return ConnectionIssue{Code: ConnectionConfigInvalid, Title: "连接配置无效", Detail: "连接配置无法通过安全校验。", Remediation: "检查配置字段和本地地址，修复后重新连接；不要把 token 写进配置文件。"}
	case ConnectionAccessInvalid:
		return ConnectionIssue{Code: ConnectionAccessInvalid, Title: "Access 配置无效", Detail: "Cloudflare Access 的 issuer、audience 或主体映射未通过校验。", Remediation: "检查 Cloudflare Access 配置和应用策略，确认当前登录主体被允许后重新连接。"}
	case ConnectionOriginStartFailed:
		return ConnectionIssue{Code: ConnectionOriginStartFailed, Title: "本地 MCP 启动失败", Detail: "Preview 无法启动本地 MCP 服务。", Remediation: "查看日志与诊断中的错误码，确认端口和配置没有被其他进程占用。"}
	case ConnectionOriginNotReady:
		return ConnectionIssue{Code: ConnectionOriginNotReady, Title: "本地 MCP 未就绪", Detail: "本地 MCP 已尝试启动，但健康检查未通过。", Remediation: "确认本地配置和 Access 校验，然后重新连接；不要只看进程是否存在。"}
	case ConnectionPortInUse:
		return ConnectionIssue{Code: ConnectionPortInUse, Title: "端口已被占用", Detail: "连接所需的本地端口无法绑定。", Remediation: "关闭占用端口的旧实例或修改受支持的本地端口配置，再重新连接。"}
	case ConnectionEdgeUnreachable:
		return ConnectionIssue{Code: ConnectionEdgeUnreachable, Title: "无法连接 Cloudflare 边缘", Detail: "cloudflared 无法建立到 Cloudflare 的外连通道。", Remediation: "检查网络、防火墙和代理，允许 cloudflared 使用 TCP 7844 或回退的 TCP 443。"}
	case ConnectionTunnelNotReady:
		return ConnectionIssue{Code: ConnectionTunnelNotReady, Title: "Tunnel 尚未就绪", Detail: "cloudflared 正在运行，但仍未报告可用连接。", Remediation: "等待几秒后重新连接；若持续失败，检查 cloudflared 日志和 Tunnel 状态。"}
	case ConnectionNoEdgeConnections:
		return ConnectionIssue{Code: ConnectionNoEdgeConnections, Title: "Tunnel 没有边缘连接", Detail: "本地指标可访问，但当前 Tunnel 没有活跃边缘连接。", Remediation: "检查网络、防火墙和 Tunnel 运行状态，然后点击“重新连接”。"}
	case ConnectionDNSMismatch:
		return ConnectionIssue{Code: ConnectionDNSMismatch, Title: "公开主机路由不匹配", Detail: "公开主机没有指向当前 Tunnel。", Remediation: "检查 Cloudflare DNS/Tunnel route，确认公开主机与当前 Tunnel 一致。"}
	case ConnectionTimeout:
		return ConnectionIssue{Code: ConnectionTimeout, Title: "连接检查超时", Detail: "本地服务或 Tunnel 在限定时间内未就绪。", Remediation: "检查日志与诊断，确认网络和端口可用后重新连接。"}
	case ConnectionStopFailed:
		return ConnectionIssue{Code: ConnectionStopFailed, Title: "旧连接清理失败", Detail: "Preview 未能完整停止上一次由它启动的连接进程。", Remediation: "关闭仍在运行的 Preview 实例，确认 8788/49300 端口已释放后再重新连接。"}
	default:
		return ConnectionIssue{Code: ConnectionUnknown, Title: "连接失败", Detail: "连接后端返回了未分类的安全错误。", Remediation: "打开日志与诊断查看错误码，修复对应问题后重新连接。"}
	}
}

func ConnectionButtonLabel(state ConnectionControlState, connectorAvailable bool) string {
	if state.Busy {
		return "连接中…"
	}
	if !connectorAvailable {
		return "连接不可用"
	}
	if NormalizeConnectionResult(state.Result).Phase == ConnectionReady {
		return "重新连接"
	}
	return "一键连接"
}

func tokenStatusLabel(configured bool) string {
	if configured {
		return "已配置"
	}
	return "未配置"
}

func connectionStatusLabel(result ConnectionResult, busy bool) string {
	if busy {
		return "连接中"
	}
	if NormalizeConnectionResult(result).Phase == ConnectionReady {
		return "已连接"
	}
	if NormalizeConnectionResult(result).Phase == ConnectionFailed {
		return "连接失败"
	}
	return "未连接"
}

func connectionPhaseLabel(phase ConnectionPhase) string {
	switch normalizeConnectionPhase(phase) {
	case ConnectionPreflight:
		return "预检本地配置"
	case ConnectionStartingMCP:
		return "启动本地 MCP"
	case ConnectionStartingTunnel:
		return "启动 Tunnel"
	case ConnectionVerifying:
		return "验证连接"
	case ConnectionReady:
		return "连接就绪"
	case ConnectionFailed:
		return "连接失败"
	default:
		return "未开始"
	}
}

func normalizeConnectingPhase(phase ConnectionPhase) ConnectionPhase {
	switch normalizeConnectionPhase(phase) {
	case ConnectionStartingMCP, ConnectionStartingTunnel, ConnectionVerifying, ConnectionPreflight:
		return normalizeConnectionPhase(phase)
	default:
		return ConnectionPreflight
	}
}

func normalizeConnectionPhase(phase ConnectionPhase) ConnectionPhase {
	switch phase {
	case ConnectionIdle, ConnectionPreflight, ConnectionStartingMCP, ConnectionStartingTunnel, ConnectionVerifying, ConnectionReady, ConnectionFailed:
		return phase
	default:
		return ConnectionIdle
	}
}

func normalizeConnectionCode(code ConnectionCode) ConnectionCode {
	value := strings.ToLower(string(code))
	if value == "" {
		return ""
	}
	if len(value) > 64 {
		return ConnectionUnknown
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return ConnectionUnknown
		}
	}
	return ConnectionCode(value)
}

func sanitizePublicHost(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 253 || strings.ContainsAny(value, "/\\:@?#\r\n\t") {
		return ""
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return ""
		}
	}
	return value
}
