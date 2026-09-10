# 实际实施状态与验证记录

日期：2026-09-10。当前等级：**K0 内核原型 + P04 平台有界验证 + P05 真实 ChatGPT/Cloudflare Tunnel 调用通过 + P06 有界文件发现与 literal 搜索已实现；P08 固定环境探针核心尚未接入 MCP。** 下一阶段执行路线见 [`docs/NEXT_PHASE_PLAN.zh-CN.md`](NEXT_PHASE_PLAN.zh-CN.md)。

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
| P04 | 部分实现；平台有界验证完成 | `config`/`policy.BoundScope`、Go 1.25、基于 `os.Root` 的只读 rootfs、`readcore` bound adapter；Windows 与 WSL2 的特殊文件、路径、symlink/junction swap 和临时 loopback SMB remote-root 测试已按边界完成；仍不是独立安全审查或发布结论 |
| P05 | 最小 MCP 与真实 Cloudflare ingress 已验证 | 官方 MCP Go SDK v1.7.0、现代无状态/旧版有状态 Streamable HTTP 协商、本地 bearer 与 Cloudflare Access JWT/JWKS、Host 校验；ChatGPT“极高”实际调用 server_info/ping/read_file/batch_read 通过，Tunnel 重连后无需重新登录 |
| P06 | 第一增量已实现并通过本机与真实链路验证；手工靶场已加入 | list_directory、find_files、search_text、tree_directory；有界迭代、扁平深度优先树、literal UTF-8 搜索、deny/ignore、签名短期 cursor、revision/generation 失效和覆盖率说明；四个发现工具已由 ChatGPT Business“极高”验证，`manual-test-targets/` 提供分页、嵌套、Unicode、空文件和拒绝负例 |
| P07 | 未实现 | workspace snapshot、行范围与模型任务效果评测 |
| P08 | 核心第一增量已实现，未接入远程工具面 | 固定 tool_exists/tool_version 核心、显式受信任可执行路径、私有环境、输出/超时限制与进程树终止；不提供任意 Shell，配置/MCP 接入和独立安全复核待完成 |
| P09/P10 | 未实现 | 多 connection 配置、真实隔离、官方 runtime supervisor、故障恢复 |
| P11–P14 | 未实现 | 索引、产品化、独立审查、可选写入 |

代码位置：`internal/config/`、`internal/policy/`、`internal/readcore/`、`internal/rootfs/`、`internal/mcpserver/`、`internal/cfaccess/`、`cmd/readcore-demo/` 和 `cmd/local-probe-mcp/`。Cloudflare 本地增量由 Luna 5.6 Max 子代理起草，主代理在其两次未能按时收尾后接管审查、修正与验证。

### P04 增量事实（2026-09-09）

- `config`/`policy` 已修复配置上限、重复 key、显式 `enabled` 语义，并由 `policy.BoundScope` 绑定 connection/profile revision 和 root/path deny。
- `rootfs.Source` 从配置快照持有 Go 1.25+ `os.Root`，只读打开普通文件；`BoundScope` adapter 接入 `readcore.Engine`，Open、Metadata、ReadAt 均重新验证授权和撤权。
- deny 匹配已覆盖 Windows 大小写不敏感语义与 `**`；Source revision 校验阻止“旧 Source + 新 scope”在 root ID 复用后访问旧 root。
- Windows handle metadata 已纳入 volume/file identity、link count 和 reparse attributes；hardlink、junction/reparse、Unicode/组合字符、长路径、ADS/保留名/非法路径和 symlink+junction swap 测试通过。临时通过 `New-PSDrive -Name Z -PSProvider FileSystem -Root '\\localhost\C$' -Persist` 将 `Z:` 映射到 loopback SMB 共享时，`GetDriveTypeW` 返回 4，remote-root 测试通过；finally 执行 `Remove-PSDrive -Name Z -Force`，并复核 PowerShell PSDrive、LogicalDisk 和 `net use` 均无 `Z:`。
- WSL2 Ubuntu-22.04（Linux 6.6.87.2-microsoft-standard-WSL2、Go 1.26.2）临时复制当前工作树后，FIFO、Unix socket、hardlink、symlink swap 的目标测试 `-count=5 -v` 及 race 版本通过，`go vet ./...` 通过；这不是裸机 Linux 证据。机器可读记录见 `docs/evidence/P04-platform-verification.json`。
- 上述是实现者在明确环境和次数下的有界验证，不是数学证明，也不代表 P04 全部放行；本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台、独立安全审查仍是残余风险。P05 的本地 MCP 生命周期已测，但 P01 的真实账号/Tunnel 与 App 调用仍未实测。

