# R12 开发材料与接手入口

交接日期：2026-10-24。适用对象：接手 GUI 的开发人员，以及协作补齐本地管理接口的后端人员。

仓库默认入口为[英文开发指南](R12_DEVELOPMENT.md)。最新 R12 从 `feat/r12-dual-mode` 获取；此前 ZIP 保留为独立时间点快照。

## 1. 先取得正确源码

R12 的持续开发入口是仓库 `feat/r12-dual-mode` 分支，包含原先未提交/未跟踪的完整实现。克隆时使用 `git clone --branch feat/r12-dual-mode git@github.com:LE-saber/Local-Probe.git`，并记录实际提交；只克隆旧 main 或最初基线会缺少 R12。本轮只上传开发源码，不代表已合并 main 或发布 Release。

此前已交付的 ZIP 是以 `dd0a6b73981d8006403913e400d698c3b8fee291` 为基线的工作树快照，没有 `.git`。下面包结构说明仅适用于该存档；仓库接手直接使用英文开发指南，不需要额外下载本机快照。

交付包目录：

```text
README.zh-CN.md          交付包使用说明
SNAPSHOT.json            基线、文件数、排除范围与验证范围
SOURCE_SHA256SUMS.txt    source 下每个文件的 SHA-256
Verify-Snapshot.ps1      解压后核对完整性，不运行项目
source/                 可独立构建的当前源码、测试、示例和文档
```

在新目录解压并校验，保留原 ZIP 作为交接基准。不要覆盖已有工作树、执行 reset/clean，或把这个快照当作正式版本安装到用户环境。已有仓库需要整合时，在独立分支/工作目录中比对后导入；快照包含新增文件，不能仅应用 tracked diff。

本包不提供真实运行配置、配对身份、令牌、私钥、CA、SSH 信息、账本、用户备份、日志、编译缓存、现成 EXE 或 Go 工具链。示例配置并不授权任何真实连接；接手人应创建自己的临时工作区和测试身份。

## 2. 阅读顺序

1. [GUI 开发交接书](GUI_HANDOFF.zh-CN.md)：视觉基准、交互、现有 RPC、拟议契约和验收要求。
2. [R12 最新进展](R12_PROGRESS.zh-CN.md)、[实施状态](IMPLEMENTATION_STATUS.md)：从上往下阅读，后面的旧记录属于历史。
3. [操作后端](R12_OPERATIONS_BACKEND.zh-CN.md)：本地审批、作业、运行器、文件写入及实际限制。
4. [执行契约决策](R12_EXECUTION_DECISION.zh-CN.md)：已经批准 `trusted_host`，不需要重新决定同一产品选择。
5. [配对与通道](R12_PAIRING_AND_TRANSPORT.zh-CN.md)、[Relay 部署记录](R12_RELAY_DEPLOYMENT.zh-CN.md)。
6. [R12 总体技术计划](R12_DUAL_MODE_REMOTE_IMPLEMENTATION_PLAN.zh-CN.md)：后续任务与总体架构；计划中的接口不代表已实现。
7. [本地操作验证证据](evidence/r12/2026-10-24-operations.md)、[通道证据](evidence/r12/2026-10-24-transport.md)。

以真实代码、最新证据和执行决策为准。历史计划中的严格网络隔离要求没有在新模式中实现；`trusted_host` 使用当前用户的文件和网络权限，**不是沙箱**。

## 3. 当前可以复用的成果

| 范围 | 已有实现 | 尚不能据此宣称 |
|---|---|---|
| GUI | 五页正式页面、桌面桥接、原生备份对话框、主题/DPI处理、24个前端VM场景 | R12 GUI 已接好、所有显示器均已人工验收 |
| 身份/通道 | 持久化配对、撤销、端点 mTLS、原生 stdio Agent、直接 WSS 与静态 Relay | 自动续期、动态路由/票据、身份灾备已完成 |
| 操作后端 | Windows 命令/脚本/版本/build/test、文本写入/精确补丁、逐次本地审批、作业与取消 | Web/GUI 高级入口或 Linux 目标执行已完成 |
| 并发 | 两连接各一个真实作业，互相隔离 | 至少三条独立生产连接宿主已交付 |
| 文件规则 | 只读边界、deny/ignore、离线读取/预览/提交、写前复核 | 在线规则立即撤权与选择性 epoch 已全部实现 |
| 验证 | 全项目 Go test/race/vet、实际运行器/构建测试、本机加密操作闭环已通过 | 新操作经 Linux 电脑和真实公网 Relay 已通过 |

公网只读中转曾实际验证；当时 Agent 和目标仍在同一 Windows 电脑。新增操作后端尚未完成跨机中转验收，不能挪用旧证据。用户另有 Linux 电脑，要求开发完成后再测试；本材料不含其访问方式。

