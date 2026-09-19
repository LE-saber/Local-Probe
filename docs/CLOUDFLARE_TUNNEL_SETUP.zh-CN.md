# Cloudflare Named Tunnel + Access 本地接入

本文档描述 Local-Probe 的 Cloudflare 本地阶段实现。本轮只完成本机代码、临时 JWKS 测试和 PowerShell 预检；没有登录 Cloudflare、创建 Tunnel、DNS 或 Access Application，也没有声称 ChatGPT 端到端成功。

## 架构

```text
ChatGPT 公共 MCP URL
        │ OAuth 由 Cloudflare Access Managed OAuth 处理
        ▼
Cloudflare Edge ── Access JWT ── Named Tunnel
                                      │
                                      ▼
                          127.0.0.1:8788 Local-Probe
                          exact Host + CF JWT 校验
                          sub → connection 显式映射
                          policy → profile/root/tool
```

旧 OpenAI Tunnel 本地 hop 保持不变：使用 `X-Local-Probe-Token`，默认 `127.0.0.1:8787`。Cloudflare 是独立 ingress，默认 `127.0.0.1:8788`，不会接受旧 hop token，也不会把请求中的 connection/profile/root 当作身份。

CF ingress 在 SDK 之前检查精确 `Host`。只有该模式且 Host 命中运行配置时，才关闭官方 Go MCP SDK 的 localhost protection；listener 仍由命令强制绑定到 loopback。

MCP 传输同时兼容两代客户端：带 `Mcp-Protocol-Version: 2026-07-28` 的现代请求进入 SDK 的无会话 Streamable HTTP 路径，`server/discover` 返回包含该版本的能力列表，后续 `tools/list`/`tools/call` 每次请求都重新验证 Access 身份；没有该现代版本头的旧协议请求保留会话 ID、GET/SSE、DELETE 和会话用户绑定。普通单次 POST 使用 `application/json` 响应，旧协议的独立 GET/SSE 仍使用 `text/event-stream`。现代请求必须按协议带 `_meta` 协商字段和 `Mcp-Method` 等标准头，不能把 HTTP 200 的 JSON-RPC 错误当成工具发现成功。

## 初始化和文件位置

```powershell
.\scripts\Initialize-CloudflareTunnel.ps1
```

初始化脚本只创建本机目录、占位配置和空 token 文件，不调用 Cloudflare API。默认位置如下：

| 用途 | 默认位置 | 处理 |
|---|---|---|
| cloudflared Tunnel token | 仓库外 `.secrets\cloudflared-tunnel-token.txt` | 只填一行秘密，不提交 |
| 远程 Tunnel 预期值 | `.runtime\cloudflare-tunnel.json` | 记录公网 hostname、loopback origin 和 metrics 地址，不含凭据 |
| Access verifier 配置 | `.runtime\cloudflare-access.json` | 填 issuer/JWKS/audience/映射 |
| Local-Probe 配置 | `.runtime\local-probe.json` | 沿用现有初始化脚本 |

仓库内 `.runtime/` 与仓库外 `.secrets/` 已忽略/隔离。初始化脚本尝试收紧 Windows ACL，预检会报告失败。

## 需要填写的值

编辑 `.runtime\cloudflare-tunnel.json`：

- `public_host`：一个固定公网 DNS hostname；
- `origin_url`：保持 `http://127.0.0.1:8788`，不要使用 `0.0.0.0`、局域网或公网地址；
- `metrics_addr`：保持 loopback，例如 `127.0.0.1:49300`。
- `transport_protocol`：`auto`、`http2` 或 `quic`，默认是 `auto`。`auto` 让 cloudflared 按当前版本和网络状况自动协商；只有明确需要固定协议时才改为 `http2` 或 `quic`。启动脚本在 `auto` 下不会设置 `TUNNEL_TRANSPORT_PROTOCOL`，避免把旧的进程/用户环境变量误当成默认值。

本方案统一使用 Cloudflare 推荐的**远程托管 Named Tunnel**。Tunnel 身份由仓库外 token 文件提供；Published application route 与 Host Header 等 origin 参数在 Cloudflare Dashboard 管理。不要把本地托管 Tunnel 的 `credentials-file`/ingress YAML 与远程 token 启动方式混用。

编辑 `.runtime\cloudflare-access.json`。字段结构也可参考仓库模板 [configs/cloudflare-access.example.json](../configs/cloudflare-access.example.json)：

```json
{
  "issuer": "https://<team>.cloudflareaccess.com",
  "jwks_url": "https://<team>.cloudflareaccess.com/cdn-cgi/access/certs",
  "audience": "<Access Application Audience Tag>",
  "clock_skew_seconds": 30,
  "principal_to_connection": {
    "<signed Access JWT sub>": "chatgpt-local"
  },
  "public_hosts": ["mcp.example.example.com"]
}
```

这些值必须来自你的 Cloudflare 配置，本仓库不猜测。`principal_to_connection` 是唯一身份绑定，目标 connection 必须已存在且启用。origin 使用成熟的 `coreos/go-oidc` 验证 RS256 签名、issuer、audience，并检查必需 `exp` 与可选 `nbf`；时钟偏差默认 30 秒，最大 5 分钟。

## 启动本地阶段

