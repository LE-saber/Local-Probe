# 威胁模型与发布闸门

## 当前状态

验证记录更新：2026-09-16。

当前代码为无网络的 K0 读取内核、显式本地文件 demo，以及 P04 的部分安全边界实现。已有 `config`/`policy.BoundScope`、基于 Go 1.25+ `os.Root` 的只读 rootfs、`readcore` bound adapter、R7 的 `ProductionReady=false` local-only metadata candidate catalog、R8 的 `ProductionReady=false` desktopadmin 本地只读 projection，以及 R9 的 `ProductionReady=false` local-only releasecheck/Windows acceptance scaffolding；**P04 的 Windows/WSL2 平台有界验证和一次真实 ChatGPT Business“极高”经 Cloudflare 的 MCP 链路已有记录，但这不是发布放行，也不代表多账号、长期运行、Pro、R7 索引产品验收、R8 桌面产品验收或 R9 发布材料已完成。** 以下仍是必须兑现的安全设计及验收条件，不是已通过的安全认证。

## 信任边界

可信：设备所有者的显式配置、受保护凭据、已认证 ingress 构造的 Scope、经审查的 Source/OS 打开器。

不可信：模型生成的参数、本地项目/README 中的指令、文件名与内容、用户上传的工具输出、网络请求、自报账号/profile 名、未验证的 cursor 和第三方二进制。

拥有本机管理员权限的攻击者、恶意内核或设备所有者主动授予错误 root，不在应用可独立解决的范围内；不能因此忽略常见 symlink/junction/路径竞争和秘密外泄。

## 主要攻击及约束

| 攻击 | 必须具备的防护 | 当前情况 |
|---|---|---|
| README 提示模型读取并外泄私钥 | deny policy 作用于读/搜/概览/命令；工具不自行扩权；数据外传提示 | 待 P04/P05；提示词不能代替 policy |
| 模型把 profile 改成 admin 或伪造 root | 可信 ingress 绑定身份，root/tool 检查，未知连接失败 | 已有一次 Business“极高”经 Cloudflare 的真实 auth/MCP 链路；多账号 principal 映射、长期与生产接线仍待验证 |
| `..`、UNC、ADS、symlink/junction swap | handle-relative root API、平台测试、特殊文件拒绝 | `os.Root`、root/path 校验、逐组件 symlink/reparse 拒绝、Windows handle identity/hardlink/junction、Unicode/组合字符、长路径、ADS/保留名/非法路径、Windows symlink+junction swap、WSL2 FIFO/socket/hardlink/symlink swap 及临时 loopback SMB `GetDriveTypeW=4` remote-root 测试均已在明确环境/次数下通过；本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台和独立审查仍是残余风险 |
| A 账号使用 B 的缓存/游标/任务 | connection/profile revision 绑定，撤权失效，独立配额 | 待 P09，模拟 Scope 测试不算真实多账号 |
| 巨型文件/长行/超量请求拖垮进程 | 单项和整体预算、bounded concurrency、全局 admission、deadline | byte batch 预算已实现；全局调度/wire 限额待实现 |
| Git external diff、PATH 劫持、解释器环境注入 | 固定 action/executable/args、净化 env、禁止任意 shell、进程树终止 | 没有命令工具，待 P08 |
| localhost 管理页面被恶意网站访问 | loopback、认证、Host/Origin、CSRF；不通过 tunnel 暴露管理 API | 没有管理 HTTP 页面 |
| 桌面客户端被误当作控制面或泄露敏感状态 | 本地只读 projection、typed enum action、bounded 脱敏诊断、revision/status 一致性校验；动作在生产 gate 前固定 unavailable | R8 `desktopadmin` 已实现 overview/connection status/developer rule preview/diagnostics；`DispatchAction` 绝不调用 dispatcher，`Exit` 不停止 supervisor；无 listener、GUI/tray、process、credential 或 MCP |
| runtime key 在参数/日志/支持包泄漏 | secret reference、受保护存储、脱敏、admin/runtime 分离 | 没有存储或使用真实凭据 |
| 模型/网络伪造“已发布”或篡改 release evidence | evidence 只能由可信进程内 typed constructor 创建；固定 code/outcome；private report；unknown commit 对 provenance fail closed；wire 只读且有界 | R9 `releasecheck` 已实现；`ProductionReady=false`；当前 acceptance 报告 `release_ready=false`；独立审查、签名、provenance 仍未完成 |
| acceptance 检查借机读取秘密或把大 fixture 写入仓库 | 固定仓库边界；tracked safety 只检查名称和 fixture metadata；stdout JSON 有界；Quick/WhatIf；不自动安装、联网或创建 1m fixture | R9 `scripts/acceptance.ps1` 已实现 Windows 本地入口；真实 signing/Tunnel/web/install/soak/1m 均明确跳过 |
| index 漏掉新文件后声称搜索完整 | freshness/coverage/reconcile，必要时 live scan | R7 local-only catalog 只产生候选；已验证 small/10k/100k 的集合相等和候选 live verify；百万文件/1m 按 2026-09-16 决策不运行，显式能力保留但不作为 R9 入口条件；变化目录、新文件遗漏/损坏回退、physical root identity/ignore fingerprint、overflow fallback 和生产接线仍未完成；默认关闭 |
| 弱 metadata token 被当作强快照 | 标明 strength，强审查使用不可变来源，不承诺仓库事务 | rootfs 已生成 size/mtime/mode 弱 token 并标记 metadata；真实 snapshot 未实现 |

