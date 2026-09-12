# 实际实施状态与验证记录

日期：2026-09-12。当前等级：**K0 内核原型 + P04 平台有界验证 + P05 真实 ChatGPT/Cloudflare Tunnel 调用通过 + P06 有界文件发现与 literal 搜索已实现 + R1 audit.v1 与 direct-search 第一增量已接入本地 MCP + R2 lines/tail 与 workspace_snapshot 第一增量已接入本地 MCP + R3 无进程环境发现已接入本地 MCP + R4 本地固定探针/typed input/审计与 networkguard 规划边界已实现 + R4 commandpath 本地 identity binding 已实现 + R5 固定 Git plan/parser 已实现但保持不可执行 + R6 transport config、非生产 FileStore、admission gate、fake supervisor automation core、Windows runtimeowner 和 readiness 本地非生产契约已实现。** R4/R5/R6 的新增能力仍未接入远程 MCP；R4 的 OS network enforcement、生产 launcher/broker、R6 的可信 runtime 接线与 Cloudflare health 证据仍未完成。下一阶段执行路线见 [`docs/NEXT_PHASE_PLAN.zh-CN.md`](NEXT_PHASE_PLAN.zh-CN.md)。

## 一、先计划，后实现

1. 读取 `LE-saber/deep-project-orchestrator` 的 README、SKILL、软件项目和验证规则；未修改该仓库。
2. 确认目标仓库为空并可写。
3. 首先完成并提交 `docs/MASTER_PLAN.zh-CN.md`，初始化 main。计划 commit：`1df00950e302cf509d2c521cc8f26548f0f2109d`；计划 blob：`cc1c495f77208ed7dc51bbf94839b62ce421994b`。
4. 从计划提交创建 `feat/readcore-foundation`，其后才编写和测试内核。代码通过 GitHub Connector 写入，未直接覆盖 main 或自动合并。

计划是主交付物，包含 P00–P14 的完整步骤。当前完成状态不应通过代码行数或计划中的未来目录推断。

## 二、本轮实际完成

| 计划项 | 状态 | 已有成果 |
|---|---|---|
| P00 | 主要完成 | 计划先行、来源 SHA、许可证/政策纠偏、分支；指定上游选择性检出已于 2026-09-08 完成 |
| P01 | 真实链路已验证 | Cloudflare Named Tunnel、Access Managed OAuth 与 ChatGPT Business 插件已完成真实登录、同步和调用；故障恢复仍需继续扩展 |
| P02 | 完成内核部分 | Scope/Source/Handle/Request/Result/Limits、接口边界、威胁模型 |
| P03 | 完成当前 byte-range 内核 | 有界 ReaderAt、确定性公平批量、部分失败、版本/UTF-8/取消、demo、测试 |
| P04 | 部分实现；平台有界验证完成 | `config`/`policy.BoundScope`、Go 1.25、基于 `os.Root` 的只读 rootfs、`readcore` bound adapter；Windows 本轮、WSL2 历史轮次已覆盖特殊文件、路径、symlink/junction swap 和临时 loopback SMB remote-root 边界；仍不是独立安全审查或发布结论 |
| P05 | 最小 MCP 与真实 Cloudflare ingress 已验证 | 官方 MCP Go SDK v1.7.0、现代无状态/旧版有状态 Streamable HTTP 协商、本地 bearer 与 Cloudflare Access JWT/JWKS、Host 校验；ChatGPT“极高”实际调用 server_info/ping/read_file/batch_read 通过，Tunnel 重连后无需重新登录 |
| P06 | 第一增量已实现并通过本机与真实链路验证；手工靶场已加入 | list_directory、find_files、search_text、tree_directory；有界迭代、扁平深度优先树、literal UTF-8 搜索、deny/ignore、签名短期 cursor、revision/generation 失效和覆盖率说明；四个发现工具已由 ChatGPT Business“极高”验证，`manual-test-targets/` 提供分页、嵌套、Unicode、空文件和拒绝负例；R1 已补充单次调用打开文件/目录预算与不可推进游标防护 |
| P07 | 第一增量完成 | MCP `bytes`/`lines`/`tail` 范围读取、受策略绑定的 `workspace_snapshot`、Windows 手工靶场；模型任务效果评测和强一致快照仍未完成 |
| P08 | Windows 本地核心第二增量与 commandpath identity binding 已实现，OS enforcement、生产接线与 MCP 未完成 | 固定 tool_exists/tool_version、PE/固定路径/句柄 identity 守卫、挂起进程复核、单进程 Job、私有环境/cwd、输出/超时限制与进程树终止；`version_probe` 仍兼容 `slots:[]`，`fixed_command` 的 exact argv、enum/bounded_integer/root_relative_path typed slots、配置解析、root ID 交叉校验与纯 `ResolveVariant` 已实现；`ResolvedInput` 为最终 exact argv 提供 canonical、长度前缀的 SHA256 digest，version-probe 的一次性 confirmation v2 绑定该 digest 与 config revision，旧 v1 token fail closed；执行期从同一最终映像 guard handle 计算小写 SHA256；`ProcessOutcome` 记录真实自然退出码或明确的 timeout/cancel/output-limit 状态及有界时长/字节计数；local commandexec 已将该 outcome 映射为 `command.result`；config revision lease、local commandexec fail-closed bridge、developer mode 配置和一次性确认核心、R4-AUDIT-01（AuditRecorder、同步 `command.reject`、`command.admission`/`command.start` 有界异步入队、即时校验/入队错误 fail closed）、audit.v2 command producers、NET-01 networkguard contract/fake、R4-NET-02 固定 8 个 ALE family 的 opaque plan 与跨平台 DisabledBackend 已实现；`internal/commandpath` 与 rootfs adapter 已实现本地 final handle/identity commitment、revalidate 和不可序列化 binding，但 `Source.New` 的根目录 Lstat→OpenRoot 竞态、长路径与最终 launcher 原子硬门仍未闭合；typed runtime values 已进入 commandexec 的本地 `Prepare`/`Confirm`/`BuildRequest` 边界，fixed execution 仍在 confirmation/admission/start/runner 前由 commandexec 以 `unsupported_profile` fail closed，因而没有新增 fixed 执行能力；真实 WFP/broker/service、EnforcementCapability 铸造、CLI/supervisor 生产接线、MCP `run_probe` 与任意命令仍关闭 |
| R5 | 本地固定 Git plan/parser 已实现，保持不可执行且未接入 MCP | `git_status`/`git_diff` 的固定参数、受限 root-relative path、bounded output/entries/hunks/lines、porcelain-v1/统一 diff 结构化解析和 capture completeness 已实现；`Plan.PreviewExecutable()` 固定为 `false`，原因是 repo-local clean/smudge/process filters 无法由当前固定 flags 完整关闭，且 `RootID` 仍不是 rootfs binding/cwd。Git 不启动、不解析 raw command、不接入 launcher 或 MCP |
| R1/R2/R3 | 第一增量已实现，服务级边界仍未完成 | direct search 的 cursor replay/coverage/open budgets、typed audit.v1、`get_environment`/`discover_tools`、R2 范围读取和 `workspace_snapshot` 已接入本地 MCP；全局 admission/wire 配额、运行时 audit 故障 fail-closed、强快照和真实 Business 复测仍未完成 |
| P09/P10 | 第一增量（本地核心）已实现，生产 runtime/真实验证未完成 | R6 transport-aware connection config、非生产 FileStore、全局/每 connection admission gate、disable/revoke/revision binding、健康状态机、backoff/circuit breaker、sleep/wake/reconnect、cleanup failure 状态和 connection isolation 的 fake automation core；另有 `runtimeowner` Windows DuplicateHandle→Job、PID+creation identity、ancestor/tree membership、Terminate dispatch/WaitExited 与可重试失败清理契约，以及 `readiness` Issuer/Session/revoke、fresh attestations、one-use nonce、per-scope/总 replay budget、长期 evaluator 契约；两者均 `ProductionReady=false`，不创建真实 child/tunnel，不等于 trusted launcher/broker、direct-leaf membership、MCP ping/Cloudflare health 或双连接 soak 证据 |
| P11–P14 | 未实现 | 索引、产品化、独立审查、可选写入 |

