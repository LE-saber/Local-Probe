# R11 后端实施与验证进展

更新日期：2026-10-07。分支：`feat/backend-r11`。用户已批准执行第 2 版计划，主 agent 直接实现。当前是**第一增量，完整后端未交付**，不是发布验收通过；离线 deny/ignore 规则已接入 R11.1 GUI，其它执行能力仍未接入。

## 2026-10-07：GUI 规则接入与网络实验暂缓

用户决定暂时放弃网络检查。本轮不执行 AppContainer/LPAC/WFP 专项实验，也不改系统策略；保留历史失败记录和产品执行关闭状态。未完成的执行链不能仅通过前端按钮开放。以下为本轮已接入的真实后端增量：

- 本地 `internal/desktopbridge/rules.go` 提供严格参数校验的 `rules.read/preview/apply`；只操作当前选择的连接。读取、预览限时；保存使用既有异步忙状态门、配置锁与 revision CAS。
- GUI 分别编辑 profile/root 自身规则，不把叠加投影写回某一层。预览使用 `workspaceadmin.PreviewRules` 的真实 policy 结果；显示路径允许/拒绝以及发现忽略，deny 与 ignore 语义分开。
- 保存要求完整共享连接影响确认、其它实例离线声明；先停止自有控制器，不自动重连。GUI 无法证明其它进程已停止，仍为可信配置目录的离线管理，不是在线全局撤权。
- 后端测试覆盖真实保存与 policy 重载、停止失败、CAS 冲突、共享确认、清空/no-op；前端 19 个 VM 场景覆盖 RPC 参数、输入/revision 绑定、草稿恢复及输出转义。末次源代码修改后全项目 test/race/vet PASS，可选系统隔离/真实 Tunnel 测试未运行。
- 独立 `bin/local-probe-desktop-r11.1.exe` 已构建并做原生窗口规则读取/路径预览检查；暗色编辑框文字颜色修正。没有修改用户现有配置、停止用户 GUI 或启动真实连接。

此增量不包含命令运行、产品版本探查入口、真实多连接并发、工作文件写入、构建测试或任意脚本。操作说明见 [桌面说明](DESKTOP_PREVIEW.zh-CN.md)。下方 2026-10-06 实验和七项未交付状态仍有效。

## 本轮实际新增

1. `internal/workspaceadmin/rules.go`：按明确连接查看/编辑当前 profile 或登记 root 的 deny/ignore；部分字段替换、显式清空、共享连接影响确认、严格限量校验、配置 CAS 与持久化。影响列表包括暂停 root/禁用连接。
2. `PreviewRules`：复用实际 `policy.Manager/BoundScope` 对用户提供的有界规范路径判定；deny 优先，ignore 不是访问拒绝；不读取或扫描 workspace。
3. `cmd/local-probe-admin`：本地查看、预览、离线保存 CLI。无 listener、凭据解析、命令执行或远程确认。必须明确选择绝对配置路径；apply 必须有 offline/approve、相同 revision、完整影响列表。
4. 修复 Win32 配置 mutex 的线程所有权：从获取到释放固定 OS 线程，避免 Go goroutine 迁移造成互斥失效。使用该 mutex 的原有 workspace 管理也受益。
5. 新增单元、真实 CLI 子进程、真实 Windows 跨进程 CAS、线程锁、竞态和 JSON 模糊测试；修改后重新加载配置并检查实际 policy 决策，不用 UI 状态代替访问结果。

配置 FileStore 的生产安全门没有移除；本增量仅用于可信私有配置目录的离线管理。没有完成运行期撤权、硬化文件持久化或 write_allow/execute 范围，不能把第一增量当成完整高级规则验收。

## 七项必交付状态

| 功能 | 当前可验证状态 | 仍需完成 |
|---|---|---|
| 高级开发者命令 | 配置/规划核心保持；真实执行仍关闭 | 受限 worker、WFP/文件隔离、逐次本机确认、固定命令执行、MCP 入口与失败回收 |
| 工具版本探查 | 既有本地 probe 回归及本机 Node/Python/PowerShell 固定版本探查通过，未新增真实用户调用入口 | 真实工具身份注册、隔离证据、本机确认及 CLI/MCP 接线 |
| 多连接同时运行 | 既有协调核心回归通过 | 两个真实独立运行实例、runtime factory/admission 接线、撤权互不干扰、故障恢复与 soak |
| 高级文件规则 | 离线 deny/ignore 编辑、预览、影响确认与持久化可用；2026-10-07 接入 GUI | 在线撤权/授权 epoch、所有管理入口一致性、write_allow/任务范围及任务输入执行 |
| 工作文件写入 | 未实现 | 独立写授权、强内容版本、预览/确认、Windows 原子提交、竞争与恢复保护 |
| 构建/测试 | 未实现 | 受控私有项目副本、真正编译/测试及子进程树、离线缓存、产物和受授权写回 |
| 用户自选脚本 | 未实现 | 四类运行时适配、内容/输入摘要确认、真实隔离、输出/取消/超时/回收与写回 |

