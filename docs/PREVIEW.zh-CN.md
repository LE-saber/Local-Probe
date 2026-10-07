# Local-Probe Windows Preview

这是 Local-Probe 的 Windows 原生 GUI/tray 预览版。它提供只读状态投影、Cloudflare 8788 或 OpenAI Secure MCP Tunnel 8787 一键连接、工作空间授权管理、托盘驻留、结构化日志查看和脱敏诊断导出。它不是生产 supervisor、通用进程控制器或正式安装包。

第一次部署请先阅读 [Windows Preview 部署](DEPLOYMENT_PREVIEW.zh-CN.md)。该文档明确区分 Cloudflare Preview（8788）和 OpenAI Secure MCP Tunnel（8787）两条二选一链路。

## 构建与启动

要求：Windows x64、Go 1.25 或更高版本、Git 和 PowerShell。Cloudflare 通道需要官方 `cloudflared.exe`；OpenAI 通道需要官方 `tunnel-client.exe`。两者都放在被 Git 忽略的本地工具目录或文档列出的外部工具目录，不提交到源码仓库。

从仓库根目录运行环境检查、初始化、构建和启动：

~~~powershell
$authorizedRoot = (Resolve-Path 'C:\replace-with-folder-you-authorize').Path
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\Test-LocalProbeEnvironment.ps1 -RepoRoot (Get-Location).Path
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\Setup-LocalProbePreview.ps1 -RepoRoot (Get-Location).Path -AuthorizedRoot $authorizedRoot
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\Build-LocalProbePreview.ps1 -RepoRoot (Get-Location).Path
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\Start-LocalProbePreview.ps1 -RepoRoot (Get-Location).Path
~~~

`-AuthorizedRoot` 是必须显式指定的已存在目录；Setup 不会默认授权仓库或包目录。也可以使用 Start-LocalProbePreview.ps1 -Build 一次构建并启动。脚本的完整参数以 Get-Help .\scripts\Setup-LocalProbePreview.ps1 -Full 为准；`-PublicHost` 用于指定公网 hostname。

构建产物位于被 Git 忽略的 bin 目录：

~~~text
bin\local-probe-preview.exe
bin\local-probe-mcp.exe
~~~

直接运行 Preview 时，它按可执行文件位置推导仓库内的 .runtime\local-probe.json；Cloudflare 配置来自 .runtime\cloudflare-tunnel.json 和 .runtime\cloudflare-access.json；审计目录默认为当前用户的 %APPDATA%\Local-Probe\audit。

Cloudflare token 只从仓库父目录读取：

~~~text
<仓库父目录>\.secrets\cloudflared-tunnel-token.txt
~~~

token 必须是单独一行。Preview 不显示、记录或导出 token。`Setup-LocalProbePreview.ps1` 会同时生成 Cloudflare Preview 所需的 `local-probe.json`、Tunnel 和 Access 占位配置；不要用 OpenAI 8787 链路的 `Initialize-TunnelClient.ps1` 代替它。

Cloudflare Preview 默认寻找：

~~~text
<仓库根目录>\.tools\cloudflared.exe
~~~

可用 `Setup-LocalProbePreview.ps1 -CloudflaredPath <官方文件> -CopyCloudflared` 显式复制。为兼容现有开发机，程序仍会在主路径缺失时尝试旧的父目录 `_tools` 布局；新部署不应依赖该回退。

## 界面真实能力

- Overview：显示配置 revision、root/profile/connection 数量、审计摘要和连接建议。
- Connections：显示脱敏连接状态，并按启动参数管理 Cloudflare 8788 或 OpenAI 8787 的一键连接/重新连接及其本机就绪阶段。
- Developer Rules：只读展示开发者模式、connection 和固定规则摘要，不能在此编辑或启用规则。
- 工作空间访问：输入路径或浏览目录，新增多个授权 root，并通过复选框批量撤销。
- Logs / Diagnostics：读取有界结构化审计和诊断摘要。
- About：显示 Preview 版本和边界。

默认启动仍选择 Cloudflare：本地预检、启动 `bin\local-probe-mcp.exe`（`cloudflare-access`，8788）、等待 loopback、启动 `cloudflared`、等待 metrics `/ready` 和活动 HA connection。使用 `Start-LocalProbePreview.ps1 -Transport openai_runtime` 时，一键连接改为启动 `local-token` MCP（8787）、进行带 hop token 的 MCP 工具探针、生成仅引用外部 secret 文件的临时 tunnel-client profile、启动官方 `tunnel-client` 并等待它写出的 loopback `/readyz`。两条路径都不接受模型提供的任意 executable、argv、env、cwd 或命令。