## 三、真正运行过的验证

早期 Linux 内核验证环境为 **Go 1.23.2 / Linux amd64**，无容器外网 DNS；GitHub 操作通过连接器完成。当前 `go.mod` 已提升到 Go 1.25.0，Windows rootfs 增量验证使用 Go 1.26.0，WSL2 平台验证使用 Go 1.26.2；Cloudflare Access 验证使用 `coreos/go-oidc` 及其间接依赖，因此首次构建需要模块缓存或网络。工具链通过不等于产品发布版本。

| 检查 | 实际结果 | 限制 |
|---|---|---|
| `go test -count=1 -v ./...` | PASS；22 个顶层 Test 函数，另有子测试和 fuzz seeds | 实现者自测 |
| `go test -race -count=1 ./...` | PASS；两个包 | 不等于真实多账号/全局调度验证 |
| `go vet ./...` | PASS，退出码 0 | 静态检查不是安全审计 |
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

## 四、已知未完成边界

- Cloudflare Access JWT/JWKS 与 ChatGPT 身份链路已经过一次真实账号验证；该证据不等于多账号、长期稳定性或 Cloudflare/OpenAI 后端兼容性保证。Scope 仍只能由可信代码创建。
- 已有最小生产 rootfs adapter：使用 `os.Root`、逐组件 symlink/reparse 拒绝、普通文件检查、handle identity/link-count 检查和 bound adapter。Windows 与 WSL2 已完成明确次数的特殊文件、路径、symlink/junction swap 及临时 loopback SMB remote-root 有界验证；本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台和完整 TOCTOU/OS 攻击覆盖仍是残余风险。
- P06 已有绑定 connection/profile/revision/root/query/预算的短期 HMAC cursor；仍没有全局多连接公平调度、完整 MCP wire 限额或强快照。默认 cursor key 为进程随机值，因此重启后旧 cursor 会安全失效。
- byte offset 的 continuation 不能直接作为可跨账号转移的授权凭证。
- Metadata 版本是弱证据，无法检测保持相同元数据的内容更改；batch 也不是全仓快照。
- 暂不回收短文件/错误项的剩余配额；优先保证可解释和确定性。
- 不能强制取消任意阻塞 OS I/O；当前 Source 约束与未来平台测试必须明确。
- 已验证实际 ChatGPT Business 插件、用户指定的“极高”推理档位、Cloudflare Tunnel 与一次自动重连；尚未验证两个真实账号并发、长时间运行和账号策略差异，本轮明确不以 Pro 模式替代。
- 未完成独立审查、生产发布或任意命令开放；P08 仅允许固定动作，且在安全复核和 MCP 接入完成前不对远程客户端暴露。

## 五、下一执行者的明确入口

P06 的 `list_directory`、`find_files`、`search_text`、`tree_directory` 已部署并由现有 ChatGPT Business“极高”会话验证分页、发现、literal 搜索、拒绝路径和后续范围读取；实测发现并修复了 `search_text` 返回 matches 时 `coverage.returned_entries` 未累加的问题，复测 10 个 matches 与计数一致。近期顺序按 [`docs/NEXT_PHASE_PLAN.zh-CN.md`](NEXT_PHASE_PLAN.zh-CN.md) 执行：先 R0 冻结能力分层、command profile、audit.v1 与 annotation 契约；再做 R1 的 direct 读取稳定性、cursor/coverage、全局资源预算和日志基础设施；R3 的无进程 `discover_tools`/`get_environment` 可与 R1 并行。R2 的 lines/tail/snapshot 依赖 R1 证据，R4 的 P08 TOCTOU 修复和固定探针必须通过 Windows/Unix 硬门后才可考虑 MCP 暴露；不开放模型自定义 command、args、cwd、env 或 timeout。

R5 受控 Git read actions、R6 P09/P10 多 connection/supervisor/recovery、R7 证据驱动的 index、R8 GUI/tray、R9 发布与独立审查依次推进；P14 写入始终独立。P04 的本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台仍按路线的停止条件跟踪，不把本地有界验证写成生产安全结论。
