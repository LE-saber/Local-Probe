# Local-Probe Windows Preview 部署

本文是面向第一次使用 Local-Probe 的 Windows 部署入口。它描述当前仓库中真实可用的 Preview 路径，不把尚未完成的生产 supervisor、安装器、签名、自动更新或任意命令执行包装成已实现功能。

## 先选一条接入方式

当前有两条互相独立的 MCP 接入链路。它们不能同时占用同一个本地服务，也不应把两套配置混用。

| 方案 | 本地端口 | 身份方式 | Preview 的“一键连接” | 适用场景 |
| --- | ---: | --- | --- | --- |
| Cloudflare Preview | `127.0.0.1:8788` | Cloudflare Access JWT | 支持；GUI 管理本次启动的 MCP 与 `cloudflared` | 当前推荐的网页 ChatGPT/公开 HTTPS MCP 路径 |
| OpenAI Secure MCP Tunnel | `127.0.0.1:8787` | 本地 hop token + OpenAI Platform Tunnel | 支持；以 `-Transport openai_runtime` 启动 Preview，也保留独立脚本 | 已有 OpenAI Platform Tunnel、需要官方 tunnel-client 的路径 |

下文首先说明 Cloudflare Preview；OpenAI Tunnel 的步骤在文末。一次只选择一条路径，不要同时启动占用对应 origin 的独立脚本与 Preview 管理流程。

## 1. 环境要求

- Windows x64；当前开发和 Preview 验证使用 Go 1.26.x，`go.mod` 的最低语言版本为 Go 1.25.0。
- Git、PowerShell 5.1 或 PowerShell 7。脚本不会修改系统执行策略；必要时用 `-ExecutionPolicy Bypass` 只对当前脚本进程执行。
- Cloudflare 方案需要官方 `cloudflared.exe`；OpenAI 方案还需要官方 `tunnel-client.exe`。第三方可执行文件不提交到仓库。
- 首次 `go build` 需要下载 Go 模块。若网络受限，先确认 Go module proxy 或准备好本地模块缓存。

环境检查只报告状态和稳定错误类别，不打印 token、key、JWT、命令行或文件内容：

```powershell
Set-Location <clone-root>
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Test-LocalProbeEnvironment.ps1 `
  -RepoRoot (Get-Location).Path
```

Windows PowerShell 5.1 可把 `pwsh` 换成 `powershell`。脚本的完整参数以 `-?` 输出为准。

## 2. 克隆当前 Preview 分支

如果当前 Preview 尚未合并到默认分支，直接克隆功能分支：

```powershell
git clone --branch feat/cloudflare-mcp-ingress https://github.com/LE-saber/Local-Probe.git
Set-Location .\Local-Probe
```

如果 Preview 已经合并到默认分支，则省略 `--branch`。仓库父目录可以任意选择；下文用 `<clone-root>` 表示仓库根目录，用 `<clone-parent>` 表示其父目录。

## 3. 初始化本地 Preview 布局

推荐使用统一初始化入口。它创建被 Git 忽略的 `.runtime`、只读 Local-Probe/Cloudflare 配置和一个仓库外的 Cloudflare token 文件；不会创建 OpenAI Tunnel 的 API key、Tunnel ID 或 local hop token，也不会登录 Cloudflare、创建 Tunnel、修改 DNS、上传文件或把秘密写进仓库：

```powershell
$authorizedRoot = (Resolve-Path 'C:\replace-with-folder-you-authorize').Path

pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Setup-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path `
  -AuthorizedRoot $authorizedRoot
```

`-AuthorizedRoot` 必须是你明确选择的已存在目录；脚本不会默认授权仓库或便携包目录。如需设置公网 hostname，使用 `-PublicHost` 参数。先运行：

```powershell
Get-Help .\scripts\Setup-LocalProbePreview.ps1 -Full
```