```powershell
go run ./cmd/local-probe-mcp -config .runtime/local-probe.json -ingress cloudflare-access -cloudflare-access-config .runtime/cloudflare-access.json -listen-addr 127.0.0.1:8788
```

无凭据预检（不登录、不列举、不创建资源）：

```powershell
.\scripts\Test-CloudflareTunnelPrerequisites.ps1
.\scripts\Test-CloudflareTunnelPrerequisites.ps1 -RequireCredentials -CheckMcpEndpoint
# 不依赖真实 Tunnel 的隔离脚本测试（loopback /ready + metrics fixture）
.\scripts\Test-CloudflareTunnelPrerequisites.Isolated.ps1
```

填好 UUID、token 和 Access 设置后，再前台启动：

```powershell
.\scripts\Start-CloudflareTunnel.ps1
```

该脚本使用 `cloudflared tunnel --no-autoupdate --metrics <loopback> run --token-file <外部文件路径>`；token 值不会进入 argv、配置或日志。脚本不会执行 login、Tunnel/DNS/Access app 创建，也不会用不受支持的本地 ingress YAML 覆盖远程托管路由。

预检会先检查 metrics 地址的 `/ready`：只有 HTTP 200 才表示 cloudflared 已经向 Cloudflare 注册并处于 ready 状态；随后才会结合 `cloudflared_tunnel_ha_connections` 指标确认至少有一个 HA 连接。即使 HA 指标暂时仍为正数，`/ready` 返回 503 或无法访问时也不会报告 Tunnel 已连接，从而避免把旧指标当成健康状态。cloudflared 尚未启动时 `/ready` 可显示为 PENDING，这是允许启动前检查继续进行的正常结果；已监听但返回非 200 则会阻止启动并提示先处理旧的/未就绪进程。

预检还会测试 Cloudflare Tunnel edge 的 TCP 7844、Access JWKS 的 TCP 443、loopback 绑定和潜在的全局出站 Block 规则。发行脚本只报告并拒绝启动，不会自动停用或改写系统防火墙；网络管理员应按 Cloudflare 要求放行出站 7844。

## 后续人工 Cloudflare/ChatGPT 步骤

1. 在 Cloudflare Zero Trust 中创建或选择远程托管 Named Tunnel，取得 token 并粘贴到仓库外 token 文件；本机无需保存账户级 `cert.pem`。
2. 为固定 hostname 添加 Published application route，service 必须与本地 `origin_url` 一致；把 HTTP Host Header 设为同一个 `public_host`。
3. 为 hostname 创建 Access Application，仅允许指定用户/组；不要对 MCP route 使用会返回 HTML 的 Bot Challenge/CAPTCHA。metrics 和管理入口不加入 Tunnel route。
4. 启用 Cloudflare Access Managed OAuth 和动态客户端注册，把 ChatGPT 当前 Developer Mode/connector 页面显示的实际 HTTPS redirect URI 加入允许列表。OAuth 端点留在 Cloudflare edge，origin 不伪造 OAuth well-known/OAuth server。
5. 将 team issuer、JWKS URL、Audience Tag 和已核验的 JWT `sub` 填入 Access JSON，重启 Local-Probe。`sub` 是 Access 用户 ID；可在 Zero Trust 用户信息中取得，或登录受保护 hostname 后查看 `/cdn-cgi/access/get-identity` 返回的 `user_uuid`，不要把 JWT/token 发给本项目或第三方解码网站。
6. 在 ChatGPT 使用 `https://<hostname>/mcp`，选择 OAuth/Access 登录流程，先验证 `initialize`、`tools/list`、`ping`，再按最小权限开放只读文件工具。

ChatGPT 不能按本项目需要发送任意 API Key、`X-Local-Probe-Token`、Cloudflare Service Token 或自定义 header。因此这些只能供明确支持自定义 header 的其他客户端使用，不能替代 Access Managed OAuth + origin JWT 校验。

Managed OAuth 客户端发送给 Cloudflare 的是 opaque bearer token；Cloudflare 在 edge 解析身份后把签名 JWT 放入 `Cf-Access-Jwt-Assertion`。origin 会丢弃转发来的原始 `Authorization` 值，只验证该签名断言，避免把 opaque token 或旧本地 token 误当成第二条身份路径。

## 本地验收和边界

临时 loopback issuer/JWKS 测试覆盖有效/错误签名、issuer/audience、过期、nbf、未知 subject；MCP `initialize`、`tools/list`、`tools/call`、GET/SSE、DELETE；精确 Host、缺少/重复 assertion、禁止 Authorization fallback；旧本地 bearer 和 no-OAuth well-known 404 回归。

```powershell
go test ./...
go test -race ./...
go vet ./...
```

这些结果只证明本地实现与本机配置校验，不证明 Cloudflare 资源、DNS、Access 策略、ChatGPT Workspace 权限或 ChatGPT 端到端创建成功。关闭进程或移除 Cloudflare route 后应 fail closed；切回旧 OpenAI Tunnel 不需删除现有 root 或策略配置。

## 官方依据

- [Cloudflare Access Managed OAuth](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/managed-oauth/)
- [Cloudflare Access JWT 验证](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/)
- [Cloudflare Tunnel 配置模型](https://developers.cloudflare.com/tunnel/configuration/)