代码位置：`internal/config/`、`internal/environment/`、`internal/policy/`、`internal/readcore/`、`internal/rootfs/`、`internal/mcpserver/`、`internal/cfaccess/`、`internal/runtimeowner/`、`internal/readiness/`、`cmd/readcore-demo/` 和 `cmd/local-probe-mcp/`。Cloudflare 本地增量由 Luna 5.6 Max 子代理起草，主代理在其两次未能按时收尾后接管审查、修正与验证。
### P04 增量事实（2026-09-09）

- `config`/`policy` 已修复配置上限、重复 key、显式 `enabled` 语义，并由 `policy.BoundScope` 绑定 connection/profile revision 和 root/path deny。
- `rootfs.Source` 从配置快照持有 Go 1.25+ `os.Root`，只读打开普通文件；`BoundScope` adapter 接入 `readcore.Engine`，Open、Metadata、ReadAt 均重新验证授权和撤权。
- deny 匹配已覆盖 Windows 大小写不敏感语义与 `**`；Source revision 校验阻止“旧 Source + 新 scope”在 root ID 复用后访问旧 root。
- 主代理补上 `policy.BoundScope.AllowsPath` 的撤权前置校验：scope 失效时路径授权与 root/tool 授权一样立即返回 false；测试覆盖配置替换/禁用后的旧 scope 不再通过路径检查。
- Windows handle metadata 已纳入 volume/file identity、link count 和 reparse attributes；hardlink、junction/reparse、Unicode/组合字符、长路径、ADS/保留名/非法路径和 symlink+junction swap 测试通过。临时通过 `New-PSDrive -Name Z -PSProvider FileSystem -Root '\\localhost\C$' -Persist` 将 `Z:` 映射到 loopback SMB 共享时，`GetDriveTypeW` 返回 4，remote-root 测试通过；finally 执行 `Remove-PSDrive -Name Z -Force`，并复核 PowerShell PSDrive、LogicalDisk 和 `net use` 均无 `Z:`。
- 历史 WSL2 Ubuntu-22.04（Linux 6.6.87.2-microsoft-standard-WSL2、Go 1.26.2）轮次曾临时复制当前工作树并通过 FIFO、Unix socket、hardlink、symlink swap 的目标测试及 race/vet；这不是裸机 Linux 证据。本轮 WSL Ubuntu-22.04 未安装 Go，未运行 Linux runtime 测试，不能把本轮记为 WSL 通过。机器可读记录见 `docs/evidence/P04-platform-verification.json`。
- 上述是实现者在明确环境和次数下的有界验证，不是数学证明，也不代表 P04 全部放行；本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台、独立安全审查仍是残余风险。P05 的本地 MCP 生命周期和 P01 的一次真实账号/Tunnel/App 调用已有记录，但不代表多账号、长期运行或后端兼容性保证。

## 三、真正运行过的验证

早期 Linux 内核验证环境为 **Go 1.23.2 / Linux amd64**，无容器外网 DNS；GitHub 操作通过连接器完成。当前 `go.mod` 已提升到 Go 1.25.0，Windows 本轮验证使用 Go 1.26.0；WSL2 的 Go 1.26.2 仅属于历史验证记录，本轮 Ubuntu 环境因未安装 Go 未运行 Linux 测试。Cloudflare Access 验证使用 `coreos/go-oidc` 及其间接依赖，因此首次构建需要模块缓存或网络。工具链通过不等于产品发布版本。

| 检查 | 实际结果 | 限制 |
|---|---|---|
| Windows `go test ./...` | PASS（Go 1.26.0） | 本机运行证据；不等于 Linux/其他平台运行验证 |
| Windows `go test -race ./...` | PASS（Go 1.26.0） | 不等于真实多账号/全局调度验证 |
| Windows `go vet ./...` | PASS，退出码 0 | 静态检查不是安全审计 |
| Windows `git diff --check` | PASS | 只检查工作树 diff 格式 |
| `GOOS=linux GOARCH=amd64 go build ./...` | PASS | Linux 交叉构建；未在 Linux runtime 执行，不能替代 WSL/裸机 Linux 测试 |
| `go test -coverprofile=... ./...` | readcore 94.1%；demo 75.9% statement coverage | 覆盖率不代表所有威胁已处理 |
| `FuzzValidPath`，3 秒、2 workers | PASS；39,940 次 fuzz 执行 | 短时探测，不是穷尽证明 |
| `FuzzUTF8Pagination`，3 秒、2 workers | PASS；28,747 次 fuzz 执行 | 同上 |
| Linux amd64 build | PASS | 本机运行 demo 已测 |
| `GOOS=linux GOARCH=amd64 go test -c ./internal/rootfs` | PASS | 仅 rootfs 交叉编译检查；未在 Windows 上执行 Linux 二进制，不能替代 Linux 攻击测试 |
| Windows amd64 cross-build | PASS（历史记录） | 该记录本身不等于 Windows 运行或安全路径测试；本机运行证据见下方增量验证 |
| macOS arm64 cross-build | PASS | 没有 macOS 运行测试 |

最初将多项检查合并到一次命令时，外层命令在冷启动 fuzz 编译阶段达到执行工具时间上限；此前单元/竞争/vet/coverage 已结束。随后分别运行两项 fuzz，均实际通过。没有把未结束的一次命令记为通过。