统一初始化会同时生成 Cloudflare Preview 所需的 `.runtime\local-probe.json`、Tunnel 配置和 Access 配置。底层的 `Initialize-CloudflareTunnel.ps1` 只生成 Cloudflare 文件，不会生成 Local-Probe 授权配置；`Initialize-TunnelClient.ps1` 则属于 OpenAI 8787 路径，不应混入 Cloudflare Preview 初始化。

默认秘密位置如下，值只在本机填写，不要复制到聊天、Issue、PR 或命令历史：

```text
<clone-parent>\.secrets\cloudflared-tunnel-token.txt
```

Cloudflare Preview 只需要填写 `cloudflared-tunnel-token.txt`。OpenAI Tunnel 路径会在其独立初始化阶段创建自己的 API key、Tunnel ID 和本地 hop token 文件。

## 4. 准备 Cloudflare 方案

### 4.1 放置官方 cloudflared

Preview 不会自动下载或执行未知来源的程序。将经过来源和 SHA-256 校验的官方 Windows `cloudflared.exe` 放到 Preview 的固定外部工具布局：

```text
<clone-root>\.tools\cloudflared.exe
```

可让统一脚本把已下载并核验的官方文件复制到固定位置：

```powershell
.\scripts\Setup-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path `
  -AuthorizedRoot $authorizedRoot `
  -CloudflaredPath C:\path\to\cloudflared.exe `
  -CopyCloudflared
```

脚本不会自动联网下载。当前 GUI 的默认连接控制器只使用上述固定路径；`CloudflaredPath` 是本机初始化参数，不会暴露为模型工具参数。

### 4.2 填写 Cloudflare 配置

编辑被 Git 忽略的 `.runtime\cloudflare-tunnel.json`：

- `public_host`：你在 Cloudflare 中使用的完整 hostname；
- `origin_url`：保持 `http://127.0.0.1:8788`；
- `metrics_addr`：保持 loopback，例如 `127.0.0.1:49300`；
- `transport_protocol`：`auto`、`http2` 或 `quic`，一般保持 `auto`。

编辑 `.runtime\cloudflare-access.json`。模板见 [`configs/cloudflare-access.example.json`](../configs/cloudflare-access.example.json)，需要填入 issuer、JWKS URL、Access Application Audience Tag、已核验的 JWT `sub` 到 `chatgpt-local` 的映射，以及与 Tunnel 配置完全一致的 `public_hosts`。

在 Cloudflare Zero Trust 中配置 Published application route：hostname 指向 `http://127.0.0.1:8788`，HTTP Host Header 使用同一个公网 hostname。不要把 metrics 或本地管理地址暴露到 Tunnel route。

### 4.3 构建并启动 Preview

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Test-LocalProbeEnvironment.ps1 `
  -RepoRoot (Get-Location).Path

pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Build-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path

pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Start-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path
```

也可以使用 `Start-LocalProbePreview.ps1 -Build` 一次构建并启动。构建产物为 `bin\local-probe-preview.exe` 和 `bin\local-probe-mcp.exe`。在 GUI 的 Connections 页面点击“一键连接”。固定流程依次执行配置和 token 预检、启动本地 MCP（8788）、等待 loopback、启动 `cloudflared`、等待 `/ready` 和活动 HA connection。

连接成功后，在 ChatGPT 使用 `https://<public-host>/mcp`。先验证 `initialize`、`tools/list`、`ping`，再使用 `server_info`、目录检索和只读文件读取。GUI 显示“已连接”只代表本机 origin 和 Tunnel readiness 已通过，不等于 ChatGPT Workspace、Access 策略或网页端调用已经通过。

## 5. 工作空间、托盘和诊断

- **工作空间访问**页面可新增绝对路径、浏览目录、复选多项并批量移除。保存的是配置授权关系，不删除物理目录；新增目录必须是固定磁盘上的普通目录，网络盘、UNC、symlink/junction/reparse、父子重叠范围会被拒绝。
- GPT 只能访问当前 profile 的 roots、工具白名单和只读策略。文件夹绝对路径不会放入 `server_info`、审计或诊断导出。
- 关闭窗口只隐藏到托盘；托盘菜单提供主界面、刷新、诊断导出、字号切换和退出。退出只停止本次 Preview 拥有的子进程，不接管手动启动的 MCP/Tunnel。
- Logs/Diagnostics 读取有界、脱敏的审计事件；导出前执行第二次 allowlist 和敏感字段检查，已有目标文件不会被覆盖。