## R7 索引候选的安全边界

R7 的 `internal/catalog` 是本地实验性的 bounded metadata cache，不是授权系统、快照系统或
存在性证明。它只保留 root-relative path 和有限 metadata；不保留正文、绝对路径、句柄或可
跨线传输的 cursor。catalog 必须绑定可信的 connection/profile/revision，并在 dirty、未完成
reconcile、scope 失效或 generation 不一致时 fail closed。查询结果即使页满或为空，也只能
解释为“当前缓存给出的候选”，不能解释为目录不存在或扫描完整。

每个候选必须重新经过当前 `policy.BoundScope` 和 `rootfs.Source` 的 live verify；需要完整性
时仍使用 direct scan 或完整 reconcile。catalog 不得绕过 deny/ignore、reparse/symlink、root
identity、打开与 metadata 校验。watcher 只可提供 dirty hint；overflow、取消、并发 reconcile、
变化目录、新文件遗漏、损坏记录或 live verify 失败时必须禁用候选路径并回退 direct/reconcile。

在任何产品接线前，还必须证明并绑定 physical root identity 和 ignore fingerprint，进行并发
reconcile 的 generation final check，并保留严格 cancellation 语义。百万文件/1m 按本轮决策不
运行，显式 harness 能力保留；不同磁盘类型证据尚未完成；SQLite/FTS 未实现。10k/100k 的 candidate query 局部较快，但 live verify 后
端到端慢于 direct，因此当前保持 `ProductionReady=false`、不接 MCP、默认关闭。

## R8 桌面管理第一增量的安全边界

`internal/desktopadmin` 是未来桌面客户端消费的进程内 projection，不是 listener、管理服务或
授权器。它只接受受信的、进程内、非阻塞的 `ConfigSource`、`StatusSource` 和可选
`DiagnosticsSource`；这些 source 的 `Snapshot`/`Snapshots` 不得做网络或进程 I/O。客户端会
复制并重新校验 source 输出，source 失效、panic、超限或状态不一致时返回 unavailable/fail
closed，不猜测状态。

当前只读面是 bounded overview、connection status、developer rule preview 和脱敏 diagnostics。
diagnostics 不含消息正文、路径、命令/argv、环境、endpoint、token、key 或 credential。revision
接受内存 Store 的 `rN` 与 FileStore 的 `sha256:<64 hex>`；config/status/connection revision
不一致时不应用旧状态。

