# R12 配对与远程通道开发版

日期：2026-10-24。本说明适用于工作树中的后端开发版，不代表公网、不同电脑或 GUI 已验收。

## 程序与能力

| 程序 | 职责 |
|---|---|
| `local-probe-pair` | 本地离线创建身份、指纹检查、签发、安装和撤销 |
| `local-probe-target` | 默认只读 MCP，可选本地审批操作引擎；接收 Tunnel 或主动连接 Relay |
| `local-probe-agent` | 原生 Agent 启动的 stdio MCP 桥接 |
| `local-probe-relay` | 自建服务器上的密文转发，监听回环地址等待 HTTPS 反向代理 |
| `local-probe-operations` | 目标所有者的本地检查、批准/拒绝和恢复材料读取，无远程批准入口 |

默认仍只读。2026-10-24 新增可选真实操作后端，须独立配置和逐次本地批准，见[操作说明](R12_OPERATIONS_BACKEND.zh-CN.md)；GUI 配对/审批尚未接线。

## 配对顺序

下面是操作约定，命令中的路径/连接/指纹均须由实际部署替换。公有文件用 UTF-8 JSON 保存。CLI 不打印私钥，身份目录不得作为普通配置备份或上传到服务器。

1. 目标电脑：`local-probe-pair -action target-init -store <新的绝对身份目录> -approve -offline`。目录的父级须已存在；输出公开 bundle JSON。已有目录或身份不会被覆盖。
2. 对目标输出执行 `local-probe-pair -action fingerprint-bundle`，从 stdin 读取 bundle，保存/独立核对显示的指纹。该指纹必须经可信途径交给 Agent 侧用户，不能仅相信 Relay 同时提供的 bundle 和指纹。
3. Agent 电脑：`local-probe-pair -action agent-init -store <新的绝对身份目录> -approve -offline`，输出公开 CSR 请求。
4. 对 CSR 执行 `local-probe-pair -action fingerprint-request`；将请求文件交给目标所有者，并经独立途径核对这一请求的指纹。
5. 目标配置使用 v2，明确设置一个已启用的 `purpose=agent_remote` 连接，授权到所需的只读 profile。签发命令：`local-probe-pair -action issue -store <目标身份目录> -config <绝对配置路径> -connection-id <连接ID> -fingerprint <已核对的CSR指纹> -approve -offline`。stdin 为 CSR；输出 pairing JSON，包含公开目标 bundle 与签发的 30 天 Agent 证书。
6. Agent 安装：`local-probe-pair -action install -store <Agent身份目录> -fingerprint <第2步独立核对的目标指纹> -approve -offline`。stdin 为 pairing JSON。错误目标、Agent 私钥不匹配、证书元数据被改动均拒绝。

如果 CSR 输出丢失，可用 `-action request -store <Agent身份目录>` 生成新的 CSR；签名随机性会使其指纹变化，必须重新核对。目标 bundle 可用 `-action show` 重新导出。签发后输出丢失时不重新签发同一公钥，避免覆盖既有范围；使用本地的原始公开输出或重新创建新的 Agent 身份。

签发工具必须在使用身份目录的运行时停止后操作。运行中撤销 API 已能取消租约，但 GUI/本地管理 IPC 尚未接入；CLI 的 `-offline` 是用户对已停止运行时的明确声明，不会自行结束任何进程。

## Tunnel 方式

目标端：

```text
local-probe-target -store <目标身份目录> -config <绝对配置路径> -audit-dir <绝对私有审计目录> -listen 127.0.0.1:8791
```

目标进程从本地环境读取 `LOCAL_PROBE_TARGET_ACCESS_TOKEN`。使用至少 32 字节随机凭据，不要把令牌写进命令参数、日志或本说明。将已有受控 Cloudflare Named Tunnel 的 WSS 路径转发到回环 origin 的 `/v1/peer`；本轮没有修改用户现有 Tunnel。

Agent 端的 MCP stdio 命令：

```text
local-probe-agent -store <Agent身份目录> -transport tunnel -endpoint wss://<受控域名>/v1/peer
```

桥接从本地环境读取 `LOCAL_PROBE_AGENT_ACCESS_TOKEN`，其值对应目标的外层准入凭据。Cloudflare Access 服务凭据可另外通过 `LOCAL_PROBE_CF_CLIENT_ID` 和 `LOCAL_PROBE_CF_CLIENT_SECRET` 加载，两项必须同时存在。配置 Agent 的 `command/args` 时保留参数数组，不拼接 shell 命令，也不把凭据放进 args。