## 6. OpenAI Secure MCP Tunnel 方案（替代路径）

这条路径不使用 Cloudflare Access。完整说明见 [`TUNNEL_SETUP.zh-CN.md`](TUNNEL_SETUP.zh-CN.md)。最小步骤如下：

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Initialize-TunnelClient.ps1 `
  -RepoRoot (Get-Location).Path `
  -AuthorizedRoot <authorized-folder>

# 在仓库父目录的 .secrets 中填写 control-plane-api-key.txt 和 tunnel-id.txt
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Test-TunnelClientPrerequisites.ps1 `
  -RepoRoot (Get-Location).Path -RequireCredentials

# 窗口一
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Start-LocalProbeMcp.ps1 `
  -RepoRoot (Get-Location).Path

# 窗口二
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Start-TunnelClient.ps1 `
  -RepoRoot (Get-Location).Path
```

初始化完成后，可继续使用上面的独立脚本；也可构建 Preview 并运行：

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Start-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path `
  -Transport openai_runtime
```

此时 Connections 的一键连接会管理本次启动的 8787 MCP 与官方 `tunnel-client`。该路径监听 `127.0.0.1:8787`，ChatGPT 连接的是 Platform Tunnel 关联的 Tunnel ID。GUI 显示“本机已就绪”不代表目标 ChatGPT workspace 已关联或工具已经完成真实调用，仍需在开发者模式 App 中手动验收。

## 7. 常见故障

| 界面/预检类别 | 原因 | 处理 |
| --- | --- | --- |
| `config_missing` / `config_invalid` | `.runtime` 尚未初始化或 JSON 无效 | 重新运行统一初始化；确认 `local-probe.json`、Tunnel 和 Access 配置均存在 |
| `token_missing` / `token_empty` | Cloudflare token 文件缺失或为空 | 只在仓库父目录 `.secrets\cloudflared-tunnel-token.txt` 填一行 token |
| `cloudflared_missing` | 外部官方二进制不在固定路径 | 放置官方 `cloudflared.exe`，不要用 symlink/reparse 代替 |
| `mcp_binary_missing` | 构建没有产生 MCP origin | 重新运行 `Build-LocalProbePreview.ps1` |
| `port_in_use` | 旧的手动 MCP/Tunnel 或第二个 Preview 正在监听 | 关闭拥有该端口的进程；不要让两个启动链路同时运行 |
| `edge_unreachable` / `tunnel_not_ready` | 网络、防火墙或 Cloudflare route 未就绪 | 确认出站 TCP 7844/443、Cloudflare `/ready`、HA connection 和 Published route |
| `access_config_invalid` | issuer、JWKS、audience、subject 或 hostname 不匹配 | 对照 `configs/cloudflare-access.example.json` 和 Cloudflare Access 配置逐项核对 |
| OpenAI doctor 失败 | Runtime API key、Tunnel ID 或 8787 origin 不可用 | 只按 OpenAI Tunnel 文档填写外部 secret，先启动本地 MCP，再重跑严格预检 |

## 8. 清理与边界

可删除并重新生成：`bin/`、`tmp/` 和仓库内 `.runtime/`（先备份需要的配置）。不要把 `.secrets/`、Tunnel token、Runtime API key、JWT、日志中的原始内容或第三方二进制提交到 Git。

当前 Preview 不包含生产级 supervisor/service 安装、自动更新、代码签名、Linux/macOS GUI、写入工具、任意 shell 或任意命令执行。提交前和发布前请以 [`R9_RELEASE_CHECKLIST.zh-CN.md`](R9_RELEASE_CHECKLIST.zh-CN.md) 的真实验证结果为准。
