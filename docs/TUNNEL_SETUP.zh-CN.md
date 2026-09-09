# ChatGPT App / OpenAI Tunnel 本机准备

本文只覆盖 Windows 本机的官方 `tunnel-client` 准备和真实账户联调入口。
它不把本地预检写成已经完成的 P01/P05 真实调用证据；只有目标账户实际完成
`initialize -> tools/list -> tools/call` 后，才能更新 `docs/COMPATIBILITY.md`。

官方入口：

- [OpenAI Secure MCP Tunnel](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels)
- [OpenAI Developer Mode](https://developers.openai.com/api/docs/guides/developer-mode)
- [Platform Tunnels](https://platform.openai.com/settings/organization/tunnels)
- [Platform Runtime API keys](https://platform.openai.com/settings/organization/api-keys)
- [ChatGPT Connectors](https://chatgpt.com/#settings/Connectors)

## 需要用户提供的只有两个本机值

初始化脚本会在仓库外建立以下目录和空文件：

```text
D:\HOPP\download\Local-Probe\.secrets\control-plane-api-key.txt
D:\HOPP\download\Local-Probe\.secrets\tunnel-id.txt
D:\HOPP\download\Local-Probe\.secrets\mcp-bearer-token.txt
```

把值写入文件时各占一行，不要加引号，不要把值发到聊天、Issue、PR 或命令
历史中：

1. `control-plane-api-key.txt`：Platform **Runtime API key**，其 principal
   只需目标 Tunnel 的 **Tunnels Read + Use**。这是长期运行 daemon 使用的 key。
2. `tunnel-id.txt`：Platform Tunnels 页面中已有或新建 Tunnel 的 ID，格式是
   `tunnel_` 加 32 个小写十六进制字符。Tunnel ID 本身不是 API key，但仍放在
   外部文件中，便于切换并避免把账户环境写进仓库。
3. `mcp-bearer-token.txt` 由初始化脚本自动生成高熵本地 hop token；不需要用户
   填写、复制或发送。Local-Probe MCP 入口和 tunnel-client profile 读取同一文件，
   以此把本机连接认证与 OpenAI control-plane key 分开。

因此用户只需填写前两项。这里不需要普通模型 `OPENAI_API_KEY`。也不需要 `OPENAI_ADMIN_KEY`；只有使用
命令行创建/修改/删除 Tunnel 时才需要 Admin key，且不能把它放进长期 daemon
配置。本项目的本地配置只引用 `file:`，不会保存实际 key。

## 已准备的本机文件

官方 Windows amd64 `tunnel-client v0.0.14` 完整 release（含相邻
`cloudflared.exe`）位于仓库外：

```text
D:\HOPP\download\Local-Probe\_tools\tunnel-client-v0.0.14-windows-amd64\
```

下载压缩包的 SHA-256 已核对为：

```text
784ab8da7b5a88f0109f1fd8aaf0a1c86067430b896dddf307ef7e3cc49fa1a5
```

仓库内的配置模板是 [`configs/tunnel-client.example.yaml`](../configs/tunnel-client.example.yaml)。
运行时配置会放在被 git 忽略的 `.runtime/tunnel-client.yaml`；secret 目录始终
位于仓库外。

## 初始化和预检

在仓库根目录 PowerShell 执行：

```powershell
.\scripts\Initialize-TunnelClient.ps1
.\scripts\Test-TunnelClientPrerequisites.ps1
```

第一条命令只建立目录、空占位文件和使用 `file:` 引用的运行时模板；第二条
命令不打印 key、Tunnel ID 或完整 doctor 输出。先在 Platform 页面取得 Tunnel
ID 和 Runtime API key，并填写前两份外部文件。

本地 MCP 服务使用初始化脚本生成的只读 JSON 配置和自动生成的本地 hop token，
先在一个 PowerShell 窗口启动：

```powershell
.\scripts\Start-LocalProbeMcp.ps1
```

它调用固定的 `cmd/local-probe-mcp`，默认只监听 `127.0.0.1:8787`；不要把
token 作为命令参数传入。再在第二个窗口运行严格预检和 tunnel-client：

```powershell
.\scripts\Test-TunnelClientPrerequisites.ps1 -RequireCredentials
.\scripts\Start-TunnelClient.ps1
```

若只想先启动 tunnel-client 等待尚未就绪的本地 MCP 服务，可以临时加
`-SkipDoctor`；正式调用前必须不带该参数再次运行 doctor。默认 MCP 地址是
`http://127.0.0.1:8787/mcp`，脚本拒绝非 loopback 地址，健康/UI 地址也固定为
loopback 随机端口，不使用 `--allow-remote-ui`。

保持该 PowerShell 窗口运行。脚本不会把 runtime key 放在 argv 中；它只把
Tunnel ID（非 secret）作为参数传给官方客户端，并把健康 URL 写入：

```text
D:\HOPP\download\Local-Probe\repo\.runtime\tunnel-health-url.txt
```

不要启用 `--log.http-raw-unsafe`，不要把健康监听改成 `0.0.0.0`。

## 账户端联调顺序

请在脚本预检通过后，再打开浏览器登录目标 ChatGPT 账号：

1. 在 Platform Tunnels 页面创建或选择 Tunnel，确认该 Tunnel 关联了目标
   ChatGPT workspace，并复制其 ID 到外部 `tunnel-id.txt`。
2. 在 Runtime API keys 页面创建 Restricted key，授予 Tunnels Read + Use，
   将完整 key 粘贴到外部 `control-plane-api-key.txt`。创建后重新运行严格预检。
3. 在 ChatGPT 的 Settings / Connectors（或账户显示的 Developer Mode/App
   入口）创建开发者模式自定义 App，连接方式选 Tunnel，选择或粘贴同一个
   Tunnel ID。不能把“App 可以添加”当作“工具已经调用成功”。
4. 在普通 Web 对话中选择用户指定的“极高”模型/推理档位（以该账号 UI
   实际显示为准），本轮不选择 Pro。先调用无敏感数据的 `server_info`/`echo`
   或等价只读工具，确认 `tools/list` 与 `tools/call`，再进入本地文件工具。
5. 记录实际模型标签、App 是否可添加、工具是否发现、只读调用是否成功，
   以匿名标签写入兼容性矩阵；不记录邮箱、Cookie、完整 key 或完整账户 ID。

如果“极高”档位在该账号 UI 中不可见，应记录为账号能力/策略待确认，不用
Pro 替代，也不通过公开 URL 或 DOM 自动化绕过 Tunnel。

## 故障分类

- 本地二进制/hash/`cloudflared` 失败：重新下载官方 release，不运行未知来源
  的 tunnel 程序。
- 严格预检提示 key：确认使用 Runtime key，且文件只有一行；不要改用 Admin key。
- 403/不可 poll：检查 key principal 的 Tunnels Read + Use、Tunnel ID 以及
  organization/workspace association。
- Tunnel 在 Platform 可见但 ChatGPT 不可选：检查 workspace association、
  ChatGPT 账户 Developer Mode/App 权限，并确认 daemon 仍在运行。
- `doctor` 找不到 MCP：先在本机启动 Local-Probe MCP 服务并保持
  `127.0.0.1:8787` 可用，再重跑严格预检。

任何一次失败都要按层记录（账户入口、Tunnel 权限、daemon poll、本地 MCP
协议、工具调用），不要把 UI 看见 App 写成端到端通过。