`start`、`stop`、`reconnect` 只能通过严格 typed request 表达；当前 `DispatchAction` 固定返回
capability unavailable，绝不调用 `ActionDispatcher` 或 supervisor/connectionmanager。`Exit`/
`Close` 只关闭客户端 projection，不停止 supervisor。`ProductionReady=false` 固定不变。
本增量没有 HTTP、named pipe、GUI、tray、process、credential、Tunnel 或 MCP 接线，故不会
新增本地端口或远程管理面。

未来若闭合 R6 production gate，管理入口优先采用 per-user supervisor + Windows 原生 Win32
tray + embedded loopback management page，并优先 named pipe + SID ACL。loopback fallback 必须
同时具备 Host/Origin、CSRF、认证和 CSP 检查；管理入口不穿 Cloudflare Tunnel。当前不引入
Electron、Wails、Fyne 或 Walk；实际控制、托盘和页面仍以后续独立安全审查为准。

R7 的百万文件/1m 测试按 2026-09-16 决策不运行，现有显式 harness 档位保留但不作为 R9
入口条件；这不改变 catalog 默认关闭和 direct fallback 的安全边界。

## R9 发布证据与 acceptance 的安全边界

`internal/releasecheck` 不是签名服务、供应链证明或发布授权器。它只接受可信进程内通过
`NewEvidence` 创建的 typed evidence；固定的 evidence code、outcome 和四个状态位不能由
模型、网络请求或 JSON 直接制造。`Report` 的数据保存在 private snapshot 中，getter 返回副本，
JSON projection 有大小上限且拒绝反序列化；`Evaluate` 重新计算 `release_ready`，不采信输入的
`release_ready` 声明。缺失/pending、hard failure、重复 evidence 或 status conflict 都保持
fail closed；`commit=unknown` 强制 `provenance_missing` hard/pending。`ProductionReady=false`
固定不变，因此即使测试构造出全 pass evidence，也不能推出发布可以进行。

`scripts/acceptance.ps1` 是 Windows 本地检查器，不是安全隔离器。它把仓库边界固定到脚本
所在目录；tracked safety policy 只读取 Git 路径名称，对 fixture 只读取有限 metadata，不读
文件正文；发现 `.runtime`、key/token/secret 名称或超过上限的 fixture 即失败。`-Quick` 只
跳过指定本地重检查，`-WhatIf` 只生成跳过报告；`-Json` stdout 有界。它不联网、不安装、不
签名、不启动 Tunnel/ChatGPT，不生成 1m fixture；真实 signing、Tunnel/web、install/update/
uninstall、双连接 soak 和 1m 均是明确的非自动化 gate。任何成功的本地检查只能说明检查项
本身通过，不能替代独立审查、生产 R4–R8 gate、供应链材料或 release-ready 决策。普通、Quick、
WhatIf 以及错误兜底报告的 `release_ready` 均固定为 `false`。

R9 的状态必须分别记录 `implemented`、`self-tested`、`independently-reviewed` 和
`release-ready`。当前只有前两者可能由本地实现/命令证明；独立审查尚未完成，release-ready
固定为否。仓库当前没有根 `LICENSE`、`NOTICE` 或第三方清单，也没有 SBOM、CVE 扫描、签名、
provenance、portable package、migration rollback、install/update/uninstall 矩阵、一小时双
connection soak、支持流程或 R4–R8 生产硬门。这些是实际阻塞，不可由文档、workflow 定义或
本地报告覆盖。

## 当前已验证的 P04 部分实现