### 大文件行为证据

1. 单元测试使用逻辑大小 10 GiB 的合成 ReaderAt，在 9 GiB 偏移读取 4,096 字节；确认仅请求/返回这一页，未分配或读取整文件。
2. 另外在 Linux 创建一个真实的 **10 GiB 稀疏文件**，只在 9 GiB 位置写入 4,096 字节，再用编译后的 demo 读取。返回正文 4,096 字节、逻辑 ReaderAt 字节 4,096、version strength=metadata；当时文件实际分配磁盘约 4,096 字节。临时文件随后删除。
3. 这不是实体 10 GiB 日志扫描测试，不是百万文件仓库测试，也不是 Pro 模型的调用效率测试。不能据此承诺这些场景的延迟。
4. 合成 ReaderAt 微基准三轮为约 33.3/36.2/33.5 微秒/页，9,088 B/op、21 allocs/op；结果只用于内核局部参考，不代表真实磁盘或 Web 端性能。

### ChatGPT Business `tree_directory` 真实验证（2026-09-10 20:53，模型档位“极高”）

在已登录的 GPT-workspace Business 会话中，真实调用 Local-Probe 完成以下链路；只记录
返回的非敏感字段，不记录任何 token、JWT 或密钥：

- `tree_directory(root_id=project, path=internal, max_depth=2, page_size=8, max_entries=30)`
  第一页返回 `internal`（depth 0）、`internal/cfaccess`（depth 1）及其两个 `.go`
  文件、`internal/config` 及其两个 `.go` 文件和 `internal/mcpserver`（共 8 项），
  `coverage.complete=false`、`warnings=["page_limit"]`，存在 continuation。
- 使用完全相同参数和第一页 cursor 调用第二页，返回从
  `internal/mcpserver/cloudflare_test.go` 继续的 8 项，仍为
  `complete=false`、`warnings=["page_limit"]`；`internal` 的 depth 0 根条目没有重复。
- 从前两页选择 `internal/cfaccess/verifier.go` 调用 `read_file(offset=0,max_bytes=128)`，
  返回 `bytes_read=128`、`eof=false`。
- `tree_directory(path=.runtime,max_depth=1,page_size=8,max_entries=30)` 被拒绝，错误
  关键字段为 `code=invalid_request`；未返回 `.runtime` 名称下的文件内容或绝对路径。

上述验证证明远程 Business→Cloudflare→MCP→rootfs 的 tree 分页和拒绝路径已跑通；
`manual-test-targets/README.md` 中新增的中文/Unicode、空文件、组合读取和专用拒绝目录
用例仍需按夹具说明再做一轮用户侧手工复测。

精简机器可读证据见 `docs/evidence/K0-local-verification.json`。已测试的七个源码文件逐一按 Git blob SHA 与上传 tree 核对一致，避免测试本地一份、提交另一份。

### 复现命令

```sh
go test -count=1 -v ./...
go test -race -count=1 ./...
go vet ./...
go test -coverprofile=coverage.out ./...
go test ./internal/readcore -run='^$' -fuzz=FuzzValidPath -fuzztime=3s -parallel=2
go test ./internal/readcore -run='^$' -fuzz=FuzzUTF8Pagination -fuzztime=3s -parallel=2
go test ./internal/readcore -run='^$' -bench=BenchmarkLargeRange -benchmem -count=3
go run ./cmd/readcore-demo -file ./README.md -offset 0 -max-bytes 4096
```

GitHub Actions 配置另固定 Go 1.26.5 和 action commit，对 Linux/Windows/macOS 跑 core/demo 测试，并在 Linux 跑 race/fuzz。**写入 workflow 不等于 CI 成功；真实 run 状态以 PR/Actions 为准。** CI 的普通文件 demo 通过也不能替代未来 rootfs 安全测试。

### Windows 增量验证（2026-09-09）

在 **Windows amd64 / Go 1.26.0** 上补充运行了以下检查。普通 rootfs 测试覆盖 Unicode/组合字符、长路径、ADS/保留名/非法路径、hardlink、junction/reparse；symlink+junction swap 普通测试重复 5 次、race 测试运行 1 次，测试会回读 outside marker 并要求读写重叠计数大于零。

| 检查 | 实际结果 | 限制 |
|---|---|---|
| `go test ./internal/rootfs -count=1 -v` | PASS | 包含 Unicode/组合字符、>260 长路径、ADS/保留名/非法路径、hardlink、junction/reparse 及普通 symlink+junction swap；实现者自测 |
| `go test ./internal/rootfs -run '^TestWindowsRejectsSymlinkAndJunctionSwapRace$' -count=5 -v` | PASS | 每次校验 outside file/directory marker 未变且 overlap counter > 0；测试中的允许共享/锁失败按策略计数 |
| `go test -race ./internal/rootfs -run '^TestWindowsRejectsSymlinkAndJunctionSwapRace$' -count=1 -v` | PASS | 同上；实现者自测，不等于真实多账号/全局调度验证 |
| `go vet ./...` | PASS | 静态检查不是安全审计 |
| `go test -c ./internal/rootfs`（Windows amd64） | PASS | 仅编译检查，不替代运行时攻击测试 |
| `GOOS=linux GOARCH=amd64 go test -c ./internal/rootfs` | PASS | Windows 主机上的 Linux 交叉编译；未在 Windows 执行 Linux 二进制 |

