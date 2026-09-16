# 兼容性与真实账户预检

核查日期：2026-09-16。当前有**一次**真实的 ChatGPT Business“极高”经 Cloudflare
Tunnel 到 Local-Probe 的 MCP 调用证据；这证明该匿名账户/该次配置的最小链路可用，不是
所有套餐、账号、模型或长期运行的兼容性承诺。Pro、多账号并发、长期运行、workspace
策略差异和发布级健康证据仍未验证。

## 官方资料口径差异

- Developer Mode 指南当前列出 Plus、Pro、Business、Enterprise、Education，并描述完整 MCP 工具能力：`https://developers.openai.com/api/docs/guides/developer-mode`。
- Help Center 当前将 Full MCP 与 Business/Enterprise/Edu 关联，另描述 Pro 的 read/fetch 范围：`https://help.openai.com/en/articles/12584461-developer-mode-and-mcp-apps-in-chatgpt-beta`。
- Tunnel 指南将 Platform 的 Read/Use/Manage 权限与 ChatGPT developer-mode 权限分开，并允许多个组织/workspace 的 association：`https://developers.openai.com/api/docs/guides/secure-mcp-tunnels`。

因此不按订阅名称推断能力。账户套餐、Workspace 管理策略、网页模型实际标签、App 工具
发现、Tunnel Read/Use/association 和本地 MCP 状态必须分别记录；一次成功也不代表其它
组合成功。

## 已有一次真实链路证据

| 匿名 connection | OS/本地端 | workspace/套餐 | 网页模型/推理档位 | App/Tunnel | 已验证调用 | 证据日期与范围 |
|---|---|---|---|---|---|---|
| A | Windows 本机；origin loopback | Business | 网页实际选择“极高” | 自定义 MCP App；Cloudflare Access + Named Tunnel，association/真实 ingress 已通过 | `server_info`、`ping`、`read_file`、`batch_read`；随后 `list_directory`、`find_files`、`search_text`、`tree_directory`；Tunnel 重连后无需重新登录 | 2026-09-10 20:53 的 `tree_directory` 记录及 P05/P06 本地脱敏证据；详见 [`IMPLEMENTATION_STATUS.md`](IMPLEMENTATION_STATUS.md) |
| B | 未验证 | 未验证 | 未验证 | 未验证 | 未验证 | 未验证 |

该记录不包含邮箱、Cookie、完整 org/workspace ID、API key、JWT 或 Tunnel token。`tree_directory`
的已记录调用使用 `root_id=project`、`path=internal`、`max_depth=2`、`page_size=8`、
`max_entries=30`，分页与 `internal/cfaccess/verifier.go` 的 128-byte 读取通过；`.runtime`
拒绝路径也通过。它只说明这次 Business“极高”会话的远程调用和本地 rootfs 结果一致，不说明
Pro 或第二个账户行为。

## 状态分类

本文件的兼容性事实分为：

- `implemented`：本地代码/配置边界存在，不表示外部服务可用。
- `self-tested`：实现者在指定 OS、客户端、协议和日期实际运行过。
- `independently-reviewed`：独立上下文审查通过；当前没有该结论。
- `release-ready`：产品发布硬门全部通过；当前为否。

已有真实 Business“极高”+Cloudflare 调用属于一次 `self-tested`/真实链路观察；它不是
`independently-reviewed`，也不是 `release-ready`。R9 `releasecheck` 的每份报告同样固定
`production_ready=false` 与 `release_ready=false`。

## 操作步骤

1. 在目标账户确认原生 App/Developer Mode 入口及创建权限，记录网页模型实际标签，不凭订阅名称推断。
2. 单独确认 Platform Tunnel Read/Use 权限及 ChatGPT workspace association；由所有者在本机录入 runtime key，不把 key 发给聊天、Issue 或 PR。
3. 先用官方 MCP SDK 的无敏感信息调用验证 `initialize`/`server/discover`、`tools/list` 和 `tools/call`，再经 Tunnel 调用只读工具。
4. 记录真实错误层次：workspace/App、Tunnel permission/association、edge/Access、local MCP、rootfs，不把“App 可添加”当作工具调用成功。
5. 第二账户重复，使用两个不同 profile 做正向和越权测试，再独立停用其中一个；记录多账号隔离和长期运行结果。
6. 每条证据仅记录匿名标签、日期、协议/客户端版本、工具名、范围与脱敏结果，不保存对话全文、凭据或秘密路径。

本轮明确：使用 Business“极高”记录，不用 Pro 代替；Pro 仍未验证。一个 shared connection
不提供可凭空相信的终端成员身份；首版授权边界是 connection/profile。要逐成员隔离，需要
可信 OAuth principal 映射或独立 connector/ingress。

## 当前本地可验证内容

Windows 本机固定官方 MCP Go SDK v1.7.0，已有现代无状态与旧版有状态 Streamable HTTP
协商、本地认证、connection/profile 工具隔离、跨源保护、会话用户绑定，以及
`read_file`/`batch_read` 测试。可执行服务只接受显式 loopback 地址，历史检查为
`127.0.0.1:8787`。

官方 `tunnel-client v0.0.14` Windows amd64 的包、release SHA-256、loopback profile、
secret 文件 ACL、Git 忽略和 `api.openai.com:443` 可达性均有本地预检记录。真实调用使用的
凭据在仓库外，不写入本文档；不要把预检、App 创建或一次成功调用扩展为所有账号保证。
详见 `docs/TUNNEL_SETUP.zh-CN.md`。

1m/百万文件测试按 2026-09-16 用户决定不运行；这不阻塞 R9 入口，但 R7 catalog 仍
`ProductionReady=false`、local-only、默认关闭并必须 live verify。R9 的 Windows
`scripts/acceptance.ps1` 只做本地有界检查，不联网、不打开浏览器、不证明 Cloudflare 或
ChatGPT 兼容性，也不代表 P05 的 `workspace_snapshot`、完整 wire 预算或 P10 supervisor 已完成。