这条 WSS 通道承载额外的端点 TLS。官方 Secure MCP Tunnel 继续使用原有标准 MCP 路径，不能把 `/v1/peer` 填进官方 Tunnel 冒充通用字节流。

## 自建 Relay 方式

首版使用静态环境引用路由，示例见 [Relay 配置](../deploy/relay/relay.example.json)。Relay 只知道路由 ID 和外层凭据摘要，不拥有端点 CA 或设备私钥。

服务器：

```text
local-probe-relay -config <绝对Relay配置路径> -listen 127.0.0.1:8792
```

在进程环境中分别设置配置引用的目标/Agent 两个独立高熵令牌；不允许两种角色共享令牌，也不允许多个路由复用凭据。通过用户控制的 HTTPS 反向代理暴露 WSS 的 `/v1/target` 和 `/v1/agent`。代理必须支持 WebSocket Upgrade；不得记录 Authorization、数据帧或工具内容。首版程序拒绝直接监听公网地址，TLS 终止交给已配置的反向代理。

目标电脑：

```text
local-probe-target -store <目标身份目录> -config <绝对配置路径> -audit-dir <绝对私有审计目录> -relay-endpoint wss://<Relay域名>/v1/target
```

此时读取 `LOCAL_PROBE_RELAY_TARGET_TOKEN`，不创建回环监听或公网入站端口。目标维护 4 条主动出站的等待/已配对通道，断线后抖动退避重连。

Agent 电脑：

```text
local-probe-agent -store <Agent身份目录> -transport relay -endpoint wss://<Relay域名>/v1/agent
```

Agent 的 `LOCAL_PROBE_AGENT_ACCESS_TOKEN` 对应 Relay 配置中的 Agent 角色凭据。Relay 按角色绑定的固定路由匹配双方，不接受任意 host、port、URL 或 query 参数。双方匹配后，Agent 和目标直接进行内层双向 TLS；路由发错到别的目标时仍因设备公钥不匹配而失败。

如使用用户自有的私有证书颁发机构，Agent 和 Relay 模式的目标端可显式提供 `-outer-ca-file <绝对公开CA PEM路径>`。只接受 CA 证书，不接受私钥，不跳过域名验证；未提供时使用正常系统信任库。

### 首版 Relay 的设计调整

计划书中的单独控制连接/一次性票据机制暂以**已认证、按角色静态绑定的 WSS 等待通道**实现第一增量。原因是首版不需要远程动态注册或动态路由管理，省去可以扩张路由权限的控制 API。每一对数据通道仍只连接固定路由的双方；它不具备任意端口转发能力。

容量：每路由最多 16 条目标通道、全局 128 条；未配对等待 30 秒后关闭。数据消息最多 64 KiB；配对后的流只有二进制密文。活跃通道每 20 秒心跳，60 秒读期限；读写异常关闭两侧。入口按实际对端地址限制速率，忽略 forwarded headers；反向代理后多个客户端暂共享来源预算。

2026-10-24 已补充 Linux systemd 测试部署脚本、窄范围 TLS 前端及真实公网只读验收，见[部署记录](R12_RELAY_DEPLOYMENT.zh-CN.md)。动态设备登记、运行期凭据管理、完整运维指标和证书自动续期仍未完成，不能把这个基础 Relay 称为生产服务。

## 撤销与存储

离线撤销：`local-probe-pair -action revoke -store <目标身份目录> -key-pin <签发结果中的公钥pin> -approve -offline`。撤销文件只写一次，重启后仍拒绝；同一公钥不能通过重新签发切换范围。新配对使用新 Agent 私钥。

Windows 私有记录使用当前用户 DPAPI，并在新建目录设置当前用户/SYSTEM 的受保护 DACL。Unix 首版依赖 0700 目录和 0600 文件，不声称对用户本人/root 加密。记录写入失败/损坏会拒绝加载，不自动生成新身份。

运行端读取独立的目标叶密钥及公开记录，不需要加载 CA 签名私钥。它是职责分离，不是抵御当前 OS 用户的隔离：将来的 `trusted_host` 脚本使用同一用户权限，可能访问该用户的身份存储；不能把这个模式宣传成沙箱。

此版本无身份备份恢复功能。不要复制旧身份目录来恢复授权，否则可能丢失撤销记录；应生成新身份并重新配对。
