# Local-Probe Windows Preview

这是 Local-Probe 的 **Windows 本地 GUI/tray 预览版**。它把已经实现的配置投影、开发者规则投影和有界审计读取能力组合成桌面管理器，方便查看当前状态、导出脱敏诊断，并用受限的一键连接流程启动本 Preview 自己管理的 MCP origin 和 Cloudflare Tunnel。它仍不是生产版本或通用进程控制器：连接流程只接受固定的本地配置和固定的可执行文件，不提供任意命令、任意路径或模型可调用的进程控制。

## 构建与启动

要求：Windows、仓库要求的 Go 工具链，以及可执行 PowerShell 脚本的本地环境。

从仓库根目录运行：

```powershell
.\scripts\Build-LocalProbePreview.ps1
.\scripts\Start-LocalProbePreview.ps1
```

默认产物是：

```text
bin\local-probe-preview.exe
bin\local-probe-mcp.exe
```

Preview 构建会同时编译 GUI 和本地 MCP origin。`bin\local-probe-mcp.exe` 是一键连接流程使用的稳定副本；不要把 `.runtime` 里的临时测试副本当作发行文件，也不要手动把 token 写入命令行参数。

也可一次完成构建与启动：

```powershell
.\scripts\Start-LocalProbePreview.ps1 -Build
```

指定配置、审计目录或预览程序路径：

```powershell
.\scripts\Start-LocalProbePreview.ps1 `
  -ConfigPath D:\path\to\local-probe.json `
  -AuditDir D:\path\to\audit `
  -ExecutablePath .\bin\local-probe-preview.exe
