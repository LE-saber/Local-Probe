# R4 Windows `network=deny` 可行性与最小实现设计

状态：设计稿，未实现，未修改 Windows 防火墙/WFP 配置

日期：2026-09-11

本文只回答 Windows R4 的 OS 级网络拒绝问题。它不是已实现功能清单，也不授权
当前版本开放 `run_probe`。在本文对应实现和验收完成以前，当前 profile 中的
`network=deny` 仍然只是一个 fail-closed 的准入条件；没有得到网络隔离证明时，
命令必须拒绝启动。

## 1. 结论先行

推荐的第一条可行路线是：

1. 保留现有 Windows 固定 PE/句柄守卫、挂起启动和单进程 Job。
2. 新增一个只处理本地固定 profile 的 Windows network broker。broker 使用
   **WFP user-mode dynamic session** 建立短生命周期的拒绝过滤器；不使用
   `netsh`，不把临时规则写入持久 Windows Firewall policy。
3. broker 由已安装的、签名的、低权限 Windows service 承载，运行身份优先为
   `LocalService`/virtual service account，并使用 restricted service SID；不以
   `LocalSystem` 作为常规运行身份。安装或授予 WFP 对象 ACL 需要一次管理员
   操作，运行期仅允许严格的内部 profile ID/variant ID，不接受路径、argv、env
   或任意 PID。
4. 由 broker 自己创建挂起的目标进程，把它放入 `ACTIVE_PROCESS=1`、禁止
   breakaway、kill-on-close 的 Job；在恢复主线程以前安装并核验 IPv4/IPv6 的
   outbound、inbound/listen、bind/resource-assignment 拒绝过滤器。
5. 只有在过滤器覆盖、目标映像身份、进程身份、Job 归属和本轮 Windows 网络
   回归测试均得到证明时，才向现有 `commandprofile` 注入一个不可序列化的
   enforcement capability。否则返回 `network_enforcement_required`，不启动。

AppContainer 仍保留为兼容性实验/特定已打包工具的可选后端，不作为普通桌面 CLI
的默认解决方案。Windows Firewall 的临时程序规则可作为安装诊断或明确持久策略，
但不作为一次 probe 的默认实现。

这个结论是有条件的：**WFP 的 app-path 过滤本身不能自动证明 DNS Client 服务、
代理、已有连接或其它系统 broker 的副作用也被隔离。** 如果实机测试发现某一
profile 的 DNS/代理/loopback/已有 flow 不能被同一准入证明覆盖，该 profile 必须
保持 local-only/disabled；不能把“目标进程没有直接 socket”写成“主机没有网络
活动”。

## 2. 定义与威胁模型

### 2.1 R4 中 `network=deny` 的操作定义

对一次 probe，`network=deny` 至少要同时成立：

- 目标进程在恢复主线程之前已经受到 OS 网络策略约束；
- IPv4 和 IPv6 的出站 connect/首包、入站 accept/listen、端口 bind 和适用的
  非 TCP 流量都被拒绝；
- `127.0.0.1`、`::1`、其它本机地址没有隐含 allow；loopback 也属于网络；
- UDP/TCP DNS、mDNS/LLMNR、DoT/DoH、HTTP(S) 代理、WebSocket、QUIC、ICMP/
  raw socket 等测试项不会绕过声明的覆盖范围；
- 目标进程不能通过子进程、Job breakaway、脚本 wrapper、外部 helper、项目配置
  或环境变量把网络动作转移给另一个未受控进程；
- probe 结束、取消、超时、broker 崩溃、目标崩溃或 BFE 会话中断时，目标进程
  先被终止/回收，临时策略随后被清理；不能留下永久拒绝或永久放行；
- 若以上任何一项只能得到 `unknown`，准入结果不是 `verified`，而是拒绝。