- `config`/`policy.BoundScope` 已覆盖配置上限、重复 key、显式 `enabled` 语义、connection/profile revision 和 root/path deny；deny 支持 Windows 大小写不敏感匹配与 `**`。
- rootfs 从配置快照持有 `os.Root`，只读打开普通文件，逐组件拒绝 symlink/reparse；handle metadata 纳入 Windows volume/file identity、link count 和 attributes，hardlink/junction/reparse 拒绝测试通过；`BoundScope` adapter 让 Engine 的 Open、Metadata、ReadAt 都重新检查撤权。Source revision 也阻止 root ID 复用时旧 Source 接受新 scope。
- root 配置拒绝 UNC，并使用 `GetDriveTypeW` 拒绝 remote drive；临时 `New-PSDrive -Name Z -PSProvider FileSystem -Root '\\localhost\C$' -Persist` 映射中返回 4，`TestWindowsRejectsRemoteMappedRoot` 通过，随后 `Remove-PSDrive -Name Z -Force` 清理且 PSDrive/LogicalDisk/`net use` 均无 `Z:`。identity 查询错误 fail-closed 为 `ErrUnavailable`，不降级到 `m0` 绕过 hardlink/reparse 检查。
- Windows amd64 / Go 1.26.0 已通过 rootfs Unicode/组合字符、长路径、ADS/非法路径、symlink+junction swap（普通 `-count=5`、race `-count=1`）测试；WSL2 Ubuntu-22.04（Linux 6.6.87.2、Go 1.26.2，非裸机）已通过 FIFO/socket/hardlink/symlink swap（普通与 race 各 `-count=5`）及 `go vet ./...`。Linux rootfs 交叉编译也通过。上述均为实现者自测/编译证据，不是独立安全审查；详细命令和结果见 `docs/evidence/P04-platform-verification.json`。

## 生产 Source 放行条件

先锁定受支持 Go 工具链，最低具备 os.Root 或采用经审查的等价句柄相对 API。Windows 与 WSL2 的特殊文件、Unicode/long path、ADS/非法路径、symlink/reparse swap 和临时 loopback SMB remote-root 有界测试已通过；这只表示明确环境和次数下的实现者证据，P04 仍未作为发布闸门放行。仍需处理本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台，并完成独立安全审查。不能将 `EvalSymlinks`、字符串 startsWith 和随后 Open 组合当作抗竞争安全边界。os.Root 也不是完整 OS sandbox，硬链接、挂载、特殊文件和本地管理员风险仍需具体策略。

Windows 的 handle identity、hardlink、junction/reparse、UNC、ADS/非法路径、Unicode/组合字符、长路径和 symlink+junction swap 测试已通过；临时 `Z:` loopback SMB 映射的 `GetDriveTypeW=4` remote-root 测试也通过并完成清理。WSL2 Ubuntu-22.04 已实测 FIFO、socket、hardlink、symlink swap 及 race/vet。上述平台证据仍不覆盖本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台，也不因交叉编译而扩大为“所有平台安全已验证”。

## 网络开放闸门

在生产 Source 的全部 P04 闸门、ingress auth、policy、全局预算、完整响应限制、取消和日志脱敏没有通过对应测试前，不增加可远程访问的 listener。当前已有一次真实 ChatGPT Business“极高”经 Cloudflare 的 MCP 调用记录，但真实 auth/MCP/Tunnel 的单次链路不等于生产 gate；多账号、长期运行、Pro、R4–R8 生产接线仍未完成。Tunnel 只是传输通道；必须单独处理本地 MCP 的认证与权限。模型调用的工具不得修改 roots/profiles/tunnel 配置或取得 key。

## 隐私边界

用户授权的文件片段会发送给 ChatGPT，因此不能宣称“源码永不离开本机”。只读权限仍可泄漏秘密；本地索引、诊断包和日志应默认不保存正文。最终用户需明确选择授权 roots，并按实际账户的数据政策决定是否传输。

## 独立审查

发布前由独立上下文的审查者获得需求、固定 commit、构建步骤和攻击测试目标；要求提供可复现反例或通过证据。本轮仅有实现者自测，未完成独立安全审查。