## 4. 工程入口与分工

| 路径 | 接手用途 | 责任边界 |
|---|---|---|
| `internal/desktopweb/assets/` | 正式 HTML/CSS/JS/SVG，现有风格的唯一正式基线 | GUI 主责 |
| `internal/desktopbridge/` | 本地 RPC、snapshot、备份、规则与旧开发者投影 | GUI 适配；权限契约与后端协作 |
| `internal/desktophost/`、`third_party/go-webview2/` | WebView2 宿主、原生对话框、主题与 DPI | 修改须保留真实 Windows 验证 |
| `internal/operations/` | 私有账本、审批、执行、状态和本地取消 | 后端核心，GUI 通过可信本地接口使用 |
| `internal/deviceidentity/` | 配对、授权主体、撤销与证书 | 后端核心；页面不接触私钥 |
| `internal/mcpserver/operations.go` | 配对 MCP 的八个操作工具 | 远程入口没有本地批准权限 |
| `internal/agentbridge/`、`internal/peertransport/`、`internal/relay/` | Agent 转发、加密流、静态中转 | 后端/后续集成 |
| `cmd/local-probe-target/` | 目标服务与可选操作引擎 | 独立后端入口 |
| `cmd/local-probe-agent/` | 原生 Agent 的 stdio MCP 桥接 | Linux/Windows Agent |
| `cmd/local-probe-pair/`、`cmd/local-probe-operations/` | 本地配对、检查、批准/拒绝/取消 | 可作为 GUI 管理契约的行为参照 |
| `deploy/operations/catalog.example.json` | 空的运行器目录表示例 | 按本机真实路径和哈希配置，不预授权 |
| `deploy/relay/` | 通用 Relay 示例和测试部署脚本 | 阅读材料，不自动部署或改真实服务器 |

旧 `DesktopBridge.Service` 仍为单 controller；旧 `developer.go` 的 `network_enforcement_required` 不属于新操作引擎。不得仅改按钮状态或删旧门来假装新后端可用。

GUI 负责人优先完成页面和类型化适配；管理接口由后端协作补齐，不把安全核心搬进 JS，不另做一套执行器。接口正式落地前可用独立样稿评审，正式页面不得输出模拟健康、在线或批准状态。

## 5. 必须先对齐的本地接口

现有 envelope 为 `{id,method,params}` → `{id,ok,data,error}`，不是 JSON-RPC 2.0。完整已有方法和建议新增方法见 GUI 交接书；下表是**待设计/实现的接口范围，不是现有可调用方法**。

| 范围 | 必要数据和行为 | 实施注意 |
|---|---|---|
| 能力与连接 | 用途、通道、目标、单连接生命周期、可用能力与拒绝理由 | 配置选择、传输建立、内层认证和 MCP 就绪分别显示 |
| 配对 | 公开指纹、CSR/bundle 导入导出、连接绑定、撤销 | 可信本地操作；不把私钥或签发权限交给页面/模型 |
| 开发者配置 | trusted_host 明示、连接许可、可写 profile、运行器哈希 | 默认只读；改模式不自动批准任务 |
| 本地审批 | 精确计划、主体、目录、argv/脚本、文件前后内容、digest、失效原因 | 批准只走本地可信管理入口，绑定完整计划 |
| 作业 | 状态、分页输出、本地拒绝/取消、unknown/中断 | 本地查看不能误续 Agent 的运行租约 |
| 在线规则 | 预览、CAS、影响连接、撤权与活动任务处理 | 依赖后端；当前不能宣传在线即时生效 |
| 诊断/恢复 | 脱敏健康信息、配置备份恢复范围 | 恢复不能复活私钥、撤销授权或开发者批准 |

新增 DTO 要标注必填/可空、版本、错误码、分页/大小限制和取消语义；先写契约/后端测试，再接正式 GUI。

## 6. 首次开发环境与检查

Windows amd64 为操作目标和 GUI 的验证环境。仓库 `go.mod` 最低 Go 1.25.0，已有完整证据使用 Go 1.26.5；建议先复用同一版本重现。模块初次获取需要网络或可信模块缓存；本包不是离线依赖包。保留 `go.mod/go.sum` 和本地 WebView2 replace。

GUI 需要 Windows WebView2 Runtime；前端逻辑测试需要 Node。race 需要该 Go 版本支持的 C 工具链。Python/Node/PowerShell/cmd 为可选真实脚本测试运行器；不因为缺少它们就伪造版本或自动安装软件。

从解压后的 `source` 目录执行：

```powershell
go version
node --version
go mod verify
node scripts/Test-LocalProbeDesktopFrontend.mjs
go test ./...
go test -race ./...
go vet ./...
```