## 工作空间访问

工作空间列表是真实的 chatgpt-local → profile → roots 授权关系。新增和删除使用配置 revision 做并发保护，并原子保存 .runtime\local-probe.json。撤销授权不会删除物理文件夹。

允许的目录必须是已存在的固定磁盘普通目录。磁盘根目录、UNC/映射网络盘、可移动盘、符号链接、junction/reparse point 和父子重叠的授权范围会被拒绝。已包含在现有 root 内的路径会提示无需重复添加。

文件夹绝对路径只用于本机管理界面，不进入 MCP server_info、审计或 support bundle。GPT 最终能访问什么，仍由 profile 的 roots、tools、deny/ignore 和只读策略决定。

## 托盘与生命周期

- Preview 使用单实例保护。
- 关闭窗口只隐藏到托盘。
- 托盘右键菜单提供主界面、刷新、诊断导出、关于、退出和字号档位切换。
- 退出只停止本次 Preview 自己启动的 MCP 和所选 Tunnel 客户端，不会终止手动启动或其他服务拥有的进程。
- Preview 连接按钮只管理本次启动参数选择的一条生命周期。手动进程已经占用端口时报告 `port_in_use`，不会杀掉无关进程。

## 连接故障类别

| 类别 | 含义 | 处理 |
| --- | --- | --- |
| token_missing / token_empty | Cloudflare token 文件缺失或为空 | 在仓库父目录 .secrets 中填入一行 token |
| token_invalid_format / token_rejected | token 格式错误或被 Cloudflare 拒绝 | 重新复制当前 Tunnel 的完整单行 token |
| cloudflared_missing | 固定位置没有 cloudflared | 放置官方二进制，不要用 symlink/reparse 代替 |
| mcp_binary_missing | Preview 构建没有产生 MCP origin | 重新运行 Build-LocalProbePreview.ps1 |
| config_missing / config_invalid | .runtime 配置不存在或无效 | 运行统一初始化，检查 local-probe、Tunnel 和 Access JSON |
| origin_not_ready / port_in_use | 8788 未就绪或被占用 | 关闭冲突实例；不要同时启动两种 Tunnel 链路 |
| edge_unreachable / tunnel_not_ready | metrics 未 ready 或没有 HA connection | 检查出站 7844/443、Tunnel route、网络和防火墙 |
| access_config_invalid | issuer、JWKS、audience、subject 或 hostname 不匹配 | 对照 configs/cloudflare-access.example.json 逐项核对 |

按钮不会登录 Cloudflare、创建 Tunnel、修改 DNS、写入 token 或扩大 Access 权限。连接成功只表示本地 origin 与 Tunnel readiness 通过；网页 ChatGPT 仍需配置公开 MCP 应用和 Access/OAuth。

## 字号、日志和诊断

字号按钮在 100%、125%、150%、175%、200% 之间循环，默认 150%，只影响当前 Preview 界面。

审计读取器只读取符合固定命名规则的普通文件，默认上限为 16 个文件、512 KiB、8192 行、128 条记录，单行 64 KiB。展示和导出采用 allowlist，不包含原始配置、文件内容、完整路径、命令行、环境、凭据、Tunnel ID、JWT 或网络端点。导出前执行第二次脱敏和大小检查；已有目标文件不会被覆盖。

## 明确未实现

- 生产级 MCP/Tunnel supervisor、服务安装、自动恢复和跨用户 ownership；
- 除工作空间 roots 外的配置、开发者规则或命令放行规则编辑；
- 凭据、token、key、Cookie、命令输出或文件内容展示；
- HTTP 管理站点、named pipe broker 或额外网络监听；
- 安装器、自动更新、代码签名、正式发布包或生产就绪声明；
- Linux/macOS GUI、任意 shell、任意命令、写入工具和默认大规模索引。

OpenAI Secure MCP Tunnel 仍可按 [TUNNEL_SETUP.zh-CN.md](TUNNEL_SETUP.zh-CN.md) 使用独立脚本；也可先完成同一份外部凭据初始化，再用 `Start-LocalProbePreview.ps1 -Transport openai_runtime` 交给 Preview 管理本次 8787 生命周期。本机 ready 只证明本地客户端链路，不等于 ChatGPT workspace 已完成关联或真实工具调用验收。