这不是“阻止用户在机器上联网”，也不应影响 Local-Probe 本身的 MCP/Tunnel
连接。策略必须只绑定到本次受控目标的可验证身份；如果只能通过阻断整机、用户、
`svchost.exe`/DNS Client 或全部 loopback 来达成效果，则该方案不符合最小影响
要求，应停止实现并重新评审。

### 2.2 需要防御的参与者和假设

| 参与者/故障 | 保护目标 | 不能声称的范围 |
|---|---|---|
| ChatGPT/远端模型提供任意文本、路径、argv、env、cwd | 远端不能改变命令和网络策略 | 本地管理员或 SYSTEM 可修改本程序/策略 |
| 被探查的 `.exe` 有 bug、会读取配置、尝试联网或启动子进程 | 只运行固定 profile，且网络策略先于 resume 生效 | 不等同于通用恶意代码沙箱 |
| 当前用户的普通并发进程 | 不应被 probe 的临时规则误伤 | 同路径/同身份规则的潜在碰撞需实测并拒绝 |
| 进程替换、reparse、硬链接、同名 wrapper | 实际映像与审核身份一致 | 内核 rootkit、驱动、管理员可绕过 OS 安全边界 |
| broker、目标或 BFE 崩溃 | 子进程和动态过滤器可回收 | 动态过滤器移除与 Job kill 的极短时序需实机证明 |
| VPN、企业策略、第三方防火墙 | 发现过滤优先级/冲突并 fail closed | 不假设所有机器的 WFP provider 行为相同 |
| DNS Client、WinHTTP/系统代理、COM/RPC broker | 不把系统代理活动误报为目标网络已隔离 | 不能凭 app-path filter 单独推导 DNS 服务行为 |

当前 Local-Probe 的信任模型是单机所有者管理的本地连接，不是多租户安全边界。
因此 broker 的 IPC、配置 ACL、service SID 和本地确认仍是硬门；“只绑定本地”
不是“任何本地用户都可信”。

## 3. 四种候选方案比较

| 方案 | 隔离强度 | 普通 Win32 CLI 兼容性 | 管理权限/副作用 | 进程级绑定 | 崩溃清理 | 结论 |
|---|---|---|---|---|---|---|
| AppContainer | 强，网络和资源按 capability/identity 隔离 | 低到中；需 package identity、manifest、ACL，普通 CLI 常依赖用户目录/注册表/临时目录而失败 | 打包/ACL/loopback 可能需安装或管理员动作；manifest 是产品负担 | 以 AppContainer identity 绑定 | OS 容器可回收，但包装和 helper 仍需审计 | 仅作已打包工具的可选后端 |
| WFP dynamic session | 可按 app ID + user SID 在 ALE 层拒绝；可覆盖 v4/v6 | 高；目标仍是普通 Win32 PE | 打开/写入 WFP 对象需相应 ACL；动态对象本身不持久 | app path 不是 process instance，须叠加专用身份、挂起启动和 Job | dynamic session 对象随 session 关闭/崩溃自动删除；Job 负责目标 | **最小推荐原语**，但 DNS/已有 flow 必须实测 |
| Windows Firewall 程序规则 | 可按程序路径阻断 outbound，策略由 WFAS/WFP 仲裁 | 高 | 通常需要管理员；规则默认是系统策略对象，误删/崩溃可能遗留；会影响同路径进程 | 路径级而非实例级；规则冲突和优先级复杂 | 默认无“随进程崩溃自动清理”保证 | 不用于 transient probe；只作明确持久政策 |
| 低权限 broker/service | 本身不是网络过滤器；可集中托管 WFP、Job、配置和审计 | 高；broker 可继续启动普通 CLI | 安装服务/授予 WFP ACL 需要一次管理员；运行身份可为 LocalService/virtual account | broker 自己创建进程、专用 token/SID + app ID | broker 持有 Job 和 dynamic session；崩溃路径必须实测 | **推荐承载方式**，但不能以 LocalSystem 逃避 ACL 设计 |

### 3.1 AppContainer：为什么上次实验不适合直接采用