测试在开发人员正常用户环境执行，使用合成目录；已有 Windows 受限令牌/DACL 测试可能受自动化沙箱身份影响，失败需记录实际原因，不能删断言。TEMP/TMP 建议指向仓库隐藏的 `.tools` 子目录，避免 `./...` 枚举暂存构建目录；不要更改全局环境。

独立后端构建示例：

```powershell
go build -trimpath -o bin/dev/local-probe-target.exe ./cmd/local-probe-target
go build -trimpath -o bin/dev/local-probe-agent.exe ./cmd/local-probe-agent
go build -trimpath -o bin/dev/local-probe-pair.exe ./cmd/local-probe-pair
go build -trimpath -o bin/dev/local-probe-operations.exe ./cmd/local-probe-operations
```

完整配对/启动顺序以通道说明为准。不要把示例中的省略号当作启动参数；命令行真实 flags 在相应 `main.go` 中。

GUI 构建：先阅读 `scripts/Build-LocalProbeDesktop.ps1`，再指定本机 `-GoPath`、独立输出目录。脚本还会写 `bin/local-probe-mcp.exe`；应在解压的新目录构建，不能覆盖用户正在运行的二进制。启动/打包的脚本见 GUI 交接书。

真实运行器 opt-in：设置进程级 `LOCAL_PROBE_OPERATIONS_REAL_TOOLS=1`，并提供 `LOCAL_PROBE_OPERATIONS_PYTHON`、`LOCAL_PROBE_OPERATIONS_NODE`、`LOCAL_PROBE_OPERATIONS_POWERSHELL`、`LOCAL_PROBE_OPERATIONS_CMD` 的本机绝对路径；定向运行 `TestInstalledPythonNodePowerShellScriptsAndVersions` 和 `TestRealGoBuildAndTestProject`。默认跳过不代表通过，Go build/test 还需本机可用的 Go。

## 7. 开发顺序和可验收成果

1. **基线重现**：校验包，运行前端与 Go 基线，列出环境差异；不得把历史 PASS 写成本机新结果。
2. **契约评审**：确认能力/错误/状态，本地审批、身份与连接控制的接口先有后端实现和测试。
3. **单连接闭环**：用途/配对/授权 → Agent 请求 → 本地查看精确计划 → 批准/拒绝 → 实际结果/取消；保持只读兼容。
4. **生产控制面**：后端补在线配置、连接级选择性 epoch、至少三条独立连接宿主，再接 GUI；两并行作业不能代替验收。
5. **桌面回归**：五页、树/拓扑/列表、主题与原生菜单一致、备份对话框、复制/右键/刷新、长文本和草稿保留；人工检查 100/125/150/200% 与跨屏。
6. **跨机中转**：开发完成后，经用户提供 Linux 访问方式，测试 Linux Agent → 真实 Relay → Windows 目标；使用独立测试身份和合成文件，不使用用户工作目录。需要时由所有者安全提供短期凭据和有效 CA，不随源码交付。

每一步交付实际修改、接口说明、测试结果、失败/未测试项和独立构建。GUI 保留当前暖灰/中性暗色风格，不能重做为蓝紫主题或纯表格站；完整视觉要求见 GUI 交接书。

## 8. 当前限制与禁止误报

- Windows trusted_host 继承当前用户权限；进程 Job 限额不是文件/网络沙箱。不新增账号或修改系统网络策略来绕过既定边界。
- 作业有全局 2/每连接 1 的并发限制、5 分钟批准窗口和 60 秒 Agent 状态轮询租约；输出结束后分页读取，不是交互式终端。
- 账本有 128 个历史作业和约 64 MiB 的预算，满额拒绝，尚无自动归档。
- 文本文件上限 128 KiB；精确补丁最多 32 项，非完整 unified diff。已有文件更新并非原子替换，异常可能 unknown，before image 用于人工恢复。
- 当前配置整体 revision 变化会使旧计划失效；尚无完整连接级选择性热更新。
- Linux Agent/Relay 与 Linux 目标执行是不同能力；当前不支持 Linux 目标执行/写入。
- Relay 是静态角色路由开发版；动态凭据、自动证书生命周期和正式运维交付未完成。原测试叶证书将于 2026-11-07 到期，测试前需核对，不能关闭 TLS 验证。
- 用户未授权本次交接执行 push/merge/Release、安装服务或改真实连接；源码材料不含部署凭据。

## 9. 对接后的反馈格式

请交回：修改基准/文件清单、已实现流程、接口新增/变更、自动检查及人工验收证据、待后端协作事项、失败和未测范围。GUI 专业人员可直接从 [GUI 开发交接书](GUI_HANDOFF.zh-CN.md) 的“给接手人的开始指令”继续，后端协作以本文件的接口和阶段边界为准。