上述空缺没有通过修改布尔开关、降低限制或测试 helper 冒充真实产品能力。搜索索引、更新、agent config 导入继续不进入本阶段必做范围。

## 本轮实际检查

环境：Windows amd64，Go `go1.26.0`；本次工具进程有管理员权限，结果不代表非管理员/其它电脑/裸机 Linux。测试配置全部由独立临时目录创建，不使用用户现有 `.runtime`/凭据。`GOPROXY=off`、`GOSUMDB=off`；未联网安装依赖。显式清空可选 live Tunnel 和 DPI WebView 集成开关，因此没有新的外部 Tunnel/ChatGPT 或多机 DPI 验证。

| 检查 | 本轮证据 |
|---|---|
| `go test -count=1 ./...` | PASS；末次源代码修改后重跑，无缓存替代 |
| `go test -race -count=1 ./...` | PASS；其后 CLI 新增 help/子进程/fuzz 测试再次执行 `go test -race -count=1 ./cmd/local-probe-admin ./internal/workspaceadmin`，PASS |
| `go vet ./...` | PASS；末次源代码修改后重跑 |
| Windows 真实跨进程规则 CAS | 两个真实测试进程操作同一临时配置；一个成功、一个 revision 冲突，落盘内容核对成功 |
| Windows mutex 线程/排他/释放 | 实际 GetCurrentThreadId、调度切换、另线程取消及释放后重新获取，PASS |
| CLI 真实子进程流程 | 生产 main 入口：show→apply→另进程 show→旧 revision apply 拒绝，PASS；不是外部 MCP 或 privileged worker 测试 |
| `go test -count=1 -fuzz=FuzzLoadRuleRequest -fuzztime=5s -parallel=2 ./cmd/local-probe-admin` | PASS，152,642 次执行；短时探测，不是穷尽证明 |
| `go build -trimpath -buildvcs=false -o bin/local-probe-admin-r11-rules-dev.exe ./cmd/local-probe-admin` | PASS；新独立文件，没有覆盖旧 GUI/MCP 产物；真实 EXE `-help` 退出成功 |
| `git diff --check` | PASS；未提交、推送、部署或改动 main |

产物 SHA256：`7c6ba9bebbc2d4ac0c491519893572ab5c5cf20c02f2b33936045757dba95a79`。构建不含正式发布签名；buildvcs=false 不作为可追溯发布证据。使用方法见 [规则管理说明](R11_RULE_ADMIN.zh-CN.md)。

## 无系统策略修改的替代路线复测

本轮用户要求继续修改并测试，原有“不新开账号、仅继续不修改系统策略”的限制没有撤销。本轮只改测试启动器和合成 fixture；没有修改产品执行门、GUI、已安装运行时或已有系统权限。采用 karpathy-guidelines 的小步改动：保留已经证实的默认 DACL 修复，定位剩余错误；不把不完整的隔离证据升级为产品能力。

### 本轮代码改动

- `appcontainer_windows_test.go` 新增明确 opt-in 的 LPAC 候选：在新测试进程的属性列表设置 `PROCESS_CREATION_ALL_APPLICATION_PACKAGES_OPT_OUT`，不是防火墙/WFP 策略。无网络或额外资源 capability；失败不重试更宽权限。普通 AppContainer 分支保持可单独复测。
- 新建测试专用 stdio 管道，使用 `PROC_THREAD_ATTRIBUTE_HANDLE_LIST` 精确限制为 stdin/stdout/stderr 三个子端句柄，stdin 为 EOF。父端不可继承；独立有界采集 stdout/stderr 各最多 16KiB；Job 结束/超时后回收管道。此前实验的“不继承句柄”描述是历史条件；当前只继承上述明确白名单，不继承宿主其它句柄或环境。
- fixture 写结果失败会通过 stderr 报告真实错误、断言值和网络结果。诊断不能作为写结果成功或隔离成功的替代：非零退出、缺少结果文件、严格网络失败均保留 FAIL。
- 合成只读文件只向 `ALL_APPLICATION_PACKAGES` 授予读取权限，新增父/子进程共享应用资源语义检查。普通 AC 两项通过；LPAC 完整检查未通过，不将一段错误日志当作生产 token 准入证明。

