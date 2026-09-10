# 实际实施状态与验证记录

日期：2026-09-10。当前等级：**K0 内核原型 + P04 平台有界验证 + P05 最小 MCP/Cloudflare Access 本机验证；尚未完成真实 ChatGPT/Cloudflare Tunnel 调用。**

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
| P01 | 本机准备完成；账号实测待凭据 | 官方 tunnel-client v0.0.14、loopback profile、脱敏预检脚本；没有真实账号、模型或 Tunnel 调用证据 |
| P02 | 完成内核部分 | Scope/Source/Handle/Request/Result/Limits、接口边界、威胁模型 |
| P03 | 完成当前 byte-range 内核 | 有界 ReaderAt、确定性公平批量、部分失败、版本/UTF-8/取消、demo、测试 |
| P04 | 部分实现；平台有界验证完成 | `config`/`policy.BoundScope`、Go 1.25、基于 `os.Root` 的只读 rootfs、`readcore` bound adapter；Windows 与 WSL2 的特殊文件、路径、symlink/junction swap 和临时 loopback SMB remote-root 测试已按边界完成；仍不是独立安全审查或发布结论 |
| P05 | 最小 MCP 与 Cloudflare Access 本地 ingress 已实现；真实链路待测 | 官方 MCP Go SDK v1.7.0、Streamable HTTP、本地 bearer 与 Cloudflare Access JWT/JWKS 验证、显式 Host 校验、server_info/ping/read_file/batch_read、本地 SDK 生命周期与隔离测试；workspace_snapshot、完整 wire 预算和原生 App/Tunnel 证据待完成 |
| P06/P07 | 未实现 | 目录分页、行范围、内容搜索、workspace snapshot、模型任务效果评测 |
| P08 | 未实现 | 有限环境/Git 探查，不提供任意 Shell |
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

- Cloudflare Access JWT/JWKS 身份认证已实现为显式本地 ingress 模式；尚无真实 Cloudflare Access edge 或 ChatGPT 身份链路证据。Scope 仍只能由可信代码创建。
- 已有最小生产 rootfs adapter：使用 `os.Root`、逐组件 symlink/reparse 拒绝、普通文件检查、handle identity/link-count 检查和 bound adapter。Windows 与 WSL2 已完成明确次数的特殊文件、路径、symlink/junction swap 及临时 loopback SMB remote-root 有界验证；本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台和完整 TOCTOU/OS 攻击覆盖仍是残余风险。
- 没有签名 cursor、全局多连接公平调度、完整 MCP wire 限额或强快照。
- byte offset 的 continuation 不能直接作为可跨账号转移的授权凭证。
- Metadata 版本是弱证据，无法检测保持相同元数据的内容更改；batch 也不是全仓快照。
- 暂不回收短文件/错误项的剩余配额；优先保证可解释和确定性。
- 不能强制取消任意阻塞 OS I/O；当前 Source 约束与未来平台测试必须明确。
- 未验证实际 ChatGPT App、用户指定的“极高”推理档位、Tunnel、P01 真实连接、账号政策或两个真实账号并发；本轮明确不以 Pro 模式替代。
- 未完成独立审查、生产部署或发布；没有运行任意本地命令或修改用户项目。

## 五、下一执行者的明确入口

跟踪 P04 的残余风险（本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台）并安排独立安全审查；随后按主计划 P01 取得真实最小 echo 调用证据，并在不改变本地安全边界的前提下验证 Cloudflare Access/Tunnel edge 与 ChatGPT App。当前不得据此开放 listener 或声称产品已可连接 ChatGPT。允许修改 `internal/rootfs`、`internal/policy`、`internal/config`、`internal/mcpserver`、`internal/cfaccess`、对应 tests/docs 及依赖文件；不在这一任务包增加索引、UI、Shell 或写文件。

之后依次推进 P06/P07 的调查效果，P08 的固定探查动作，以及 P09/P10 的多连接隔离与恢复。没有新增高成本架构问题时，不必再让 Pro 重写一遍计划。
