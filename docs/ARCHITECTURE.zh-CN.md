# Local-Probe 架构说明

Local-Probe 当前是一个以只读文件调查为核心的 Windows Preview。它把 MCP 协议、认证 ingress、策略绑定、rootfs 文件访问、目录检索、审计和本地 GUI 组合起来；它不是 IDE、通用 coding agent、shell 执行器或生产级服务管理器。

## 总体数据流

```text
ChatGPT / MCP 客户端
        │
        ├─ Cloudflare Access Managed OAuth / JWT
        │       │
        │       ▼
        │  Cloudflare Edge ── Named Tunnel ── 127.0.0.1:8788
        │                                      │
        └─ OpenAI Secure MCP Tunnel ───────────┴─ MCP ingress
                                                   │
                              connection/profile/root/tool policy
                                                   │
                            bounded discovery/search/read
                                                   │
                       rootfs Source + real filesystem handles
                                                   │
                            audit → bounded diagnostics
```

两种 ingress 是独立实现：

| 入口 | 监听 | 认证 | 适配器 |
| --- | ---: | --- | --- |
| `local-token` | 默认 `127.0.0.1:8787` | 外部文件中的本地 hop token | OpenAI `tunnel-client` 脚本 |
| `cloudflare-access` | Preview 默认 `127.0.0.1:8788` | 精确 Host + `Cf-Access-Jwt-Assertion` + JWKS/issuer/audience/sub 映射 | Preview controller + `cloudflared` |

Tunnel 只提供传输通道，不等于 Local-Probe 的授权。请求必须在 MCP server 中重新绑定到有效 connection、profile、root 和 tool；请求参数不能自行声明身份。

## 配置和秘密边界

```text
Git tracked:
  configs/*.example.*   无秘密模板
  cmd/ internal/        源码和测试
  scripts/ docs/        构建、部署和技术说明

Git ignored / local:
  <repo>/.runtime/      运行配置、revision、health URL、临时状态
  <repo>/bin/            构建产物
  <repo>/tmp/             测试产物
  <repo-parent>/.secrets/ token、Runtime API key、local hop token
  <repo>/.tools/         Preview 使用的官方 cloudflared 本地副本
  <repo-parent>/_tools/  旧开发环境兼容回退，不是新部署要求
```

运行配置只保存 credential reference 或外部文件引用，不把实际 key/token 写入 JSON、argv、审计或诊断导出。GUI 读取的是脱敏 projection；工作空间绝对路径只用于本机配置管理，不通过发现接口发送给 GPT。

## MCP server 层

`cmd/local-probe-mcp` 固定解析配置、选择 ingress、加载 rootfs 和 audit sink，并将 HTTP 绑定到 loopback。`internal/mcpserver` 使用官方 MCP Go SDK，提供当前已实现的 `server_info`、`ping`、范围读取、目录发现、文件查找、literal 搜索、tree、环境探查和 workspace snapshot 等受限工具。工具是否可见仍由 profile/tool 白名单、root deny/ignore 和 admission 约束。

`internal/cfaccess` 只验证 Cloudflare Access 的签名断言、issuer、audience、时间窗口和 principal 映射。它不把客户端 opaque bearer、Tunnel ID 或请求中的 connection/profile/root 当成身份。

## 文件访问和检索

`internal/policy` 生成绑定 revision 的 scope；`internal/rootfs` 使用平台实际文件句柄/root 边界重新校验普通文件、reparse/link、固定磁盘和身份；`internal/readcore` 只负责有界 ReaderAt/UTF-8/续读算法。`internal/search` 的目录和搜索结果是 bounded、可续页、受 deny/ignore 和预算约束的结果，不是无限扫描。

`internal/catalog` 目前只是默认关闭的本地候选索引证据实现。候选结果仍需要当前 scope/rootfs live verify；不能把 catalog 当成授权、快照或生产索引。

## Windows Preview 层

```text
cmd/local-probe-preview
 ├─ previewapp       配置/日志/diagnostic 的只读模型
 ├─ previewui        原生窗口、字号、托盘、工作空间和导出
 └─ previewconnect   固定 local-probe-mcp.exe + cloudflared.exe 生命周期
```

Preview controller 只启动仓库 `bin/local-probe-mcp.exe` 和固定位置的 `cloudflared.exe`，不会接受模型传入的 executable、argv、env、cwd 或任意命令。它按 MCP 8788 → metrics `/ready` → HA connection 的阶段报告状态；失败时只返回稳定错误类别和解决建议。

Preview 的窗口关闭是隐藏到托盘；退出只停止本次 Preview 自己拥有的子进程。它不会接管手工启动的 MCP/Tunnel，也没有生产 supervisor、服务安装、崩溃无限重试或跨用户 ownership。

## 工作空间和配置修改

Windows Preview 的工作空间管理是本机管理员操作：直接输入路径或打开文件夹选择器，校验固定磁盘普通目录、reparse/link 和父子重叠后，通过 revision 乐观并发保护原子写入 `.runtime/local-probe.json`。撤销授权不会删除文件夹；共享 profile 的 connection 会共同受影响。

GPT 访问范围仍由 profile 的 roots、tools、deny/ignore 和只读策略决定。Preview 不提供文件内容编辑、任意 shell、配置秘密展示或命令放行规则编辑。

## 审计和诊断

`internal/audit` 记录有界结构化事件，`internal/auditreader` 只读取符合命名规则的普通文件，`internal/supportbundle` 和 `internal/previewui` 对展示和导出执行 allowlist、大小/数量边界和第二次脱敏。诊断导出不包含原始配置、文件内容、完整路径、命令行、环境、Tunnel ID、token、JWT 或 key。

## 当前未接通的生产能力

- 真实 WFP adapter、broker/service、EnforcementCapability 铸造和生产网络隔离；
- 可信 launcher、生产 supervisor、安装/卸载、服务恢复和跨用户 ownership；
- MCP `run_probe`、任意命令、写入工具和通用 shell；
- 默认启用的大规模索引、强一致快照和百万文件端到端性能证据；
- 安装器、代码签名、自动更新、正式 release gate 和独立安全审查。

阶段事实和验证限制见 [`IMPLEMENTATION_STATUS.md`](IMPLEMENTATION_STATUS.md)、[`THREAT_MODEL.md`](THREAT_MODEL.md) 与 [`.planning/codebase/ARCHITECTURE.md`](../.planning/codebase/ARCHITECTURE.md)。