```

脚本默认读取：

- 配置：`<仓库根目录>\.runtime\local-probe.json`
- 审计目录：`%APPDATA%\Local-Probe\audit`

直接运行 `bin\local-probe-preview.exe` 时，程序也会按其安装位置推导仓库内的 `.runtime\local-probe.json`，Tunnel 配置仍来自 `.runtime\cloudflare-tunnel.json` 和 `.runtime\cloudflare-access.json`，审计目录仍使用当前用户的 `%APPDATA%\Local-Probe\audit`。Tunnel token 只从仓库父目录的外部凭据文件读取：

```text
<仓库父目录>\.secrets\cloudflared-tunnel-token.txt
```

文件内容必须是单独一行的 Cloudflare Tunnel token。Preview 只显示“已配置/缺失/格式不正确”等状态，绝不把 token 值放入界面、启动参数、审计或诊断导出。缺少配置或审计目录时，Preview 显示明确的 `unconfigured` / `unavailable` 状态；它不会替用户猜测 token，也不会自动创建 Cloudflare 资源。

The token value is never displayed, logged, or included in a diagnostic export.

## 当前界面能力

窗口包含以下只读页面：

- **Overview**：配置 revision、root/profile/connection 数量、审计摘要，以及连接阶段、连接结果和可执行的解决建议。
- **Connections**：连接、profile、transport 和 Tunnel 配置的脱敏投影；提供“一键连接/重新连接”入口，并显示本地 MCP、Tunnel metrics readiness 和 edge connection 的分阶段状态。
- **Developer Rules**：开发者模式、允许的 connection 和固定规则/variant/slot 摘要；此处不能编辑或启用规则。
- **Logs / Diagnostics**：从审计文件读取并筛选后的结构化事件与诊断摘要。
- **About**：版本、Preview 边界和运行说明。

窗口底部提供刷新和诊断导出。Connections 页面的一键连接按钮按固定顺序执行本地前置检查、启动 MCP origin、等待 loopback、启动 cloudflared、等待 `/ready` 和 active HA connection；重新连接会先清理本 Preview 当前拥有的子进程，再按同一流程启动。GUI 不开放 shell 或任意可执行文件路径。

连接失败时，界面同时显示稳定错误类别和解决方法，不显示 token、JWT、完整命令行或原始 stderr。常见类别如下：

| 类别 | 含义 | 建议处理 |
| --- | --- | --- |
| `token_missing` / `token_empty` | 外部 token 文件不存在或为空 | 在仓库父目录 `.secrets\cloudflared-tunnel-token.txt` 填入一行 token，重新点击连接 |
| `token_invalid_format` | token 含多行、超限或不可接受字符 | 重新从 Cloudflare 复制完整单行 token，不要加引号或换行 |
| `token_rejected` | cloudflared 启动后被 Cloudflare 拒绝 | 检查 token 是否撤销、是否属于当前 Tunnel；重新生成并替换文件 |
| `cloudflared_missing` | 固定位置找不到 cloudflared | 按 Tunnel 安装说明放置已验证的 Windows `cloudflared.exe`，然后刷新 |
| `mcp_binary_missing` | Preview 构建没有产生 MCP origin | 重新运行 `Build-LocalProbePreview.ps1`，确认 `bin\local-probe-mcp.exe` 存在 |
| `config_missing` / `config_invalid` | `.runtime` 配置不存在或格式错误 | 运行 `Initialize-CloudflareTunnel.ps1`，检查 public host、origin、metrics 和 Access 配置 |
| `origin_not_ready` / `port_in_use` | 本地 MCP 没有按预期监听 loopback | 检查端口占用和配置的 origin 地址；关闭冲突的旧实例后重新连接 |
| `edge_unreachable` / `tunnel_not_ready` | `/ready` 未就绪或没有活动 edge 连接 | 检查网络、防火墙和出站 TCP 7844/443，再重试；TCP 可达本身不等于 Tunnel 已就绪 |
| `access_config_invalid` / `public_route_unreachable` | Access 校验配置或公开路由不匹配 | 核对 issuer、audience、JWKS、principal mapping 与 Cloudflare Published application |

按钮只负责本机已授权的固定流程，不会登录 Cloudflare、创建 Tunnel、改 DNS、写入 token 或扩大 Access 权限。连接成功后，网页端 ChatGPT 仍需使用已经配置的公开 MCP 应用/入口；GUI 的成功状态只表示本地 origin 和 Tunnel readiness 已通过检查。

窗口顶部的“字号”按钮可在 100%、125%、150%、175% 和 200% 之间循环切换，默认使用 150%。该设置只影响当前 Preview 进程中的界面绘制，不会修改配置或影响 MCP/Tunnel。

## 托盘与生命周期

- Preview 使用单实例保护；同一用户会话中已有实例时，不再启动第二个实例。
- 点击窗口关闭按钮只会把窗口隐藏到系统托盘。
- 托盘右键菜单提供显示/隐藏主界面、刷新状态、导出诊断、关于和退出，并可直接在 100%–200% 字号档位之间切换；当前字号会显示勾选状态。
- 托盘、窗口标题栏、任务栏和 Preview 可执行文件使用同一套 Local-Probe 橙蓝图标。
- `Exit` 会退出 Preview，并停止本次 Preview 自己启动的 MCP/Tunnel 子进程；Preview 不会终止或接管它没有启动的既有进程。“重新连接”也只会回收本次 Preview 自己拥有的子进程。

因此，Preview 连接按钮只覆盖 Windows Preview 自己拥有的生命周期；手动脚本或其他服务启动的 MCP/Tunnel 仍由原来的生命周期负责。Preview 显示 unavailable 也不能单独证明外部 MCP/Tunnel 已停止。

## 日志与诊断导出边界

默认审计读取器只选择 `audit.jsonl` 和符合固定命名规则的轮转文件，并拒绝 symlink/reparse 和非普通文件。默认读取上限是 16 个文件、512 KiB、8192 行、128 条记录，单行上限 64 KiB；达到上限会显示 bounded/truncated，而不是继续无界扫描。

展示和导出的字段采用 allowlist，不包含原始配置、文件内容、命令行、环境变量、凭据、Tunnel ID、网络端点或本机路径。导出前还会在统一 support bundle 边界执行第二次结构校验与脱敏检查：字段、数组项、文本和总 JSON 大小都受限，出现敏感标记或路径形态时导出失败。Preview 导出的 JSON 最大为 64 KiB。

保存诊断时只接受新的 `.json` 目标；已有文件不会被覆盖，symlink/reparse 或非普通目标也会被拒绝。导出结果适合先由本机操作者人工复核，再决定是否对外提供；它不是自动上传功能。

## 明确未实现

当前 Windows Preview 不包含：

- MCP/Tunnel 的生产级 supervisor、服务安装、崩溃后无限重试或跨用户进程 ownership；
- 配置、开发者规则或命令放行规则的 GUI 编辑；
- 凭据、token、key、Cookie、原始命令输出或文件内容展示；
- HTTP、loopback 管理站点、named pipe broker 或任何额外网络监听；
- 安装器、自动更新、代码签名、正式发布包或生产就绪声明；
- Linux/macOS GUI。

这些边界是 Preview 的真实能力范围。现有已经开发好的 MCP、文件发现/读取和本地契约继续独立工作；本轮没有为了桌面界面补做此前尚未实现的命令执行、索引或写入功能。