AppContainer 的目标就是隔离资源和网络；但桌面 Win32 程序进入 AppContainer 后，
文件/注册表/凭据/临时目录/用户 profile、COM/RPC、helper 进程和 loopback 都
可能受到 package identity、manifest capability 和 ACL 的影响。微软文档指出，
桌面应用只有在 manifest 明确为 AppContainer 时才处于该信任级别；普通 MSIX
桌面应用通常仍是 medium IL/full trust。打包应用的 loopback 也默认受限，不能把
“能启动一个空样例”当作 CLI 兼容性证据。

本项目上次已经观察到：普通 Win32 CLI 在 AppContainer 后不可用。这个事实不是
实现细节瑕疵，而是说明“任意外部 CLI + AppContainer”不能作为 R4 通用契约。
若未来支持 AppContainer，必须每个 profile 单独准备 package identity、声明最小
capability、给 executable/dependency/private temp 目录加 ACL，并验证 loopback/
stdout/stderr/版本输出；未通过的 profile 不能回退到 unrestricted 启动。

参考：

- [AppContainer isolation](https://learn.microsoft.com/en-us/windows/win32/secauthz/appcontainer-isolation)
- [App capability declarations](https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/app-capability-declarations)
- [Windows desktop app loopback/IPC notes](https://learn.microsoft.com/en-us/windows/apps/develop/communication/interprocess-communication)

### 3.2 WFP dynamic session：优点和边界

WFP user-mode API 可打开带 `FWPM_SESSION_FLAG_DYNAMIC` 的会话；在该会话添加的
对象会在显式关闭或 client process 结束时由 BFE 自动删除。应使用这一点代替临时
写入 WFAS 的持久规则。WFP 的 ALE 层支持：

- `FWPM_LAYER_ALE_AUTH_CONNECT_V4/V6`：出站 TCP connect 及出站非 TCP 首包；
- `FWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4/V6`：入站 TCP accept 及入站非 TCP 首包；
- `FWPM_LAYER_ALE_RESOURCE_ASSIGNMENT_V4/V6`：端口分配、bind、raw/promiscuous
  等资源申请；
- 必要时使用相应 discard 层/网络事件查询观察拒绝是否命中。

过滤条件最低包含 `FWPM_CONDITION_ALE_APP_ID`，并在 broker 为每次运行建立专用
用户/服务身份时叠加 `FWPM_CONDITION_ALE_USER_ID`。微软将 `ALE_APP_ID` 定义为
应用完整路径，而非一次运行的 PID；所以 app ID 不能单独证明“只封这一实例”。
现有 launcher 的实际映像核验、专用身份、挂起状态和 Job 是 WFP 方案的必要组成，
不是可省略的重复检查。

WFP 的已授权 flow 可能在建立后继续存活；因此过滤器必须在目标 resume 前生效，
已有连接不得被接受为成功。若一个 profile 可能在启动前已有网络句柄、通过服务
代理联网或改变网络策略，必须在 profile 中禁用，不能靠 resume 后补过滤器。

参考：

- [WFP architecture](https://learn.microsoft.com/en-us/windows/win32/fwp/windows-filtering-platform-architecture-overview)
- [ALE filtering layers](https://learn.microsoft.com/en-us/windows-hardware/drivers/network/run-time-filtering-layer-identifiers)
- [Filtering condition identifiers](https://learn.microsoft.com/en-us/windows-hardware/drivers/network/filtering-condition-identifiers)
- [WFP object management and dynamic sessions](https://learn.microsoft.com/en-us/windows/win32/fwp/object-management)
- [WFP access control](https://learn.microsoft.com/en-us/windows/win32/fwp/access-control)

### 3.3 Windows Firewall 程序规则：为什么不作为一次运行的默认实现

Windows Firewall 的 outbound program rule 可以按完整程序路径 block all outbound
traffic；但是这是 WFAS policy 对象，默认不是进程结束自动撤销的临时租约。添加/删
除还要处理管理员权限、规则名/ GUID 冲突、并发实例、第三方 provider、组策略及
企业设备上的既有 allow/block 仲裁。程序路径也不是 PID；同一 `.exe` 的其它实例
会被一并影响，替换后的同路径文件可能继承规则效果。

因此不能在 probe 里调用 `netsh advfirewall ... add rule`，运行成功后再假定 defer
删除就足够：崩溃、断电、服务升级、UAC 失败和规则覆盖都可能留下系统状态或让
目标没有得到预期拒绝。若未来提供“永久阻断某个固定工具”的管理功能，应将其明
确标为持久系统设置，由 GUI/管理员确认、显示影响范围并提供可验证回滚；它不属于
R4 `network=deny` transient capability。

参考：[Configure Windows Firewall rules](https://learn.microsoft.com/en-us/windows/security/operating-system-security/network-security/windows-firewall/configure)

## 4. 最小推荐实现

### 4.1 分层

```text
MCP/model (future)
  └─ only action/profile_id + variant_id + local-confirmation reference
       └─ policy admission (developer mode, connection, revision, SHA/signature)
            └─ signed local broker IPC (named pipe, user/service ACL)
                 ├─ trusted profile resolver; no raw path/argv/env/cwd
                 ├─ Windows launcher: CREATE_SUSPENDED + PE/handle identity
                 ├─ Job: ACTIVE_PROCESS=1, no breakaway, KILL_ON_CLOSE
                 └─ WFP dynamic session: v4/v6 connect, accept/listen, bind block
```

broker 的常规运行身份优先为 `LocalService` 或 per-service virtual account，
并开启 restricted service SID；不使用 `LocalSystem` 作为“权限不够”的捷径。服务
SID 只用于访问 broker 私有目录、named pipe、Job/IPC 和配置对象。WFP engine/filter
的最小 ACL 必须在安装阶段由签名的管理员 helper 配置；如果无法把 add/close 所需
权限精确授予该 service SID，就不要把服务提升为全能管理员，而是保留 disabled，
重新评估一次性 UAC helper 或 AppContainer 特定 profile。

安装服务是持久系统变更，必须有显式本地管理员同意、卸载/回滚和诊断；**运行一次
probe 不应新增持久 Windows Firewall rule、dynamic keyword address、代理设置、
DNS 设置或全局网络开关。**

### 4.2 生命周期顺序

1. 本地 UI/CLI 完成一次性 confirmation；MCP 不能自己生成确认。
2. policy 根据 profile ID、variant ID、profile revision 和 identity digest 解析
   固定绝对 `.exe`、固定 argv、空环境、私有 cwd、预算；模型输入不参与拼接。
3. broker 检查 PE/regular/non-reparse、文件 handle identity、执行期 SHA-256 或
   Authenticode；不通过即停止。
4. broker 创建专用 restricted token/服务身份下的目标进程，显式 application name，
   `CREATE_SUSPENDED`；立即加入 Job 并设置 active process limit 1、kill-on-close、
   禁止 breakaway、无窗口和输出管道。
5. broker 打开唯一的 WFP dynamic session，按本次 profile/identity 安装并查询
   v4/v6 过滤器。没有完整的 filter receipt，不得 resume。
6. broker 在 resume 前做一次 attach/identity/Job/session 一致性检查；成功后才
   resume。失败路径先终止/等待 Job，再关闭 dynamic session。
7. 执行期间同时受 wall/output/admission budget、Job 和 dynamic session 保护；
   不允许目标修改 WFP policy、创建 breakaway child 或调用未审计 helper。
8. 正常退出、取消、超时、broker 失联和 BFE 错误都走同一 revoke/kill/close 路径；
   关闭后查询没有遗留动态对象，且没有目标进程仍存活。

### 4.3 不能伪造的 attestation

只有 broker 内部可以生成下面的不可序列化 capability；配置、JSON、MCP 参数、模型
文本和普通 Go caller 都不能设置它。建议内部契约如下，字段名可在实现时调整：

```text
NetworkGuard.Begin(admission) -> Lease
NetworkGuard.Attach(Lease, suspended-process-handle) -> Attached
NetworkGuard.Activate(Lease) -> Attestation
NetworkGuard.Revoke(Lease, reason) -> CleanupReceipt
```

`admission` 只含：`profile_id`、`profile_revision`、`variant_id`、确认摘要、请求
nonce、deadline、已验证的 executable identity digest。它不含 raw path、argv、env、
cwd 或远端 token。

`Attestation` 至少包含以下内部布尔/枚举状态：

```text
provider: "wfp.dynamic"
process_bound: true
ipv4_outbound: verified
ipv6_outbound: verified
ipv4_inbound: verified
ipv6_inbound: verified
bind_and_listen: verified
loopback: verified
existing_flows: none_or_terminated
dns_and_proxy: verified | unknown
child_policy: active_process_1
cleanup: job_kill_on_close + dynamic_session
policy_revision: opaque revision
```

`dns_and_proxy`、`existing_flows` 或任一 v4/v6/loopback/bind 状态为 `unknown` 时，
`Activate` 必须失败；调用方不能将部分结果转换为 `EnforcementCapability`。只有
所有状态为 verified/none_or_terminated 且过滤器查询与目标身份一致，才注入本地
opaque capability。这个 capability 不可 JSON 序列化、不可从配置反序列化、不可由
MCP 传入，也不应包含路径、完整 PID 或密钥。

建议的稳定错误类别：

- `network_enforcement_required`：没有得到完整证明；不启动目标；
- `network_policy_conflict`：WFP/企业 provider 仲裁结果不确定；不启动；
- `network_filter_install_failed`：任一 v4/v6/filter layer 未安装；不启动；
- `network_scope_unknown`：DNS、代理、loopback、已有 flow 或 child 覆盖未知；不启动；
- `network_cleanup_failed`：先报告失败并停止新增 probe；保留脱敏诊断，不能自动
  放宽为无隔离执行。

## 5. 权限、持久副作用与故障清理

### 5.1 管理员权限边界

- 安装/卸载 Windows service、创建/更新 service SID、安装签名 broker 和授予其
  私有目录/pipe ACL：需要管理员或安装器的显式 UAC。
- 创建 WFP provider/sub-layer、设置其安全描述符、为 service SID 授予最小
  `FWPM_ACTRL_*` 权限：按 BFE 的 access check 验证，通常需要管理员/Network
  Configuration Operators 等受控权限；不能假设普通用户可以写全局过滤器。
- 运行期只允许 broker 打开 dynamic session、添加与自身 profile 绑定的短期
  filters；不能写 persistent/boot-time filter，不能改系统防火墙 default policy。
- 任何一次权限不足、BFE stopped、第三方策略冲突都返回 unavailable/required，
  不自动弹 UAC 后继续执行，也不以 LocalSystem 替代设计。

### 5.2 崩溃和取消清理

清理顺序固定为：停止接受新请求 → 终止并等待目标 Job → 关闭输出和进程句柄 →
关闭 WFP dynamic session → 读取/确认本次 provider/filter 不再存在 → 写脱敏 audit。

WFP dynamic session 的优势是 session 结束时由 BFE 删除 session 对象，即使 client
process 崩溃也不会像持久 Firewall rule 一样永久留在系统中。但这一点不自动保证
目标进程已终止：Job 的 `KILL_ON_JOB_CLOSE` 必须由 broker 持有，且 broker 崩溃回归
必须确认目标不能在 filter 清理后继续运行。若实测存在可利用窗口，dynamic session
方案不能被标成 verified；应暂时禁止 profile，不能靠日志解释为“基本安全”。

### 5.3 对现有系统网络的影响

不得为一次 probe：

- 暂停/停止 DNS Client、BFE、WinHTTP、VPN、Cloudflare tunnel 或其它全局服务；
- 写入全局代理、路由、DNS、hosts、Firewall default policy；
- 添加未带明确 owner/expiry 的持久 firewall rule；
- 阻断 `svchost.exe`、全部 loopback 或整个用户的网络来伪造 process-level deny。

如果只能通过上述方式覆盖 DNS/代理，当前 profile 必须停用。后续若产品要支持明确
的系统级离线模式，须另立管理功能、强提示、管理员确认、状态恢复和独立审查，不能
混入 R4 probe。

## 6. Windows 验收方案（不包含 Linux）

所有测试在隔离的 Windows 测试机/快照进行；测试程序只返回成功/失败和计数，不能
输出 token、密钥、Cookie、源码正文或完整命令行。当前阶段只写方案，不执行系统
防火墙变更。

### 6.1 静态和单元测试

- filter builder 只能生成固定 layer/action/condition；拒绝任意 layer、provider、
  address、port、weight、persistent/boot-time flags；
- admission/lease 绑定 profile ID、revision、variant、identity digest、nonce、
  user/service identity 和 expiry；篡改、重放、跨 connection 均失败；
- `Attestation` 未全部 verified 时不能构造 opaque capability；JSON marshal/unmarshal
  必须失败；
- cleanup 在每个中途错误上幂等；重复 revoke、失联、超时和 BFE error 不会静默
  转为 success；
- Windows Firewall COM/netsh、AppContainer fallback 和 WFP backend 不能在普通
  `run_probe` profile 中被动态选择。

### 6.2 网络覆盖矩阵

在目标进程内尝试以下动作，每项应得到拒绝/超时且对应 WFP 事件能归属于本次 policy：

| 类别 | 至少测试 |
|---|---|
| IPv4/IPv6 outbound | TCP connect、UDP send、dual-stack `AF_UNSPEC`、QUIC/UDP |
| IPv4/IPv6 inbound | listen、accept、UDP bind/receive、dual-stack socket |
| bind/raw | TCP/UDP bind、raw socket、ICMP、promiscuous/特殊 socket（不支持则明确拒绝） |
| loopback | `127.0.0.1`、`::1`，已监听端口和新 listen |
| name resolution | cache miss 的 A/AAAA、UDP/TCP 53、mDNS 5353、LLMNR 5355、DoT/DoH 尝试 |
| higher-level APIs | WinHTTP/WinINet、系统代理、TLS、WebSocket、named-pipe-to-network proxy |
| existing flow | resume 前创建的 socket/连接、目标继承句柄、连接复用 |
| process escape | child process、`CREATE_BREAKAWAY_FROM_JOB`、wrapper、COM/service helper |

DNS Client service 会代表应用解析名称；因此必须把 cache hit 与 cache miss 分开，
并观察实际网络事件/抓包归属。仅看到目标 `connect` 返回失败不足以证明 DNS 没有
出网。若 `dns_and_proxy` 无法得到 verified，目标 profile 不得开放。

### 6.3 生命周期与对抗测试

- filter 安装前、安装后未 resume、resume 瞬间、运行中、正常退出、输出超限、超时、
  用户取消分别 kill broker/目标，确认没有活跃目标和残留 dynamic objects；
- 在挂起后替换、rename、delete、reparse、hardlink/ACL 改变 `.exe`，确认不会
  resume；
- 同一可执行文件并发启动两个不同 lease，确认 identity/过滤器/清理互不串线，
  另一个普通实例不被误伤；若无法保证，拒绝该身份绑定方式；
- 重启 BFE、断开网卡、切换 IPv4/IPv6、睡眠/唤醒、启用 VPN/企业 firewall provider，
  确认策略冲突转为 fail closed；
- service 被 `TerminateProcess`、崩溃、更新、服务控制管理器停止或系统关机，确认
  Job kill 和 dynamic session cleanup；
- 使用 `netsh wfp show state` / `netsh wfp show filters` 只读观察本次测试的过滤器
  和 provider owner；测试结束后确认没有持久 rule、dynamic keyword、代理/DNS/路由
  变更。诊断输出必须脱敏。

### 6.4 验收停止条件

以下任何一项发生，停止 R4 network implementation，并保持远程 `run_probe` 关闭：

1. 过滤器只覆盖 IPv4、只覆盖 TCP、只覆盖 connect，或 loopback/listen/bind 有遗漏；
2. App ID/path 绑定无法区分本次目标与并发实例，或同路径替换可运行；
3. DNS Client、代理、已有 flow、COM/RPC/helper 的网络动作无法证明被覆盖；
4. WFP 权限只能通过 LocalSystem、持久全局规则或阻断整机/用户网络取得；
5. broker 崩溃后目标仍可能存活并获得网络，或动态对象/防火墙规则残留；
6. 第三方/企业 WFP provider 发生仲裁不确定但实现仍返回 verified；
7. 目标可创建 child/breakaway/helper，或输出/日志暴露完整 path、argv、env、stdout、
   stderr、token/key；
8. 任一 profile 需要把模型传来的“confirmed=true”当作本地确认，或能通过配置
   JSON 伪造 enforcement capability。

## 7. 后续 Luna 实现的 API 契约（只供本地核心）

下列是实现边界，不是当前代码 API；未来实现可改名，但不能扩大语义：

```text
NetworkGuard.Begin(TrustedAdmission) -> OpaqueLease | error
NetworkGuard.Attach(OpaqueLease, SuspendedProcessHandle) -> AttachedReceipt | error
NetworkGuard.Activate(OpaqueLease) -> VerifiedAttestation | error
NetworkGuard.Revoke(OpaqueLease, reason) -> CleanupReceipt | error
```

约束：

- `TrustedAdmission` 只能由本地 policy 产生：`profile_id`、revision、variant ID、
  confirmation digest、request nonce、deadline、identity digest；不得有 raw command/
  argv/path/env/cwd/port/address/PID；
- `SuspendedProcessHandle` 必须是 broker 自己创建且尚未 resume 的真实 handle；不接受
  模型或普通远程调用传入的数字 PID；
- `Begin` 创建 dynamic session 和带 owner/expiry 的 filter draft；`Attach` 核对
  PE/handle identity、process creation time、token/service SID、Job；`Activate` 在
  所有覆盖项 verified 后才允许 resume/生成 capability；
- `Revoke` 必须先 terminate/wait Job 再关闭 WFP session；任一 cleanup 失败返回
  `network_cleanup_failed` 并阻止新的命令准入；
- Lease、receipt、attestation 和 capability 均不可 JSON 序列化、不可写入配置、不可
  由 MCP 参数构造；日志只写稳定 error、policy revision、状态和 HMAC digest；
- 不为“方便调试”增加 `SetRule`、`AllowPath`、`AllowAny`、`DisableNetworkCheck`、
  `RunRawCommand` 或 `AdoptPID` 等逃生 API。

## 8. 与当前实现的边界

当前仓库已实现 Windows 固定 PE/路径/句柄身份守卫、挂起进程复核、单进程 Job、
私有 cwd/空环境、输出/超时限制和 command profile/confirmation 核心；这些不等于
OS network deny。当前也没有 WFP broker、服务安装器、网络 attestation、profile→probe
生产接线或 MCP `run_probe`。

因此本设计完成后可声称：候选方案、威胁模型、权限和停止条件已冻结；不能声称：
Windows 命令已经断网、Cloudflare/GPT 可以安全运行 `codex -v`，或系统防火墙已被
配置。下一次实现必须先在隔离 Windows 环境完成本文件第 6 节的证据，再由 parent
review 后决定是否开放 R4 的本地 capability，最后才考虑远程工具契约。