### 实际结果

| 检查 | 本轮结果 |
|---|---|
| 普通 AC cmd/Node：普通正对照、受限对照、容器空操作 | PASS；不是用户项目或任意脚本执行 |
| 普通 AC 综合文件/身份/继承实验 | 21 项为 true（原 19 项加两个共享资源读正例）；4 项严格网络为 false，命令退出 1 |
| 普通 AC TCP4/TCP6、UDP4、监听 | 与上轮一致：TCP 超时，UDP 发送 API 和监听成功；300ms 未观察到容器 UDP 包不等于完整网络拒绝 |
| 请求 LPAC 的 cmd 空操作 | PASS；仅这一个固定兼容性案例 |
| 请求 LPAC 的 Node 空操作 | FAIL，stderr `WSAStartup: (10107) 系统调用失败`，退出 `0x80000003`；受限对照仍 PASS |
| 请求 LPAC 的 Go 综合 fixture | FAIL，退出 3；结果写入报 Winsock 未初始化/初始化失败，授权文件读写和子进程亦未通过；不是网络隔离 PASS |
| 请求 LPAC 的 class 46 token 查询 | 本机 `ERROR_INVALID_PARAMETER`；不读取失败 payload、不使用未公开 ABI 伪造 LPAC 证明 |
| 临时应用配置回收 | 所有本轮新建随机配置删除成功，存储路径不存在；没有删除已有配置 |
| 末次代码改动后普通全项目 test/race/vet | PASS；`GOPROXY=off`、`GOSUMDB=off`；显式清空 AppContainer/LPAC/installed-version/live-Tunnel/DPI-WebView 开关 |
| 末次代码改动后真实启动与默认 DACL 竞态测试 | PASS；`LOCAL_PROBE_APPCONTAINER_TEST=1`、LPAC 开关为空，`go test -race -count=1 -v -run '^(TestCurrentAccountAppContainerNativeStartup\|TestCurrentAccountRestrictedDefaultDACL)$' ./internal/probe`；普通 AC 与受限对照真实运行，不是跳过 |

