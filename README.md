# Local-Probe

Local-Probe 是一个面向 Windows 的只读 MCP Preview：让 ChatGPT 在明确授权的本地工作空间内进行目录发现、文件检索、范围读取、有限环境探查和审计查看。当前交付是可构建的 Preview 源码，不是生产级 supervisor、安装器或签名发布包。

## 5 分钟开始（Windows Preview）

当前 Preview 有两条互相独立的接入方式：

| 方式 | 本地端口 | 身份 | GUI 一键连接 |
| --- | ---: | --- | --- |
| Cloudflare Preview（推荐） | `127.0.0.1:8788` | Cloudflare Access JWT | 支持 |
| OpenAI Secure MCP Tunnel | `127.0.0.1:8787` | 本地 hop token + Platform Tunnel | 不支持，使用独立脚本 |

新用户建议先阅读 [Windows Preview 部署](docs/DEPLOYMENT_PREVIEW.zh-CN.md)，它包含依赖、配置文件、Cloudflare route、工作空间、故障处理和两条链路的完整顺序。

### 克隆和检查

如果当前 Preview 仍在功能分支：

```powershell
git clone --branch feat/cloudflare-mcp-ingress https://github.com/LE-saber/Local-Probe.git
Set-Location .\Local-Probe
```

在仓库根目录运行安全环境检查。它不会打印或上传秘密：

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Test-LocalProbeEnvironment.ps1 `
  -RepoRoot (Get-Location).Path
```

要求 Windows x64、Git、PowerShell 和 Go 1.25 或更高版本；本轮使用 Go 1.26.0 验证。Go 模块首次构建需要网络或模块缓存。Cloudflare 方案还需要官方 `cloudflared.exe`，OpenAI 方案需要官方 `tunnel-client.exe`；第三方二进制不随源码提交。

### 初始化、构建和启动

推荐使用统一初始化入口：

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Setup-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path

pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Build-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path

pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Start-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path
```

初始化会创建被 Git 忽略的 `.runtime/`、仓库内被忽略的 `.tools\cloudflared.exe` 位置和仓库外仅供 Cloudflare token 使用的 `.secrets/`。Cloudflare token 只填写到仓库父目录的 `.secrets\cloudflared-tunnel-token.txt`；不要把 token、key、JWT 或 Cookie 写入配置、命令行、日志或 Git。

Preview 默认需要以下外部工具布局：

```text
<clone-root>\.tools\cloudflared.exe
```

构建产物位于被忽略的 `bin/`：`local-probe-preview.exe` 与 `local-probe-mcp.exe`。在 GUI Connections 页面点击“一键连接”后，Preview 会按固定顺序启动 8788 MCP、等待 loopback、启动 cloudflared、检查 `/ready` 和 HA connection。它只管理本次自己启动的子进程。

Cloudflare route 必须把公网 hostname 转发到 `http://127.0.0.1:8788`，并在 `.runtime\cloudflare-access.json` 中填写 issuer、JWKS、audience、JWT subject 映射和相同的 `public_hosts`。连接成功后，ChatGPT 使用 `https://<public-host>/mcp`，先调用 `initialize`、`tools/list`、`ping`，再进行只读文件操作。

## 当前能力

- `server_info`、`ping`、有界 bytes/lines/tail 读取和批量读取；
- 目录列表、文件查找、literal 文本检索、分页 tree；
- 受 profile/root/deny/ignore 约束的 workspace snapshot 和有限环境探查；
- Windows Preview 的 Overview、Connections、工作空间访问、Developer Rules 投影、Logs/Diagnostics、About；
- 工作空间文件夹输入/浏览、多选撤销授权、原子配置保存和 revision 并发保护；
- 托盘主界面/刷新/诊断/字号/退出菜单，二次脱敏且不覆盖已有文件的诊断导出；
- 结构化审计和受限的 Cloudflare Access/OpenAI Tunnel 接入脚本。

GPT 能访问什么，取决于 profile 的 roots、tools、deny/ignore 和只读策略。当前 Preview 不开放任意 shell、任意命令、写入、凭据展示或模型编辑配置。

## 重要边界

Preview 的“一键连接”只覆盖 Cloudflare 8788 链路。OpenAI Secure MCP Tunnel 是另一条 8787 链路，需使用 [OpenAI Tunnel 部署说明](docs/TUNNEL_SETUP.zh-CN.md) 的 `Initialize-TunnelClient.ps1`、`Start-LocalProbeMcp.ps1` 和 `Start-TunnelClient.ps1`；不要同时启动两条链路。

当前尚未完成或未声明：生产级 supervisor/service、真实 WFP/broker、代码签名、安装器、自动更新、Linux/macOS GUI、任意命令执行、写入工具、默认大规模索引、百万文件端到端证据和独立安全审查。`release_ready` 仍为 false。

## 文档索引

- [Windows Preview 部署](docs/DEPLOYMENT_PREVIEW.zh-CN.md)：陌生用户的完整部署和故障排查入口；
- [Preview 使用说明](docs/PREVIEW.zh-CN.md)：GUI、托盘、工作空间和诊断真实能力；
- [开发指南](docs/DEVELOPMENT.zh-CN.md)：代码地图、测试、提交和安全边界；
- [架构说明](docs/ARCHITECTURE.zh-CN.md)：MCP、两种 ingress、rootfs、Preview 和审计数据流；
- [Cloudflare Tunnel + Access](docs/CLOUDFLARE_TUNNEL_SETUP.zh-CN.md)：Cloudflare route、Access JWT 和 metrics 预检；
- [OpenAI Secure MCP Tunnel](docs/TUNNEL_SETUP.zh-CN.md)：官方 tunnel-client 的独立 8787 路径；
- [接口与工具契约](docs/TOOL_CONTRACTS.md)；
- [威胁模型](docs/THREAT_MODEL.md)；
- [实际实施状态与验证记录](docs/IMPLEMENTATION_STATUS.md)；
- [R9 发布检查清单](docs/R9_RELEASE_CHECKLIST.zh-CN.md)；
- [代码结构映射](.planning/codebase/STRUCTURE.md)。

## 开发和验证

```powershell
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
```

Windows Preview 构建：

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Build-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path
```

生成不含 token、运行配置和第三方工具的本地 Preview ZIP：

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Package-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path -Version R9-preview -Build
```

ZIP 是便于人工测试的未签名 Preview 包，不代表 `release_ready=true`。

发布前还需要环境检查、Preview packaging 检查、非 Quick acceptance、干净目录构建和实际 Windows/Tunnel/ChatGPT 分层验收。脚本存在或 CI workflow 存在不等于对应外部链路已经通过。

总体计划仍保存在 [MASTER_PLAN.zh-CN.md](docs/MASTER_PLAN.zh-CN.md)，但它是维护和决策记录，不是新用户的首要部署入口。仓库目前尚未选择对外发布许可证；正式发布前必须补充许可证和第三方通知审查。
