# Local-Probe

Local-Probe 是面向 Windows 的本地 MCP 工具与桌面 GUI：让 ChatGPT 在明确授权的工作空间内发现、搜索和读取文件。当前版本为 **R11.3 Desktop Preview**，自有源码采用 [MIT](LICENSE)；它不是生产级 supervisor、安装器或签名稳定版。

## 桌面 GUI（推荐）

要求 Windows x64、WebView2 Runtime、PowerShell 7；从源码构建还需要 Go 1.25+（本机验证 Go 1.26.0）。官方 `cloudflared` / `tunnel-client` 需按所选通道自行准备，不随包分发。

```powershell
git clone --branch main git@github.com:LE-saber/Local-Probe.git
Set-Location .\Local-Probe
pwsh -NoLogo -NoProfile -File .\scripts\Build-LocalProbeDesktop.ps1
pwsh -NoLogo -NoProfile -File .\scripts\Start-LocalProbeDesktop.ps1
```

缺少配置时 GUI 仍可打开，不会自动授权磁盘或生成凭据。首次配置、两条通道和验证顺序见 [Desktop 操作说明](docs/DESKTOP_PREVIEW.zh-CN.md) 与 [部署说明](docs/DEPLOYMENT_PREVIEW.zh-CN.md)。现有配置保存在本机，切换 GUI 版本前从托盘退出旧实例，不只是关闭窗口。

- 概览与连接：结构化链路状态、新建/编辑/复用历史连接；当前 GUI 管理一个选定连接，不声称已支持多连接同时运行。
- 访问范围：文件树分区、目录浏览导入、授权暂停/移除、连接与目录级 deny/ignore 规则预览及离线保存。
- 活动与设置：按目录/连接方式/时间筛选；诊断导出、原生备份保存/恢复、主题/语言/字号和健康状态。
- 原生界面：PMv2 DPI 适配，页面/标题栏/右键菜单同步深浅主题；保留文本选择、复制及刷新。
- 开发者页仅提供配置与命令模板管理；**实际命令、脚本、构建执行及写文件未开放**，`execution_available=false`。完整后端任务进度见 [R11 记录](docs/R11_BACKEND_PROGRESS.zh-CN.md)。

Release 目前处于准备阶段，范围与已知限制见 [发布说明草稿](docs/RELEASE_NOTES_R11.3.zh-CN.md)。源码合并、可下载 Preview 与 production-ready 是不同状态。

## 5 分钟开始（Windows Preview）

当前 Preview 有两条互相独立的接入方式：

| 方式 | 本地端口 | 身份 | GUI 一键连接 |
| --- | ---: | --- | --- |
| Cloudflare Preview（推荐） | `127.0.0.1:8788` | Cloudflare Access JWT | 支持 |
| OpenAI Secure MCP Tunnel | `127.0.0.1:8787` | 本地 hop token + Platform Tunnel | 支持；需先完成独立凭据初始化 |

新用户建议先阅读 [Windows Preview 部署](docs/DEPLOYMENT_PREVIEW.zh-CN.md)，它包含依赖、配置文件、Cloudflare route、工作空间、故障处理和两条链路的完整顺序。

### 克隆和检查

旧原生 Preview 的独立构建入口也保留在 main：

```powershell
git clone --branch main git@github.com:LE-saber/Local-Probe.git
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
$authorizedRoot = (Resolve-Path 'C:\replace-with-folder-you-authorize').Path

pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Setup-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path `
  -AuthorizedRoot $authorizedRoot

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

Preview 的“一键连接”支持 Cloudflare 8788 或 OpenAI 8787 链路。OpenAI Secure MCP Tunnel 需先按 [OpenAI Tunnel 部署说明](docs/TUNNEL_SETUP.zh-CN.md) 完成外部凭据初始化；之后可交给 GUI 管理，或使用独立脚本。不要让两套启动方式同时管理相同的 origin/端口。本机 ready 不等于 ChatGPT Workspace/真实调用验收成功。

当前尚未完成或未声明：生产级 supervisor/service、真实 WFP/broker、代码签名、安装器、自动更新、Linux/macOS GUI、任意命令执行、写入工具、默认大规模索引、百万文件端到端证据和独立安全审查。`release_ready` 仍为 false。

## 文档索引

- [Windows Preview 部署](docs/DEPLOYMENT_PREVIEW.zh-CN.md)：陌生用户的完整部署和故障排查入口；
- [Desktop GUI 操作说明](docs/DESKTOP_PREVIEW.zh-CN.md)：新版界面、备份、规则与验证边界；
- [R11.3 发布说明草稿](docs/RELEASE_NOTES_R11.3.zh-CN.md)：预发布范围与未交付功能；
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

总体计划仍保存在 [MASTER_PLAN.zh-CN.md](docs/MASTER_PLAN.zh-CN.md)，但它是维护和决策记录，不是新用户的首要部署入口。自有代码使用 MIT，第三方许可证单独保留并随 Desktop 包提供 NOTICE；发布前仍需复核。