临时通过 `New-PSDrive -Name Z -PSProvider FileSystem -Root '\\localhost\C$' -Persist` 将 `Z:` 映射到 loopback SMB 共享时，PowerShell 对 `Z:\` 调用 `GetDriveTypeW` 返回 **4**，`TestWindowsRejectsRemoteMappedRoot` PASS；finally 执行 `Remove-PSDrive -Name Z -Force`，并复核 PowerShell PSDrive、LogicalDisk 和 `net use` 均无 `Z:`。`nativeMetadata` 故障注入测试确认查询错误返回 `ErrUnavailable` 且 Engine 不返回内容。

本节结果是当前实现者在 Windows 上对 P04 部分实现的有界验证，不构成独立安全审查，也不证明 P04 全部放行、生产 rootfs、MCP、Tunnel、真实账号或多连接隔离已完成。既有 Linux 验证证据保持不变。

### R1 搜索有界性与续页修正（2026-09-11）

P06 的四个搜索/发现工具现在将 `MaxOpenFiles` 与 `MaxOpenDirectories` 作为**每次调用**
的成功打开句柄预算；它们不是账号、连接、进程或机器级全局配额。结果中的
`coverage.opened_files`、`coverage.opened_directories` 记录本次成功打开数，
`coverage.replayed_entries` 记录为恢复签名游标而消耗的目录记录数。预算耗尽时会返回
`open_file_limit` 或 `open_directory_limit`，并保持 `complete:false`。

rootfs 的目录适配器按有界批次保留原生 `ReadDir(n)` 目录流顺序；同一 directory generation
内分页顺序稳定，但不承诺字典序/字母序，也不为排序把大型目录整体物化。审查还修复了
目录栈已占满打开预算时续页反复重放同一游标的问题：这种不可推进情况返回
`open_directory_limit_no_continuation`，不再生成循环 continuation，调用方应把结果视为
有界的部分结果。

### R2 范围读取与工作区轮廓第一增量（2026-09-11）

`read_file`/`batch_read` 保留原有 `offset`/`max_bytes` 输入，并可选接受严格的
`range.kind`：`bytes`、`lines` 或 `tail`。行号从 1 开始；有换行符的记录保留原始换行
字节；无尾部换行的最后一行仍是有效记录。`max_scan_bytes` 是 lines/tail 的硬扫描预算，
结果同时给出 `range_kind`、行号、`complete` 和 `scanned_bytes`。MCP 层的输入 schema
拒绝额外字段，旧版 byte-range 请求不需要改写。

`workspace_snapshot` 使用与搜索服务相同的 `search.Binder` 和 `policy.BoundScope`，只做
有界目录树、manifest 路径证据和语言统计，不执行源码或 manifest，也不接受模型传来的
绝对根路径。profile allowlist、deny/ignore、cursor 和 wire response 上限仍由既有 MCP
边界负责；审计仅记录固定 action 和预算计数，不记录 path、content、cursor 或请求参数。
服务通过 CLI 以固定 `workspacesnapshot.DefaultLimits()` 启动，profile 仍决定是否暴露工具。

本地 Windows MCP 集成测试覆盖旧 byte 输入、lines/tail、batch 混用、workspace snapshot
相对路径脱敏、额外字段拒绝、第二 connection 的 allowlist 拒绝和 audit action 映射。
`manual-test-targets/r2/` 提供 CRLF/UTF-8/无尾换行、4 KiB 小型长行、manifest 和 deny
负例。当前仍需在 Windows 手工调用并在真实 Business“极高”会话中复测；本轮不做 Linux
runtime 测试，也不把 workspace snapshot 宣称为强一致快照。

### R4/P08 Windows 固定探针与 developer-mode 本地核心（2026-09-12）

当前已经实现、但仅限可信本地调用方的部分包括：

- `internal/probe` 只接受固定 `git`/`python`/`node` tool ID，或由受信任 profile 驱动的
  `version` tool ID，以及本地绝对 `.exe`；Windows 会拒绝 UNC/device/ADS/保留名/路径别名、
  reparse/symlink、非普通文件和非 PE 映像。
- 执行前持有最终映像与父目录的句柄守卫，使用 `CREATE_SUSPENDED` 创建进程并立即加入
  Job Object；恢复主线程前查询实际映像路径并核对句柄 identity。Job 限制为单进程、
  kill-on-close，并保留无窗口、闭 stdin、私有 cwd、精简环境、输出/超时和回收边界。
- `internal/commandprofile` 已实现本地不可变的 Windows profile 校验：`version_probe` 继续
  兼容 `argv.slots:[]`，并保留固定小写 64 字符 SHA256、exact argv variant（只允许 `-v`、
  `--version` 或 `version`）；`fixed_command` 新增 exact argv 与逐项模板两种本地配置形式，
  模板只允许 `enum`、`bounded_integer`、`root_relative_path` typed slots。配置解析会校验
  variant/slot 引用、root ID 和约束；纯 `ResolveVariant` 只构造新的 argv，不执行命令。root-relative
  path 采用 `/` 规范、拒绝超过 4096 字节以及 Windows 保留名/非法字符，并要求受信任 resolver；
  同一 slot 在单次调用中只解析一次并复用，最终 resolved argv 受 32767 字节保守预算约束。固定 `.exe`、
  私有空 cwd、空环境、单进程、`network=deny` 声明、`per_call` 本地确认和结构化结果约束仍在；
  不接受 raw command、任意后缀、任意参数、任意 env/cwd/timeout。`ResolveInput` 在可信本地
  调用方完成 variant 解析后返回不可变 argv 和 canonical resolved-input digest；digest 对 profile
  标识/种类、variant、固定 identity 以及带边界的完整 argv 做域分离和长度前缀编码。它不是
  path/argv 的审计输出，也不替代最终 resolver 的 root 授权与 Windows launcher 检查。
- `internal/confirmation` 已实现短期、一次性、绑定 connection/profile/revision/command/
  variant/request nonce 以及 resolved-input digest 的 confirmation v2；旧 v1 token 会被
  fail closed 拒绝。模型在 MCP JSON 中提交布尔值或文本“确认”不能伪造它。
- `config.Store.AcquireRevisionLease` 和 `internal/commandexec` 已形成一个只接受本地
  selector、持有 revision lease、先做 profile/network admission 的 fail-closed bridge。
  `Executor` 要求调用方提供 `AuditRecorder`：拒绝路径同步记录 `command.reject`；成功准入后
  将 `command.admission`、消费一次性确认后的 `command.start` 分别送入有界异步队列，随后才调用
  `probe.AuditExecutable`/`ToolVersionWithPolicyOutcome`。probe 返回 path-free 的结构化
  `ProcessOutcome`：自然结束时保留真实 exit code，timeout/cancel/output-limit 不合成 exit code，
  并只报告有界 duration 与 captured-byte 计数；原始输出、path、argv、env 不进入 outcome。
  local commandexec 在每次执行尝试后把 outcome 映射为 `command.result`；即使 probe 返回受限
  失败 outcome，也会先尝试记录结果。`command.start` 表示确认后、调用 runner 前的 launch-dispatch
  intent，不是操作系统已创建/恢复进程的证明。只有即时校验或入队错误会 fail closed、不启动
  probe；结果审计失败发生在进程尝试之后，会返回稳定的 audit failure，且不会伪造新的 reject。
  后续磁盘写失败只将 sink 标记为 `degraded`，尚待 supervisor 阻断新的执行。这不是“每条事件
  持久化后才启动”的保证。审计上下文不包含 path、argv、env、output 或 token。
  该 bridge 也尚未接入 CLI/supervisor 或 MCP。对 `fixed_command`，typed runtime values 已进入
  commandexec 的本地 Prepare/Confirm/BuildRequest 链：prepared input 绑定 revision、variant
  和 resolved-input digest，Request 不承载可变 argv；但字符串 PathResolver 不是可信 final
  path binding。commandexec 仍在 confirmation、admission、start 和 runner 之前以
  `unsupported_profile` fail closed，因此本增量没有新增 fixed 执行能力。
  `commandprofile.EnforcementCapability` 没有可信铸造器时，执行会在消费确认前拒绝。
- `internal/networkguard` 的 NET-01 platform-independent contract/fake 已实现：每次操作使用
  不可序列化的独立 lease/capability/run handle，要求完整 IPv4/IPv6 outbound/inbound、bind/listen、
  loopback、children、inherited handles、existing flows、DNS/proxy 和 cleanup coverage；admission
  窗口最多 30 秒，cleanup 默认 5 秒，cleanup 失败会阻塞后续 admission，fake 不触碰任何系统状态。
  它不铸造 `commandprofile.EnforcementCapability`，不触碰 WFP/Windows Firewall，也不等于生产
  network deny。
- Windows 执行期 SHA256 从已经持有的最终映像 guard handle 读取，并在创建/恢复进程前复核
  deadline；不会对一个路径单独 hash 后再按路径启动。该摘要计算有界，且使用常量时间比较。

### R4/P08 fixed typed input 本地 prepare/confirm 增量（2026-09-12）

本增量已把 `fixed_command` 的 typed runtime values 接入**仅供本地调用方**使用的
`internal/commandexec` 边界，但没有开放执行。`Prepare` 只接收本地构造的
`commandprofile.SlotValue` 映射和 `commandprofile.PathResolver`，由 profile 逐项解析
`enum`、`bounded_integer`、`root_relative_path`，生成带 profile/revision/variant 绑定的
不可变 `PreparedInput`。`Request` 只能携带该 prepared value，不能携带可变 argv；其 argv
只可通过防御性副本预览，prepared value 不能经 JSON 伪造或跨 Executor 重放。

`Prepare` 在解析期间持有 profile revision lease；`Confirm` 重新取得同一 revision lease，
并由本地 confirmation manager 生成绑定 connection/profile/revision/command/variant/nonce/
resolved-input digest 的一次性 v2 capability。Prepare、Confirm、BuildRequest 不启动进程、
不调用 shell、不触碰网络策略，也不把 argv/path/token 写入审计。Execute 在 fixed profile
仍于 confirmation 消费、admission、start 和 runner 之前返回 `unsupported_profile`；因此
该增量只证明 typed input 的本地数据流和 fail-closed 顺序，不证明 fixed command 可执行。

当前 `PathResolver` 是一个字符串返回回调，只是可信调用方与 profile 之间的临时接口。它
没有提供 OS handle、root deny/ignore、reparse/symlink、final identity 或 Windows
TOCTOU 证明，不能称为可信 final path binding；后续必须替换/封装为真正的 rootfs-aware
resolver，并在 launcher 端按 Windows UTF-16 长度和 escaping 规则再次校验。真实 WFP、低权
限 broker/service、capability 铸造、CLI/supervisor 接线和 MCP `run_probe` 仍未完成。

同一轮主代理修复了 `policy.BoundScope.AllowsPath` 的撤权防御：路径授权现在先验证
BoundScope 当前有效，再应用路径和 deny 规则，旧 scope 在配置替换或连接禁用后不能继续
通过 `AllowsPath`。这属于文件访问策略修复，与 typed command prepare/confirm 边界分开。

### R4-NET-02 固定 WFP deny plan 与 disabled backend（2026-09-11）

本阶段只完成了网络拒绝的**平台无关规划边界**，没有执行任何 Windows WFP/防火墙操作。
`internal/networkguard/wfp` 现在提供不可由调用方注入的 opaque `Plan`，固定包含以下八个
ALE family，并且每个 family 都固定为 `block`、`dynamic_only`、`target_app` 与
`target_user` identity slots：

- `AUTH_CONNECT_V4` / `AUTH_CONNECT_V6`；
- `AUTH_RECV_ACCEPT_V4` / `AUTH_RECV_ACCEPT_V6`；
- `AUTH_LISTEN_V4` / `AUTH_LISTEN_V6`；
- `RESOURCE_ASSIGNMENT_V4` / `RESOURCE_ASSIGNMENT_V6`。

这里把 `AUTH_LISTEN` 单独纳入固定计划，是因为仅覆盖主动 connect 和入站
recv/accept 不能表达被动监听授权；`RESOURCE_ASSIGNMENT` 负责 bind 等资源申请，不能把
listen 语义省略后再声称覆盖完整入站生命周期。计划不暴露 provider、GUID、地址、端口、
权重、持久化/启动标志或 coverage 字段；JSON marshal/unmarshal 也不能构造或重放它。

跨平台 `DisabledBackend` 只按 opaque lease 在内存中保存固定计划；`LaunchSuspended` 和
`Activate` 永远 fail closed，`Activate` 永远返回零 coverage，`Revoke` 只做本地幂等清理。
它不调用 `fwpuclnt.dll`，不打开 dynamic session，不安装过滤器，不启动进程或 service，
不修改 Windows Firewall/WFP，也不铸造 `EnforcementCapability`。本阶段没有运行真实/手动
网络测试，不能把该 plan 或 disabled backend 写成已断网证据。

以下硬门仍未完成，因而不能称为 R4 完成或接入远程工具：

- 没有已接入操作系统的 network deny 执行器；`network=deny` 仍只是 profile 的硬约束。
  NET-01 的 `internal/networkguard` contract/fake 与 R4-NET-02 的固定 plan/DisabledBackend
  只提供平台无关的生命周期、固定规则形状和 fail-closed 边界，不铸造
  `commandprofile.EnforcementCapability`，不触碰 WFP/Windows Firewall，也不等于生产
  network deny。仍没有 `fwpuclnt.dll`/WFP ABI 适配器、dynamic session、过滤器安装、低权限
  broker/service、管理员安装/ACL 流程或可信 capability 铸造器；无法证明时必须拒绝执行。
  网络设计和停止条件见 [`docs/R4_WINDOWS_NETWORK_DENY.md`](R4_WINDOWS_NETWORK_DENY.md)。
- `internal/commandexec` 是本地核心 bridge，不等于生产 runtime wiring：CLI/supervisor 尚未
  构造它、管理 capability 生命周期或把结果接入正式执行/恢复路径；MCP 仍不能调用它。当前
  `internal/probe` 自己创建并恢复进程，尚未把“由 broker 创建、持有并在网络策略生效后恢复”的
  挂起进程所有权交给 network backend，因此 R4-NET-02 不能提前接入现有 probe。
- `fixed_command` 的 `ResolveVariant`/`ResolveInput` 仍只是本地配置解析与 argv/digest 构造器，
  不是授权或 launcher。新的 commandexec `Prepare`/`Confirm`/`BuildRequest` 已把 typed runtime
  values 接入本地边界，并让 Request 只携带不可变 prepared input；但其 `PathResolver` 仍是
  字符串回调，不能证明 root 授权、deny/ignore、reparse/symlink 或 final identity，不能称为
  trusted final path binding。虽然 canonical resolved-input digest 已绑定 fixed 的本地
  confirmation v2，fixed 执行仍必须在 confirmation、admission、start 和 runner 前阻断。后续
  最终 resolver 仍须重新完成 root 授权、deny/ignore、reparse 和 final identity 检查；launcher
  还须按 Windows UTF-16 长度与 escaping 规则重新检查。`cmd`、PowerShell 及其它 shell/interpreter
  方案也必须在该边界前明确闭合，不能由模板间接引入。
- SHA256 已在 Windows 执行期按同一 guard handle 计算并比较，但尚无签名/Authenticode 校验，
  也没有对抗性 VM/网络证据。
- `internal/audit` 已提供固定 schema 的 `command.admission`、`command.start`、
  `command.result`、`command.reject` producers（audit.v2 字段只记录 rule/identity digest、
  exit/timeout、输出字节计数和 network enforcement 状态）。R4-AUDIT-01 已把
  `AuditRecorder` 接入 local commandexec：拒绝路径同步发出 `command.reject`，成功路径分别将
  `command.admission`/`command.start` 有界异步入队；只有即时校验/入队错误 fail closed，后续磁盘
  写失败只标记 `degraded`，尚待 supervisor 阻断新执行，不能据此声称每条记录都在启动前持久化；
  不写入 path、argv、env、output 或 token。local commandexec 已把 probe 的 `ProcessOutcome`
  映射并接入 `command.result`；`command.start` 仍只是 launch-dispatch intent，不是 OS started
  proof。CLI/supervisor 生产接线及 MCP 仍未完成。另没有运行时 sink 故障后的 fail-closed ingress。
- `internal/mcpserver` 没有注册 `run_probe`，也没有远程 command/profile/confirmation schema。
  `fixed_command` 的本地解析增量不改变这一点；ChatGPT、Cloudflare Tunnel 和其它远程入口继续
  只能使用已注册的只读文件/环境工具。

已有 Windows 文件守卫不能被解释为完整的共享写入隔离：调用方在进入守卫前已经持有的可写
句柄或 mapped view 仍是残余风险；`LockFileEx` 的字节范围锁不约束内存映射视图，因此不能
作为该风险的完整修复。该问题必须在后续 broker/OS enforcement 和对抗测试中单独验证。

本轮未运行 Windows 手工、网页或网络测试；步骤见 [`manual-test-targets/r4/README.md`](../manual-test-targets/r4/README.md)。
不据此宣称 Linux 或其它 Unix 运行时通过；Unix fd-based launcher 仍是后续硬门。

### R3 无进程环境发现 MCP 增量（2026-09-11）

`internal/environment` 的无进程 `GetEnvironment`/`DiscoverTools` 已由
`internal/mcpserver` 注册为 `get_environment`/`discover_tools`，并由 CLI 将
`config.Config.EnvironmentTools()` 转换为本地 `environment.ToolSpec`。MCP 请求只能
选择逻辑 ID；候选路径和 `LocalDiagnostics` 不进入远端输入，响应边界再次清除候选路径。
profile allowlist、未知 ID、无配置空结果、错误脱敏和两条连接的工具过滤均有本地 MCP
测试覆盖。`discover_tools` 的候选检查使用精确配置路径，不执行 `where.exe` 或 PATH
搜索；该性质由核心实现和注入 discovery 测试覆盖，不等于真实 Business 复测。

本轮 Windows amd64 已实际通过 `go test ./...`、`go test -race ./...`、`go vet ./...` 和
`git diff --check`；`GOOS=linux GOARCH=amd64 go build ./...` 也通过，但仅证明交叉构建。
WSL Ubuntu-22.04 因未安装 Go 未运行本轮 Linux runtime 测试，不能将其记为本轮通过。

### R1 audit 基础设施与 R4 command producers（2026-09-12）

`internal/audit` 已实现固定 schema 的 typed JSONL sink：当前 sink 统一发出
`local-probe.audit.v2`；audit.v1 标识和旧 `command.exit` wire value 仅保留历史兼容用途。事件
字段包括时间、实例、关联、组件、类型、action、
severity、outcome、error_code、连接/profile/revision 和预算计数；command producer 只增加
受限 command/variant ID、lowercase identity digest、exit/timeout、stdout/stderr 字节计数和
network enforcement 状态。producer 侧拒绝敏感字段；参数、请求/响应正文、路径、token、JWT、
key、完整 argv/env/stdout/stderr 不会写入。普通事件进入有界异步队列，队列满会丢弃并标记
`degraded`；安全事件同步落盘。
文件权限、大小轮转和保留数有界，CLI 在 sink 初始化失败时不会开始监听。

当前 MCP 只接入 `auth.accept`、`mcp.list`、`mcp.call`、`mcp.result`，最终 HTTP 401/403 由
外层包装统一写一条同步 `auth.reject`；typed `CallToolResult` 只提取有限的稳定错误码，未知
格式降级为 `unavailable`，不记录错误 message。运行时 sink 故障不会自动使已启动 listener
fail-closed；R4-AUDIT-01 仅保证 commandexec 对 `AuditRecorder` 的即时校验/入队错误 fail closed；
`command.admission`/`command.start` 通过有界异步队列，后续磁盘写失败只标记 `degraded`，由 R6
admission gate 提供独立的健康输入，但尚未完成把整个已启动 listener 与该状态的生产接线。
command producers 不记录 path、argv、env、output 或 token。local commandexec 现在在 probe 执行
尝试结束后，以 path-free 的 `ProcessOutcome` 产生 `command.result`；结果审计失败不会被伪装成新的
`command.reject`。`command.start` 的语义是确认后的 launch-dispatch intent，不是 OS 已启动证明。
R6 的 admission gate 与 fake supervisor automation core 已实现，但 CLI/supervisor 生产接线、
network-tunnel、policy、fs-search 事件生产者和管理变更审计仍未实现。

## 四、R4–R6 本轮新增本地核心（2026-09-12）

本节只记录已经落在代码中的**本地、可测试核心**。它们不是生产 runtime，也没有因此扩大
当前 MCP 工具面。

### R4 commandpath：本地 identity binding

`internal/commandpath` 与 `internal/rootfs` 已提供不可 JSON 序列化的本地
`PathBinding`/`TrustedResolver` 边界：解析结果保留 final handle、identity commitment，支持
`Revalidate`/`Close`，并按当前 `BoundScope`、root ID 和相对路径再次校验。binding 可供本地
诊断预览，但不能把 `PreviewToken` 当成安全的 launcher argv；未来 launcher 必须接收 binding，
在同一受信操作中重新验证并启动。

以下硬门仍未闭合：`Source.New` 的根目录 Lstat→OpenRoot 竞态、祖先 reparse/长路径和最终
launcher 的 handle-based 原子启动边界。因此本实现不能宣称完整 TOCTOU 防护、生产命令执行或
fixed MCP execution；`commandexec` 对 fixed profile 仍 fail closed。

### R5 gitprobe：固定 plan/parser，但不可执行

`internal/gitprobe` 已实现固定 `git_status`/`git_diff` plan 和有界结构化 parser：请求只包含
逻辑 `RootID`、固定 action、受限 root-relative paths 与 bounded limits；status 使用
porcelain-v1 解析，diff 解析固定 unified-diff 形状，并区分正常 EOF 与截断/超限 capture。

`Plan.PreviewExecutable()` 固定为 `false`，`PreviewBlockedReason()` 为
`repository_filters`。原因是 repo-local clean/smudge/process filters、配置与属性扩展无法由
当前固定 Git flags 完整关闭；`RootID` 也只是逻辑标识，不是 rootfs binding 或 cwd。该包不启动
Git、不接受 raw command，不连接 launcher，也没有注册 `git_status`/`git_diff` MCP 工具。

### R6：transport config、FileStore、admission 与 fake supervisor

- `internal/config` 增加 `local`、`openai_runtime`、`cloudflare_named` 三种 transport 元数据。
  transport/tunnel 字段只接受受限非秘密标识；旧连接 JSON 缺省 transport 仍按 `local` 解析。
  transport 配置本身不创建连接、不建立 Tunnel，也不包含 token/key。
- `internal/config.FileStore` 提供显式路径、校验后的 config snapshot、revision、备份/恢复和
  进程内并发保护；它明确 `ProductionReady=false`。祖先 symlink/junction/reparse、Lstat→open/
  replace 交换、hardlink/ACL、跨进程 OS lock、别名路径和掉电恢复语义仍不能由此 helper 保证。
  因而不能直接作为生产授权或 runtime 配置边界。
- `internal/admission.Gate` 提供全局与每 connection 的 bounded permits，binding 绑定
  connection/profile/revision；disable、revoke、replace 会阻止或取消后续操作，audit sink 不健康
  时新 admission fail closed，等待支持取消，permit release 幂等。它是 transport-neutral 的
  本地 gate，不执行命令、不打开文件、不建立网络连接，也不替代 MCP/OS 沙箱。
- `internal/supervisor` 提供注入式 RuntimeFactory、HealthChecker、Clock/Timer 的 fake
  automation core，按 connection 隔离状态、端口和重试；支持 backoff（含 jitter、上限 60 秒）、
  auth circuit breaker、revision replace、sleep/wake/reconnect、关闭/移除竞态以及 cleanup
  failure 终态。该 core 不创建真实 child/tunnel；`ready` 只表示注入的 local/remote checker 对
  当前 owned-child 与 revision 返回 ready。

稳定状态值如下（`stopping`、`resuming` 为过渡态）：

`stopped`、`starting`、`local_mcp_ready`、`polling`、`ready`、`degraded`、`backoff`、
`auth_failed`、`sleeping`、`resuming`、`stopping`、`cleanup_failed`。

其中 `local_mcp_ready` 不是已完成真实 MCP ping 的证明；`ready` 不是 PID+creation time+Job
ownership、Cloudflare `/ready`/HA、Tunnel health 或外部可达性的证明；`cleanup_failed` 表示
child cleanup 未被确认完成，不能转写为 `stopped`，并会阻止继续启动直到显式恢复/清理。

### R6 `runtimeowner`：Windows 本地 ownership contract（非生产）

`internal/runtimeowner` 是给未来受信 launcher/broker 使用的本地 ownership primitive，
不是进程创建器，也不接受 executable、argv、环境变量或 secret。Windows 路径要求受信调用方
提供已有的 child process handle；实现先 `DuplicateHandle`，再把 duplicate 加入包创建的
Job Object，并绑定 PID 与 process creation time。复核结果明确表示 `ancestor/tree membership`
（Windows `IsProcessInJob` 的祖先/Job-tree 语义），不能误写成 direct/leaf Job membership。
`Terminate` 只表示向 owned Job 发出终止 dispatch；只有 `WaitExited` 成功才表示已观察到退出。
关闭、认领失败或终止清理失败会保留可重试状态，而不是丢弃仍未确认释放的 handle。

该包的 `ProductionReady=false`。trusted launcher/broker、真实 child 创建与挂起/恢复生命周期、
direct-leaf membership 证明、网络隔离和 supervisor 生产接线均未完成；本地 identity/snapshot
也不可 JSON 序列化，不能把 PID 或 creation time 作为远程授权凭证。

### R6 `readiness`：本地 readiness aggregation contract（非生产）

`internal/readiness` 由受信本地适配器持有 `Issuer`，按 connection/revision/generation
创建 `Session` 和一次性 `Context`。`Session.Revoke` 会使其派生 context/attestation 立即失效。
适配器声明的 child、local MCP（auth/ping/server_info）和 remote tunnel（auth/health/HA）
证据必须在本地采样时间窗口内保持 fresh；评估器消费 one-use nonce，并按 immutable
issuer/session/connection scope 限制 replay，同时以 scope 数量与总 replay budget 设上限。
评估器应作为对应 trusted lifecycle 的长期实例复用，不能为每个请求重新创建以绕过 replay
保护；revision/generation 变化时应撤销旧 session 并创建新 session。

这些值是适配器声明的 typed observations，不是该包自行完成的 OS、MCP 或 Tunnel 证明；它们
不接线到 supervisor、runtimeowner、MCP 或 Cloudflare。`ProductionReady=false`，不能把 readiness
decision 当作启动授权或远程请求字段。

### 自动化验证证据

在 Windows amd64 / Go 1.26.0 环境，新增核心已运行并通过：

- `go test -race -count=3 ./internal/commandpath ./internal/rootfs ./internal/gitprobe ./internal/config ./internal/admission ./internal/supervisor`
- `go test -race -count=20 ./internal/readiness ./internal/runtimeowner`
- `go vet ./internal/commandpath ./internal/rootfs ./internal/gitprobe ./internal/config ./internal/admission ./internal/supervisor ./internal/readiness ./internal/runtimeowner`
- `git diff --check`

这些是本机单元/竞争/静态检查证据，不是生产运行证据；`runtimeowner` 的测试使用 Windows
kernel seam，不是实际 child/broker 生命周期证明，`readiness` 的测试使用本地构造的适配器声明，
也不是 MCP/Tunnel 健康证明。尚待人工与后续实现的关键项包括：真实 Windows child 的
PID+creation time+Job ownership、trusted launcher/broker 与 direct-leaf membership、真实
local MCP ping/server_info、Cloudflare Tunnel `/ready` 与 HA/health、两个真实 connection 的
并发/故障隔离和至少一小时 soak；FileStore 跨进程 OS lock、R4 根目录/长路径/launcher 硬门、
R5 Git root binding/filter 安全执行链、WFP/broker/capability、CLI 生产接线和独立安全审查也
仍未完成。本轮没有新增人工 Windows、网页、Tunnel、Linux runtime 或真实网络测试。

## 五、已知未完成边界

- Cloudflare Access JWT/JWKS 与 ChatGPT 身份链路已经过一次真实账号验证；该证据不等于多账号、长期稳定性或 Cloudflare/OpenAI 后端兼容性保证。Scope 仍只能由可信代码创建。
- 已有最小生产 rootfs adapter：使用 `os.Root`、逐组件 symlink/reparse 拒绝、普通文件检查、handle identity/link-count 检查和 bound adapter。Windows 本轮、WSL2 历史轮次已完成明确次数的特殊文件、路径、symlink/junction swap 及临时 loopback SMB remote-root 有界验证；本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台和完整 TOCTOU/OS 攻击覆盖仍是残余风险。
- P06 已有绑定 connection/profile/revision/root/query/预算的短期 HMAC cursor；R6 已有独立的全局/每 connection admission gate 核心，但尚未接入 MCP 的全局 wire 配额、公平排队和完整服务生命周期，也仍没有强快照。默认 cursor key 为进程随机值，因此重启后旧 cursor 会安全失效。
- R2 的 workspace snapshot 是 live、有界的目录/路径证据，不是跨文件强一致快照；其 continuation 仍需绑定当前 connection/profile/root/generation，不能直接作为可跨账号转移的授权凭证。
- Metadata 版本是弱证据，无法检测保持相同元数据的内容更改；batch 也不是全仓快照。
- 暂不回收短文件/错误项的剩余配额；优先保证可解释和确定性。
- 已验证实际 ChatGPT Business 插件、用户指定的“极高”推理档位、Cloudflare Tunnel 与一次自动重连；尚未验证两个真实账号并发、长时间运行和账号策略差异，本轮明确不以 Pro 模式替代。
- 未完成独立审查、生产发布或任意命令开放；R4/P08 虽已有 Windows 固定映像/句柄守卫、
  同一 guard handle 的执行期 SHA256、单进程 Job、严格 version profile、canonical resolved-input
  digest、confirmation v2（绑定 digest+revision，旧 v1 fail closed）、revision lease、
  local commandexec fail-closed bridge、结构化 `ProcessOutcome`、本地 `command.result` 接线、
  一次性确认、R4-AUDIT-01 commandexec 审计边界和 audit.v2 command producers，但 WFP/broker/service
  network deny、capability 铸造、CLI/supervisor 生产接线、fixed_command 的可信最终执行链、
  trusted final resolver、签名校验和 MCP `run_probe` 均未完成，远程命令必须保持关闭；typed
  runtime values 目前只进入本地 Prepare/Confirm/BuildRequest 边界，不能据此声称 fixed 可执行。
  已有 pre-open writable/mapped handle 残余风险；`LockFileEx` 不覆盖 mapped view，不能作为完整修复。
- R4 的 commandpath binding、R5 的 Git plan/parser 和 R6 的 transport/admission/supervisor、
  `runtimeowner`、`readiness` 均仍是本地核心。R4 尚缺 `Source.New` 根目录交换竞态、长路径和
  launcher 原子硬门；R5 的 `Plan` 明确 `Executable=false`，repo-local filters 与 root binding 未
  解决，故不接 MCP；R6 的 FileStore、`runtimeowner`、`readiness` 均明确 `ProductionReady=false`。
  跨进程 OS lock、trusted launcher/broker、direct-leaf ownership、真实 Windows child/tunnel
  runtime、MCP/Cloudflare health、双 connection/soak 和 CLI/GUI 生产接线均未完成。

## 六、下一执行者的明确入口

P06 的 `list_directory`、`find_files`、`search_text`、`tree_directory` 已部署并由现有 ChatGPT Business“极高”会话验证分页、发现、拒绝路径和后续范围读取；实测发现并修复了 `search_text` 返回 matches 时 `coverage.returned_entries` 未累加的问题，复测 10 个 matches 与计数一致。R1 的 direct-search 第一增量、audit.v1、R3 的无进程 `discover_tools`/`get_environment` 和 R2 的 lines/tail/snapshot 已完成本地 MCP 接入、脱敏/allowlist 测试，但 R2 新增能力尚未声称真实 Business 复测。R4 已补齐严格 version args、`version_probe` 的 `slots:[]` 兼容、`fixed_command` exact argv/typed slots 本地解析与纯 `ResolveVariant`、root ID 交叉校验、同一 guard handle 的执行期 SHA256、canonical resolved-input digest、confirmation v2（绑定 digest+revision，旧 v1 fail closed）、revision lease、local commandexec fail-closed bridge、结构化 `ProcessOutcome` 与本地 `command.result` 接线、R4-AUDIT-01（AuditRecorder、`command.reject`、`command.admission`/`command.start` 和审计失败 fail closed）、NET-01 contract/fake 以及 R4-NET-02 固定 plan/DisabledBackend；`fixed_command` typed runtime values 已进入本地 Prepare/Confirm/BuildRequest 边界，但字符串 `PathResolver` 尚不是可信 final path binding，执行仍在 confirmation/admission/start/runner 前以 `unsupported_profile` 拒绝。R4 的 commandpath identity binding、R5 的固定 Git plan/parser 和 R6 的 transport config、非生产 FileStore、admission gate、fake supervisor automation core 已完成，但均不等于生产接线。下一增量实现可信 path resolver/final identity 和 Windows UTF-16/escaping 边界，并继续保持 shell/interpreter、Git plan 与 fixed MCP execution 关闭；真实 WFP ABI adapter、低权限 broker/service、由 broker 持有的 suspended Job integration、VM identity/network adversarial tests、Windows runtime/MCP/Tunnel health、双 connection soak 和 CLI/supervisor 生产接线仍按后续路线推进。硬门全部通过前不注册 MCP `run_probe`，也不开放 arbitrary shell 或模型自定义命令行。


R5 受控 Git read actions、R6 P09/P10 多 connection/supervisor/recovery、R7 证据驱动的 index、R8 GUI/tray、R9 发布与独立审查依次推进；P14 写入始终独立。P04 的本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台仍按路线的停止条件跟踪，不把本地有界验证写成生产安全结论。