定位依据：本机 Go 1.26.0 的 `src/internal/poll/fd_windows.go` 中，net 包初始化调用 `InitWSA`；Winsock 初始化失败记录到 `initErr`，`FD.Init` 对文件等 FD 也返回该错误。这解释了 Go fixture 的文件 I/O 为什么会同时报 Winsock 错误；不是直接证明工作目录 ACL 错误。没有修改 Go/Node 安装文件，未追踪到 Winsock 内部具体注册表/系统资源的失败调用，因此不能将可能的 LPAC 资源依赖写成已证实根因。微软文档说明 LPAC 对常规 AppContainer 可访问的资源需额外能力，但本轮没有为测试放宽这些能力。[微软 LPAC 说明](https://learn.microsoft.com/en-us/windows/win32/secauthz/implementing-an-appcontainer)、[显式句柄继承文档](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute)。实现未复制这些文档的示例代码。

复测使用两组独立进程环境：

```powershell
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:LOCAL_PROBE_APPCONTAINER_TEST = '1'
$env:LOCAL_PROBE_LPAC_TEST = ''
go test -count=1 -v -run '^TestCurrentAccountAppContainer.*$' ./internal/probe
# 综合实验非零退出：4 项严格网络断言失败。
$env:LOCAL_PROBE_LPAC_TEST = '1'
go test -count=1 -v -run '^TestCurrentAccountAppContainer.*$' ./internal/probe
# LPAC 实验非零退出：运行时兼容性失败，不构成网络准入。
$env:LOCAL_PROBE_LPAC_TEST = ''
$env:LOCAL_PROBE_APPCONTAINER_TEST = ''
```

当前结论：已有启动修复有效；普通 AC 未满足项目的 connect/首包/listen/bind 完整拒绝契约，LPAC 无额外资源能力的候选又出现运行时初始化失败。因此本轮尚未找到满足现有限制的可用严格隔离方案。生产 WFP 后端仍为 `DisabledBackend`，任意命令/脚本/构建不能开放；不以取消断言、超时或运行库崩溃作验收成功。重新请求被拒绝的系统策略授权不是本轮后续步骤。

## 启动故障定位与修复后的实测

当前状态：启动故障已修复；综合隔离实验仍因严格网络门失败，不是后端完整交付。用户选择“仅继续不修改系统策略的测试”，所以没有添加临时 WFP 过滤器、修改防火墙、安装服务或新建登录账号。以下改动仅在 Windows 测试启动器/fixture 内，不接入产品 GUI/MCP，不改变 `execution_available=false`。

### 根因与最小修复

本机调用者令牌的默认 DACL 给 Administrators/SYSTEM 完整权限，只给登录会话 read/execute，没有当前用户的完整访问 ACE。`CreateRestrictedToken(DISABLE_MAX_PRIVILEGE | LUA_TOKEN)` 将管理员 SID 变为 deny-only，原默认 DACL 因而不再给 worker 足够的自身对象权限。保持其它条件不变，cmd/Node 在原 DACL 下退出 `0xc0000142`；仅为新 worker 设置正确的默认 DACL 后，两种启动模式均退出 0。未定位到某个具体 DLL 内部调用，不将退出码解释为缺少 DLL。

修复为获取用于创建受限副本的令牌句柄时包含 `TOKEN_ADJUST_DEFAULT`，并仅对返回的新受限令牌调用 `SetTokenInformation(TokenDefaultDacl)`，默认访问列表限制为 SYSTEM 和该 worker 的当前用户。没有更改父令牌、已有桌面/程序目录权限，仍无继承句柄，仍检查同账号、无提权、AppContainer SID、映像守卫、挂起复核和 Job 归属。默认 ACL 决定新对象访问权限，不能把它与宿主文件授权或网络许可混为一谈。[微软 TOKEN_DEFAULT_DACL 说明](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-token_default_dacl)、[受限令牌说明](https://learn.microsoft.com/en-us/windows/win32/secauthz/restricted-tokens)。

新增 `TestCurrentAccountRestrictedDefaultDACL`：核验 SYSTEM/当前用户精确 DACL、父令牌默认 DACL 与提权状态不变、worker 没有提权、管理员组 deny-only，以及特权仅保留 `DISABLE_MAX_PRIVILEGE` 允许的 `SeChangeNotifyPrivilege`。该回归不启动命令、不创建应用配置。

### 实际复测结果

| 检查 | 修复后结果 |
|---|---|
| cmd/Node 空操作：普通进程、受限对照、AppContainer | PASS；同一复制映像，非任意用户脚本；不是网络安全准入 |
| Go fixture 父进程文件边界 | PASS：授权工作目录读写；越界读写拒绝；只读程序目录写入拒绝；另一个临时 AppContainer 的专属目录读写拒绝 |
| Go fixture 子进程边界 | PASS：真实子进程运行、继承相同容器 SID、没有提权，重复上述文件正/负例均通过 |
| 严格 TCP4/TCP6 拒绝 | FAIL：真实可连接的宿主正对照存在，容器调用超时，没有 `WSAEACCES`；超时不记为明确拒绝 |
| 严格 UDP4 拒绝 | FAIL：连接/写调用成功。新增宿主 UDP 实际送达正对照成功；容器数据包在 300ms 有界接收观察内未到达，但这不等于严格准入通过，也不表示已证实数据泄露 |
| 严格监听拒绝 | FAIL：监听调用成功；没有降格成“未观察到流量就是 PASS” |
| 版本探查 | 本机 Node 24.19.0、Python 3.13.1、PowerShell 7.6.5 实际退出 0；不代表这三类运行时的任意脚本已通过沙箱验证 |
| 临时对象回收 | 两个新建应用配置删除 API 成功并核验存储目录不存在；测试文件由临时目录清理，未删除已有配置 |
| 常规全项目回归 | `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` PASS；opt-in 综合隔离测试单独 FAIL，不能互相抵消 |
| 启动与默认 DACL 的独立竞态复测 | `LOCAL_PROBE_APPCONTAINER_TEST=1`，`go test -race -count=1 -v -run '^(TestCurrentAccountAppContainerNativeStartup\|TestCurrentAccountRestrictedDefaultDACL)$' ./internal/probe` PASS；真实创建临时配置，不是默认跳过 |

综合 fixture 的 19 项身份/文件/继承断言为 true，4 项严格网络断言为 false，测试命令保持非零退出。IPv6 UDP、DNS/代理/非 loopback、注册表、IPC、完整 Job 逃逸/取消回收矩阵、其它电脑及普通非管理员环境仍未验收。没有修改断言来消除失败；目前仍无法铸造生产网络隔离能力。

## 首次当前账号实测（修复前历史）

用户明确要求“不新开账号直接测试”。因此测试使用当前账号，不创建 Windows 登录账号或新的外部账号。临时 AppContainer 是当前用户下的应用配置，不是登录账号；只针对随机新建配置及合成测试目录设置访问权限，结束后删除。未修改现有程序目录权限、读取真实凭据、安装服务或新增 WFP/防火墙规则。

新增 `internal/probe/appcontainer_windows_test.go`、`installed_version_windows_test.go` 与 Windows `isolationhelper`。这些都是测试入口，不是新的产品执行 API；默认回归跳过两个 opt-in 测试开关，不铸造生产 capability，不接入 GUI/MCP。

| 当前账号实测 | 结果与边界 |
|---|---|
| 固定版本探查 | PASS：Node 24.19.0、Python 3.13.1、PowerShell 7.6.5；真实退出码 0，使用现有 PE/映像守卫、固定参数和执行限量。不是项目脚本或网络隔离证明 |
| 复制后的 cmd/Node 固定空操作 | PASS：普通本地进程启动成功；只检查测试映像没有因复制而无法运行，不能作为隔离执行的后备路线 |
| 同账号受限令牌 + 私有非交互桌面对照 | FAIL：cmd/Node 均在初始化时退出 `0xc0000142` |
| 同账号受限令牌 + AppContainer | FAIL：cmd/Node 与 Go helper 在进入正文前退出 `0xc0000142`。已在 Resume 前核验同一用户、非提权、容器 SID（容器模式）、映像身份及 Job 归属，但这不是完整隔离验收 |
| 文件/网络/子进程测试 | 未到达：候选未进入合成文件读写、IPv4/IPv6 TCP、UDP、监听和子进程继承测试，不能记 PASS。注册表、IPC 与完整攻击矩阵也未验收 |
| 临时应用配置清理 | 最新复测删除 API 成功，并核验应用存储目录不存在；没有删除或修改已有应用配置 |
| 常规回归 | 新增实验文件后 `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` PASS；最后增补固定空操作对照后，`go test -race -count=1 ./internal/probe` 和 probe/isolationhelper 的 vet 再次 PASS。opt-in 隔离失败与常规 PASS 分开记录 |

受限令牌对照仍共享私有桌面及启动器，不能由相同退出码判定根因就是 AppContainer、Go、某个 Windows 版本或权限设置。启动兼容性未定位；继续排查当前账号启动器，禁止 unrestricted fallback。实验中也出现过临时 `go-build` 测试 EXE 启动 Access denied；换到新建专用构建临时目录后测试成功进入，但隔离候选仍失败，未禁用安全软件或放宽系统权限。

复测开关（Windows，本机安装路径固定在测试文件，其它电脑须显式核验并调整；不要指向真实凭据或项目脚本）：

```powershell
$env:LOCAL_PROBE_INSTALLED_VERSION_TEST = '1'
go test -count=1 -v -run '^TestInstalledCurrentAccountVersionDiagnostics$' ./internal/probe
$env:LOCAL_PROBE_INSTALLED_VERSION_TEST = ''
$env:LOCAL_PROBE_APPCONTAINER_TEST = '1'
go test -count=1 -v -run '^TestCurrentAccountAppContainer.*$' ./internal/probe
$env:LOCAL_PROBE_APPCONTAINER_TEST = ''
```

## 后续技术工作与验收

原启动故障已修复。当前未闭合的是严格网络门，以及产品化所需的完整隔离/确认/管理接线，不能再将“启动原因未定位”或“等待新建账号授权”作为当前状态。WFP 后端仍为拒绝执行占位；用户本轮不允许修改系统策略，因此不运行 WFP 动态规则测试、不重新请求同一授权。现有策略内可继续做无提权、文件/IPC/子进程兼容性与常规回归，但这些证据不能替代缺失的网络 enforcement，也不能开放任意用户脚本。若未来用户改变这一约束再单独讨论具体系统对象范围。

工作文件写入和真实多连接链路仍需完成各自实现与测试；不能把完成一次诊断或收到测试授权等同于七项交付通过。一小时真实双连接混合负载、外部账号链路和独立审查本轮都未执行。
