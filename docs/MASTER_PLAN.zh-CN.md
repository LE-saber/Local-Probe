# Local-Probe 总体计划与逐步骤实施手册

版本：1.0；制定日期：2026-09-07。目标仓库：`LE-saber/Local-Probe`。

2026-10-06 执行范围调整：用户要求本阶段完成七项能力，包含工作文件写入、构建测试及自选脚本。执行顺序及验收以 [后端优先开发计划第 2 版](R11_BACKEND_FIRST_PLAN.zh-CN.md) 为准；本文历史 P14 后置和固定探针范围保留作原始设计记录，不再用于排除这三项必交付功能。默认只读、显式授权及隔离原则不变。

**文件性质：先于实现提交的完整实施计划，不是已实现功能清单。** 各阶段的实际完成情况、测试命令、提交和阻塞项另记在 `docs/IMPLEMENTATION_STATUS.md`。文中拟建文件及命令，只有相应阶段完成后才保证存在。

## 0. 执行定位、依据与使用方法

本项目依照 `LE-saber/deep-project-orchestrator` 的实际规则推进：Pro 负责理解目的、架构裁决、关键实现和验证设计；适合机械执行的工作拆成可独立验收的任务交给执行者。编排仓库仅供阅读，不存放本项目代码。先完成本计划，再开展目标仓库其他工作；不把计划替代实现，也不把未经验证的实现称为产品完成。

本计划有三个层次：第一至五章冻结目的、边界、架构与验收方法；第六章给出按依赖排列的逐步骤实施程序；第七至十章给出交接、风险、当前执行范围和来源。每个实施阶段都包含输入、操作、输出、测试和停止条件。执行者应按任务编号记录证据，不得把后续示例命令当作已经成功运行。

### 0.1 已核实的起点

- 本轮检查时，Local-Probe 是空的私有仓库，默认分支名为 `main`，当前 GitHub 连接具有写权限。因此首次直接提交本计划以初始化仓库；后续实现使用新分支和 PR，不自动合并。
- 已读取编排仓库的 `README.md`、`skill/deep-project-orchestrator/SKILL.md`、`references/software-projects.md` 和 `references/verification.md`。
- 已通过当前 GitHub 内容核对 LCA 的 README/工作流、ChatCMD 的许可证及 repository-index ADR、Codex Free 的 README/连接方式，以及 OpenAI 官方 Tunnel 和 Developer Mode 文档。
- 对话中先前出现的模型版本名、账户套餐、Windows 使用环境、项目规模、星数、成熟度评分和估计调用次数，不作为已经证实的用户事实或基准成绩。

### 0.2 本轮的重要纠正

1. OpenAI Developer Mode 指南当前列出 Plus、Pro、Business、Enterprise、Education，而 Help Center 页面仍将 Full MCP 与 Business/Enterprise/Edu 关联，并描述 Pro 的 read/fetch 范围。两者口径不一致。因此不继续使用先前那张绝对套餐支持表，改为真实账户能力预检。
2. ChatGPT Developer Mode/App 权限与 OpenAI Platform 的 Tunnel Read/Use/Manage 权限分别验证；有订阅不等于已获 Tunnel 权限。
3. 一个 tunnel 可关联多个 organization/workspace；不是技术上强制一个账号一个 tunnel。为隔离权限，默认一条本地 connection 对应一个受控 ingress/profile。没有可信终端用户身份时，同一 connection 下的成员共享同一权限，不能声称实现了逐成员 RBAC。
4. LCA README 当前标注 AGPL-3.0；ChatCMD LICENSE 是 MIT。不能把“借鉴交互设计”直接等同于复制混用源代码。首版不拷贝 LCA 的代码、提示词或 UI；公开发布许可证由仓库所有者决定。
5. ChatCMD 当前 ADR 记录了负面性能结果：百万路径的 indexed find 在其测试中慢于直接遍历，batch stat 也慢于 sequential。它证明某些机制已被实现和测试，不证明索引或批处理对所有负载更快。本项目必须自己做对照测试。
6. Streaming 解决内存增长，不自动让“第千万行”随机读取变快；没有行偏移索引时，定位远处行仍可能扫描前缀。字节定位、行定位和扫描预算必须分别定义。
7. 元数据版本标记不等于强内容快照，更不等于整个仓库事务快照。只读工具也会向模型传输敏感数据；Tunnel 不会消除数据披露风险。

## 一、目的与明确边界

### 1.1 要交付什么

交付一个本机程序，让用户在正常的 ChatGPT Web 中通过原生自定义 App/Plugin 调用 MCP 工具，高效调查被授权的本地文件、代码仓库、Git 状态和开发环境。重点是让模型用较少的无效往返获得可追溯的真实证据，而不是增加一个拥有大量工具的新 Coding Agent。

用户明确的优先级保持不变：

1. 完整、有效地读取本地文件。
2. 执行简单命令来探查本地环境。
3. 工作可靠性和稳定性。
4. 修改文件。

安全与权限是以上功能的共同准入条件，不是可被优先级交换掉的功能。

### 1.2 “完整读取”的操作定义

“完整”指授权范围内的文件可以被发现、搜索，并按需通过分页、范围读取和批量读取逐步访问。它不意味着一次上传整个磁盘或保证模型读过每个文件。每个结果要说明：范围、版本、是否截断、未覆盖原因、下一步如何继续。

所有文件类型应能在授权范围内被发现并取得安全的元数据；首版重点返回 UTF-8 文本。二进制、不可解码文本、超限文件、权限拒绝、忽略目录和暂不支持的链接应显式报告，不能悄悄消失后声称搜索完整。PDF/Office/图片内容解析、压缩包递归解析、数据库全文读取不是首版默认能力。

### 1.3 必须覆盖的用户场景

- 在一个陌生仓库内定位某项业务功能，找出相关文件、入口、调用关系并给出有路径/范围依据的说明。
- 一次读取一组已知相关文件，不让模型逐个调用几十次；单项失败不影响其他项。
- 查看巨大日志的一段或尾部，不把整个日志读入内存；长行和无换行文件也不能造成失控。
- 查询 Git status/diff、工具安装位置和版本；命令参数、输出和运行时间受控。
- 保存多个 connection/tunnel 配置；多个账号或 workspace 可以同时使用同一套本地核心。
- 不同 connection 仅能访问自己的 roots/tools；停用一个连接不影响其他连接。
- 本地服务、网络或 tunnel 重启后可诊断、恢复；旧 cursor 不得误续读到其他文件或其他 profile。

### 1.4 首版非目标

不做 ChatGPT 客户端、模型代理、订阅共享/绕过、Codex 替代品、完整 IDE、自动规划 Agent、子 Agent 系统、浏览器 DOM 自动化、任意远程 Shell 或公网插件市场上架。不能承诺所有订阅、所有模型、Agent Mode、Deep Research 都支持相同工具。

不默认开放整个系统盘，不自动收集全部环境变量，不读取浏览器凭据，不上传 `.env`、私钥和系统秘密。没有授权不执行 package install、测试脚本、构建脚本、Git hook、容器控制或数据库修改。

### 1.5 本计划采用、但并非用户已明确确认的假设

| 项目 | 暂定值 | 理由及替代影响 |
|---|---|---|
| 首要平台 | Windows 桌面；Linux 为 CI/开发环境，macOS 后续 | 沿用对话的产品方向，但用户未直接确认；核心不绑定平台，平台优先级可调整 |
| 用户范围 | 同一个设备所有者管理的若干可信账号/workspace | 不是面向不可信租户的 SaaS；若需逐成员身份和审计，须增加 OAuth/身份映射 |
| 文件范围 | 显式添加的一至多个本地 root | “本地完整访问”不解释为越过 OS 权限或所有目录自动授权 |
| 首版写入 | 默认不提供写工具；保留后续独立 write profile | 写入为第四优先级，先防止读取和连接被拖累 |
| 交付形态 | 核心进程 + CLI；稳定后增加轻量本地管理页面 | 不先引入 Electron；后续可加托盘，不影响核心协议 |
| 云端数据 | 用户选择的文件片段会发送到 ChatGPT | 不做“数据绝不离开本机”承诺；适用账户数据政策另行确认 |

## 二、独立架构裁决

### 2.1 不把三个项目直接拼成一个产品

保留三个有价值的思想：LCA 的概览→搜索→批量读取；ChatCMD 的有界 I/O、分页、版本与部分失败语义；Codex Free/官方客户端的原生 MCP + Tunnel 生命周期。但不默认 fork 任意整个产品，不引入其 Coding Agent、记忆、浏览器、PTY 等非必要子系统。

拟采用独立 Go 核心：普通文件的 ReaderAt/流式处理、并发限制、进程监督及跨平台打包可以在同一语言内实现；MCP 使用官方 Go SDK，不自己拼一个“看似 JSON-RPC”的私有协议。Tunnel 先使用官方独立进程，不把控制面代码嵌入核心；这样能分别升级、重启和观察。

首个无网络算法内核可用当前可运行的 Go 1.23 工具链验证；这不是产品发行的工具链承诺。**在加入路径打开器和 MCP 服务前，切换到仍受支持、已固定校验的 Go 版本，且最低具备 Go 1.24 的 os.Root 能力；SDK 所需版本取更高者。** 不能为了适配当前执行环境而发布过期运行时或用 `EvalSymlinks + Open` 冒充抗竞争路径隔离。

### 2.2 组件图

```text
ChatGPT Web 原生自定义 App
        |
        | MCP / 支持的 OpenAI Tunnel 入口
        v
OpenAI Tunnel 服务 <--- 官方 tunnel-client（本机向外连接）
                                  |
                                  | 认证后的 loopback MCP
                                  v
                         ingress -> connection 身份
                                  |
                         profile/root/tool 权限检查
                                  |
                         少量高层 MCP 工具
                                  |
                  请求预算 + 公平调度 + 统一结果封装
                       /          |           \
              文件读取/搜索    Git/环境探查    可选元数据索引

本地管理入口（不向 MCP 模型开放）
  -> 配置、凭据引用、root/profile 管理、tunnel supervisor、健康状态
```

本机 MCP 绑定 loopback；公网 URL 不是 root 路径，tunnel ID 不是 API key，profile ID 不是认证。多 connection 共享核心，但每次调用从可信 ingress 得到身份，不能让模型通过 `profile=admin` 选择权限。

### 2.3 拟建目录及职责

```text
docs/MASTER_PLAN.zh-CN.md           本计划
docs/IMPLEMENTATION_STATUS.md      实际进度和证据
docs/COMPATIBILITY.md              真实账户/模型/协议兼容矩阵
docs/THREAT_MODEL.md               信任边界、攻击面、残余风险
docs/UPSTREAM_REVIEW.md            来源锁定、许可证、复用决定
docs/TOOL_CONTRACTS.md             工具与结果语义
docs/ACCEPTANCE.md                 验收命令、基准与人工任务
internal/readcore/                 可测试的范围读取/预算/批量内核
internal/rootfs/                   平台安全打开器、普通文件限定
internal/policy/                   connection/profile/root/tool 授权
internal/tools/                    高层语义接口
internal/mcpserver/                官方 SDK 与鉴权适配
internal/search/                   有界遍历、内容检索
internal/probe/                    固定探查动作、受限进程
internal/tunnel/                   官方客户端进程监督与状态
internal/config/                   配置校验、迁移、凭据引用
internal/catalog/                  后置、可移除的索引加速层
cmd/local-probe/                   后续产品 CLI/服务入口
cmd/readcore-demo/                 本轮仅本机显式文件演示，不是 MCP 服务
scripts/                          预检、验收、基准、打包脚本
configs/example.json               无秘密的配置示例
testdata/                         小型确定性夹具
```

不要为了目录图创建空文件。只在对应任务真正实现时增加目录。

### 2.4 工具面：先保持在十个左右

| 工具 | 用户意图与主要参数 | 关键约束 |
|---|---|---|
| `workspace_snapshot` | root_id、概览深度/预算 | 返回事实和可追踪路径；推断技术栈要带来源，不伪装成模型总结 |
| `list_directory` | root_id、相对目录、cursor | 可分页，明确未扫描范围；不返回无限树 |
| `find_files` | root_id、模式、范围、cursor | 输出路径候选；目录遍历与输出都受限 |
| `search_text` | root_id、query、globs、context、cursor | 默认 literal；regex 只用有复杂度边界的实现；返回行/字节引用 |
| `read_file` | root_id、path、byte/line range、expected_version | 单文件低成本接口；底层与 batch 共用一个 reader |
| `batch_read` | 文件/范围列表、输出预算 | 输入顺序、单项错误、总预算、公平分配、明确 continuation |
| `get_environment` | 明确的信息类别 | 不读取全部环境变量或凭据 |
| `run_probe` | action 枚举、受限参数 | 不是任意 command 字符串；绝不伪装 readOnlyHint |
| `git_status` | root_id | 受控 Git 进程，输出分区和预算 |
| `git_diff` | root_id、受限 paths、范围 | 禁用 external diff/textconv 等可执行扩展，部分结果说明 |

`list_roots` 可先并入 snapshot。工具数不是硬性宗教：只有真实任务证明组合困难才拆分。管理 tunnel、增加 root、放宽权限、读取 key 都不是模型工具。

### 2.5 统一结果契约

每个工具返回 `schema_version`、`request_id`、结果、coverage、warnings、budget、continuation。文件结果包括 root_id、相对 path、start/end byte、可用时的行号、version 及其 strength、content、状态和下一步。错误码应稳定：`invalid_request`、`denied`、`not_found`、`unsupported_type`、`unsupported_encoding`、`stale_version`、`budget_exhausted`、`deadline_exceeded`、`cancelled`、`unavailable`。

总预算区分：真正读取的 I/O 字节、返回文本字节、最终序列化的 JSON/MCP wire 字节、打开文件数、扫描目录项数、执行时长。中文不是一字节，字符数不是 token 数。对 token 只提供有标识的估计；不能用“字符数/4”冒充精确预算。

结果不能同时把大段正文复制进多个字段或多份 content。core 的文本预算不等于 MCP wire 预算，MCP 层必须在序列化后再做完整 envelope 限额。

### 2.6 版本、分页和一致性

单次 read 检查打开句柄前后版本；检测变化就不返回混合正文。首版 metadata token 是弱版本，只能报告 best-effort，不能检测恶意保持相同元数据的改写。需要强一致审查时，后续使用冻结 Git worktree/只读快照或在预算内保存不可变文件副本，不把全仓 hashing 放在每次读取前。

cursor 应绑定 connection、profile revision、root identity、文件/查询、版本/索引 generation、过滤条件、位置和过期时间；签名防篡改。权限撤销与配置变化使旧 cursor 失效。不要把大量底层信息要求模型自行维护，但必须保留简单 continuation 和过期/变化提示。

## 三、安全、权限和连接模型

### 3.1 身份与权限

配置中区分 Connection、AccessProfile、Root、CredentialRef。connection label 可写“个人账号 A”，但不保存 ChatGPT 密码/Cookie。一个 profile 可以被多 connection 引用；一个 connection 首版固定一个 profile。

每个 ingress 使用独立本地认证凭据或独立受控传输，认证完成后才允许 tools/list、tools/call 及资源读取。路由中的 profile 名和模型传来的 root_id 均不是授权。未认证请求、未知 connection 和被撤销 connection 一律拒绝；所有缓存、cursor、任务状态和日志都按同一权限边界隔离。

同一个 workspace 共用 app 时，不能仅靠 tunnel 知道每个调用者是谁。首版把它视为“连接级共享授权”；若要不同成员不同目录，必须增加经验证的 OAuth principal 映射或分开 connector/ingress，不能靠对话自报用户名。

### 3.2 文件系统边界

只接受 root_id + 规范相对路径，不允许模型传 OS 绝对路径。拒绝 `..`、NUL、Windows drive/UNC/device path、ADS、危险保留名称和无效 Unicode；平台层使用安全 root handle，不能只做字符串前缀检查。符号链接、junction/reparse point、挂载点、硬链接各自验证策略；`os.Root` 不是完整 OS sandbox，不能防御已有恶意本地管理员。

首版只支持普通本地文件；设备、pipe、socket、procfs/sysfs、网络共享和不受支持的特殊文件系统不开放。对秘密路径的显式 deny 高于 ignore override；搜索、snapshot、索引和命令输出也应用相同策略。未知编码返回明确错误，不静默改写源码内容。

### 3.3 命令不是按名称前缀判安全

`python --version` 也涉及解释器启动环境，Git 可以调用配置的外部程序。固定 action 映射到绝对可执行路径和固定参数模板；禁止 shell=true、任意 `-c`、管道、重定向、命令替换、用户自定 env/cwd。设置净化环境、固定工作目录、超时、输出限制及进程树终止。

V1 探查范围只包含审计过的版本查询、工具存在性、Git status/diff。进程命令行、端口、容器和数据库查询先作为显式扩展，避免泄漏秘密或越权。项目内测试/build/install 脚本属于可执行不可信代码，不因名字叫 test 就算只读探查。

### 3.4 凭据和管理入口

Windows 使用 Credential Manager/DPAPI 或受限 ACL 文件；其他平台使用 keychain/secret service 或明确权限的文件。配置只保存凭据引用。隧道 runtime key 与 admin key 分开；不持久保存 admin key，不自动创建/扩大组织授权。启动参数、日志、崩溃包和 PR 都不得包含秘密。

本地管理 HTTP 页面也必须有 loopback、Host/Origin 校验、CSRF 防护和认证，不能认为 localhost 天然可信。管理 API 不通过 tunnel 暴露。Support bundle 默认只导出版本、状态、错误码和脱敏配置，不导出源码正文。

### 3.5 传输与产品兼容性

首选官方 Secure MCP Tunnel，但核心和 MCP 不锁死在该传输。账户不具备 Tunnel 能力时，先报告能力缺口；经用户明确接受后才使用带认证的自管 HTTPS 入口。不得为了“跑通”自动公开本机服务。

官方指南将私有 Tunnel 与公开插件分发区分；首版目标是私有 developer-mode/custom app，不承诺可直接上架公开插件市场。

## 四、交付阶段与验收门槛

| 阶段 | 交付 | 可以声称什么 | 不可声称什么 |
|---|---|---|---|
| K0 内核原型 | 本计划、数据契约、可测试的有界读取/批量内核 | 算法部分已实现并在指定环境自测 | 已接通 Pro、已实现文件系统安全、已支持真实多账号 |
| A1 私有可用 alpha | 安全 root + MCP + 一条真实连接 + 搜索/批量读 | 指定账户/模型/OS 的实际探查链验证通过 | 所有订阅通用、Windows 之外均生产可用 |
| A2 多连接 alpha | 两个真实 connection/profile、隔离、故障恢复 | 已在记录的组合下验证并发和独立停用 | 自动逐用户 RBAC、任意恶意多租户隔离 |
| B1 可分发 beta | 大仓库效果对照、打包、预检、管理、文档 | 给指定支持矩阵用户试用 | 无需权限配置或未测试平台稳定 |
| V1 只读探查版 | 安全审查、稳定性和质量门槛全部通过 | 读取/探查产品完成 | 写文件、完整 Agent 功能已完成 |
| V1.1 可选写入 | 独立写权限、预览、版本校验、审计 | 在显式启用条件下进行受控修改 | 自动全盘写、默认自主执行命令 |

### 4.1 初始预算：设计目标，不是已测事实

先以每 batch 32 项、4 个 worker、正文总预算 128 KiB、单项最大 32 KiB、总 I/O 8 MiB、默认 deadline 10 秒作为可调起点。MCP 整体 wire 上限单独设定，例如 256 KiB，并预留结构开销；这些值必须通过目标客户端实测后冻结。

每个 connection 设置并发上限与队列；全局设置文件句柄、内存和进程上限。一个高负载账号不得持续饿死另一个账号。内部 streaming 不意味着客户端会持续收到无限 SSE；每次工具调用仍提供有界结果和显式续取。

### 4.2 质量测试比“能调用”更重要

建立有标准答案的项目夹具，包含正常文本、相似文件名、误导 README、嵌套模块、忽略目录、明确 deny 的假秘密文件、长行、无换行、中文/emoji、并发改写和大量无关文件。

用同一模型、同一预算和相同任务比较：基础逐文件工具；snapshot/search/batch 工具；开启和关闭索引。记录正确证据覆盖率、错误结论数、遗漏率、工具调用数、重试率、返回字节、端到端时间、人工介入次数、内存和 I/O。文件 I/O 的吞吐和模型理解效果分开报告。

初始验收目标：标准答案中的必要证据全部可定位；硬预算零超限；未授权读取零成功；在固定 20 个目标文件的 scripted 场景中，批量方案在相同正文预算下至少减少一半工具请求；开放式调查任务不得为减少调用数而降低证据正确率。真实 Pro 任务每组至少五次，新对话分组记录；不要将调试过的成功一次当作稳定性证明。

## 五、当前关键决策及替代路径

D1：独立小核心而不是三项目融合。原因是功能范围、许可证与进程模型不一致；后续性能数据证明某模块值得直接复用时，可在保留许可的前提下单独移植。

D2：先打通端到端，再追求百万文件索引。100k/1m 作为测试档位，而不是首个里程碑就实现复杂持久索引。若有界直接扫描足够快，不为“功能丰富”加入索引。

D3：本轮亲自实现最容易被低估的数据内核：预算、公平批量、范围读取、错误/版本/继续语义及测试。MCP、真实身份和安全 root adapter 没完成前，不提供对外服务。

D4：CLI 优先、管理 UI 后置。多 tunnel 配置持久化和状态是需求，Electron/托盘不是已确认必选方案。

D5：不硬编码套餐/模型名。兼容性由真实能力矩阵表达，读写工具由本地策略与客户端能力共同决定。

## 六、逐步骤实施程序

### P00. 前提准备与来源锁定

**输入：** 本计划、空目标仓库、已连接 GitHub。**依赖：** 无。

1. 将本计划作为第一个提交写入 `main`，确认能重新读取且内容完整。之后从这个提交创建 `feat/readcore-foundation`；所有代码提交在该分支，最终 PR 指向 main。
2. 在开发机执行 `git clone https://github.com/LE-saber/Local-Probe.git`，用 `git status --short` 确认工作区状态；不覆盖用户已有修改。使用 GitHub Connector 写入时，仍记录每次返回的 commit SHA。
3. 在目标仓库外创建 `_references`。只读下载以下固定版本供调查，不运行安装脚本、不把上游完整代码推入 Local-Probe：
   - LCA `95144e610ddcc3bb5a879117803907c008b1a88e`：`README.md`、`LICENSE`、`SECURITY.md`、`server/README.md`、`server/package.json`、`server/package-lock.json`、`skills/repo-support/SKILL.md`。需要定位实现时再稀疏检出 `server/`，用 `git grep -n -e workspace_snapshot -e read_many -e search_text` 找准确函数，不凭旧对话猜文件路径。
   - ChatCMD `ad299fc6058d136cd6839ba14eb1e8de5122722c`：`LICENSE`、`Cargo.toml`、`docs/mcp_method.md`、`docs/tool-resource-budgets.md`、`docs/adr/0020-repository-index.md`、`crates/chatcmd-mcp/src/tool_catalog.rs`；需要实现细节时检出 `crates/` 并定位 `fs_read_text_v2`、`fs_batch_read` 的定义和测试。
   - Codex Free `cb487c744e2220909548ec112d3fe2a5e26cb2f1`：`README.md`、`Cargo.toml`、`docs/ARCHITECTURE.md`；通过 `git ls-tree -r --name-only HEAD` 查明许可证与 tunnel 实现路径后，读取这些文件。许可证未审查前不复制代码。
   - OpenAI tunnel-client `9f77746a5498289f04e1ae6d3e0c830f3871af52`：`README.md`、`LICENSE`、`docs/onboarding.md`、`docs/permissions.md`、`docs/configuration.md`、`docs/connectors.md`、`docs/troubleshooting.md`。这是调查快照，不自动当作发行二进制版本。
   - 官方 MCP Go SDK：读取 `README.md`、`go.mod`、许可证和 Streamable HTTP 示例；选定受维护的 release 后将确切 tag/commit 写入依赖锁定记录，不使用漂移的 `@latest` 构建发行版。
4. 下载示例（PowerShell，换入上表仓库、SHA、路径）：

```powershell
New-Item -ItemType Directory -Force ..\_references | Out-Null
git clone --filter=blob:none --no-checkout https://github.com/int04/ChatCmd.git ..\_references\chatcmd
git -C ..\_references\chatcmd sparse-checkout init --no-cone
git -C ..\_references\chatcmd sparse-checkout set LICENSE Cargo.toml docs/mcp_method.md docs/tool-resource-budgets.md docs/adr/0020-repository-index.md crates/chatcmd-mcp/src/tool_catalog.rs
git -C ..\_references\chatcmd checkout --detach ad299fc6058d136cd6839ba14eb1e8de5122722c
git -C ..\_references\chatcmd rev-parse HEAD
```

5. 在 `docs/UPSTREAM_REVIEW.md` 记录 URL、commit、具体已读文件、许可证、哪些只是文档陈述、哪些亲自测试。源码候选下载失败时记录失败；不能补写猜测的内容。
6. 为项目新增 `.gitignore`：忽略本地配置、credentials、runtime logs、参考 checkout、临时夹具、bin、coverage。公开许可证暂不代替所有者选择，文档明确未授权复用上游代码。

**输出/验收：** 计划提交在前，代码提交在后；来源 SHA 可核对；无秘密/上游大仓库进入提交。**停止条件：** 无写权限时生成 patch/package，不谎称已 push。

### P01. 外部能力预检：尽早排除接入不可行

**依赖：** P00；可与 P02/P03 离线工作并行。不能成为所有开发工作的无限等待点。

1. 在 `docs/COMPATIBILITY.md` 建表：测试日期、账户匿名标签、个人/组织 workspace、套餐自报值、网页模型实际标签、Developer Mode 可见性、App 创建权限、工具发现、只读调用、Tunnel Read/Use、workspace association、结果证据。
2. 用户在目标账户检查官方 Developer Mode/App 入口，至少确认一条只读工具调用路径。不要收集密码、Cookie、整份账单或完整 API key。
3. 分开检查 Platform Tunnels 页面能否查看/创建/使用，runtime key 是否有正确权限；记录实际组织/workspace 关联，不猜 ID。
4. 先用官方 MCP stub/SDK 示例完成 initialize→tools/list→echo 调用。使用官方 `tunnel-client help quickstart` 获取当前参数，再执行 profile 的 doctor/run。实例成功后记录客户端版本和脱敏日志。
5. 在目标 Pro 模型的正常 Web 对话调用一次只读工具；与另一可用模型对照。把“App 能添加”“模型能发现工具”“模型能调用工具”分成三个结果。
6. 真实第二账户重复一次；没有第二账户时标记 pending，不把两个模拟 connection 当作两个账号测试。
7. 原生 Tunnel 不可用时，记录具体阻塞类别：套餐、workspace policy、Platform role、association、网络、客户端兼容。仅经用户授权再试有认证的 HTTPS 替代；不能退回 DOM 自动化或冒充通过。

**输出/验收：** 至少一个真实只读调用的证据才能给 A1 放行。**阻塞时：** 继续数据内核/安全测试，但禁止宣称已接通 Web Pro。

### P02. 契约、威胁模型与首个内核骨架

**依赖：** P00。

1. 建立 `go.mod` 和 `internal/readcore/`，核心只依赖标准库；不在这个阶段写网络 listener、通用 shell 或任意路径 opener。
2. 在 `docs/TOOL_CONTRACTS.md` 写清楚 byte offset 是 0-based，line 是 1-based；UTF-8 裁切不能截断字符；页预算过小必须报错/返回明确未进展，不得无限返回同一 cursor。
3. 定义受信任调用上下文与模型 Request 分离：connection/profile/root 权限由调用方提供，不放入可自行提权的模型参数。
4. 定义 Source/Handle 接口，使内核仅处理已授权的打开结果。Source 负责安全路径解析、普通文件限制和版本来源；测试 fake Source 不等于生产 rootfs。
5. 定义预算、Result、ItemError、部分失败、取消和版本变化语义；写内核与未来 wire envelope 的边界。
6. 在 `docs/THREAT_MODEL.md` 写至少五条攻击路径：模型提示注入外泄；跨 profile；symlink/reparse race；命令参数/启动环境执行；localhost 管理入口攻击。逐条映射防护和未覆盖点。

**输出/验收：** 输入/结果/安全边界能被另一个执行者理解；无伪 MCP；核心测试可离线运行。

### P03. 亲自实现有界读取与确定性批量内核

**依赖：** P02。**本轮优先直接实现。**

1. 在 `internal/readcore/` 实现受限 byte-range read：先检查参数和权限，再打开；仅读取页需要的范围，不用 ReadFile/ReadAll 载入整文件。
2. 限制单项、batch 总正文、总 I/O、项数和 worker 数。采用执行前确定的公平份额，结果不因 goroutine 完成顺序不同而变化；短文件剩余预算可先不回收，明确这个简单策略的代价，后续用基准决定是否改为二轮分配。
3. UTF-8 末尾不足一个字符时向前裁切且返回正确 next byte；页首落在字符中间或内容非法时返回类型化错误，不能替换乱码冒充原文。
4. 对 expected_version 做打开前比对，对读取前后版本做变化检测；变化时清空正文。报告 version strength，弱元数据版本不包装为 snapshot。
5. 保持输入顺序；每项独立错误；取消后停止新 I/O；每个成功打开的 handle 都关闭。按调用上下文拒绝越权 root，拒绝非规范 path。
6. 给出 `cmd/readcore-demo`，仅允许本机操作者显式传入文件，打开后作为预授权 handle 演示分页；不监听端口、不识别 tunnel、不供模型远程调用。
7. 写单元测试：32 个请求有界并发、公平预算、单项丢失、不足 UTF-8 页、非法路径、未授权 root 不触达 Source、版本变化不返回正文、取消、十 GB 合成 ReaderAt 只读取少量字节。
8. 运行 `go test ./...`、`go test -race ./...`、`go vet ./...`，为 path 和分页添加 fuzz target；记录实际命令/版本/结果。对 demo 在真实临时文件上做一次调用。
9. 将代码、测试和状态说明提交在实现分支，开 PR，不合并。把未实现的生产 Source/MCP/Tunnel 明确写入 README。

**输出/验收：** 内核真正可运行、自测，内存/I/O 不随合成文件总长度增长；没有对外开放不安全的原型。失败测试先修复，不给“release-ready”标签。

### P04. 安全文件打开器和 profile 实现

**依赖：** P02/P03；先固定受支持 Go/SDK 工具链。

1. 在 `internal/config/` 定义 schema_version、roots、profiles、connections、credentials refs；未知字段/重复 ID/不存在引用失败，配置切换原子化。
2. 在 `internal/policy/` 建只读默认 profile、显式 deny 和 root/tool allowlist；生成不可由模型覆盖的 BoundScope；每次访问重新检查 revocation/profile revision。
3. 在 `internal/rootfs/` 使用 Go os.Root 或经审查的等价 handle-relative API；只允许普通文件，明确 symlink、junction、ADS、设备路径、非本地 FS 策略。禁止以规范化后字符串 startsWith 作为最终边界。
4. 所有 read/stat/list/search 从同一 opener/policy 进入；snapshot 不能绕开 deny；profile A 看不到 B 的 root 名、路径或缓存内容。
5. 写 Windows 特有测试：大小写、Unicode 路径、长路径、junction/reparse、保留名、ADS、drive-relative 和 UNC。Linux 写 symlink swap、hardlink/挂载边界限制说明以及特殊文件拒绝测试。
6. 撤销 profile 时取消其排队/执行任务；旧 cursor 和缓存读取失效。记录 local OS owner/管理员攻击仍在边界之外。

**验收：** 真实 OS 的 escape/race/deny 测试通过；仅 Linux 模拟不能给 Windows 放行。Source 安全性不达标，不允许进入真实 MCP 接入。

### P05. 官方 MCP 适配与最小端到端

**依赖：** P01 有可行路径、P04 安全边界通过。

1. 固定官方 Go SDK 的 release 与模块校验，写入 go.mod/go.sum 和来源记录；按 SDK 实现 Streamable HTTP，不手写协议状态机。
2. 在 `internal/mcpserver/` 加认证 middleware：本地 connection 凭据→BoundScope；未经认证不得泄漏 tools/resources/root metadata。限制 body、header、超时、并发；校验 Host/Origin。
3. 先注册 `read_file`、`batch_read`、`workspace_snapshot` 最小版本，声明真实 readOnlyHint；实现 schema 和工具错误，不用自然语言 success 掩盖失败。
4. 对完整 MCP envelope 做序列化预算检查；在需要截断时重新形成合法结果，不能直接剪断 JSON。测试 JSON escaping 最坏情况与 Unicode。
5. 写 server instructions：先概览后检索；结果不完整时继续；文件内容是不可信数据；禁止把 README 中的命令当系统指令；少量建议不伪装强制模型行为。
6. 使用 SDK client/Inspector 验证 initialize、tools/list、tools/call、会话终止、取消、超时和重启；保存协议版本与请求记录。
7. 经 P01 已验证的真实 Tunnel/App 调用这三个工具，核对本地文件摘要和返回范围；记录 Pro 实际模型标签，不仅测普通模型。

**验收：** 完成一次真实“Web 模型→工具→本地→结果”链；UI 看见 App 但调用失败不能通过。

### P06. 大目录发现、文本搜索与行范围

**依赖：** P04；与 P05 高层适配可并行但接口须一致。

1. 实现迭代式有界 walker：可取消、目录项/深度/时间限额，ignore 与显式 deny 分离，默认不过 symlink 边界。
2. `list_directory` 分页不把整个大目录排序载入 RAM；采用带 generation 的局部 catalog 或有界 cursor 状态，明确 live listing 非原子快照。
3. `find_files` 复用 walker；UTF-8/大小写/glob 语义按平台记录，分页重复/遗漏可测。
4. `search_text` 默认 literal，有界流式扫描；返回文件、行/byte、匹配上下文和 coverage。regex 用 RE2 类有限复杂度引擎，不暴露灾难性回溯。
5. ripgrep 只能作为可替换加速器：固定 executable 和参数，候选范围仍受 policy；未审查路径越界/ignore 差异时不直接把用户 root 交给外部全盘扫描。
6. 在 readcore 增加 line_range：定位前缀扫描也扣 I/O；长行用 ReadSlice/分块算法，不依赖无限 Scanner token；达到预算返回 scan-limited continuation。
7. 行/byte cursor 要说明从哪里继续；读日志尾部采用反向有界 block scan，不能为了最后 100 行先读 10 GB。
8. 生成 10k/100k 路径、巨型长行及变化目录夹具；统计打开数、扫描数、返回数和内存。每个被跳过的类别可见。

**验收：** 大目录不会一次无限输出；搜索结果可继续；编码/忽略/deny 语义一致；不是只靠 mock pass。

### P07. 高层概览和模型使用效率

**依赖：** P05/P06。

1. 在 `internal/tools/snapshot.go` 汇总 authorized roots、目录轮廓、manifest 文件候选、Git 摘要、预算/coverage；不自动执行项目脚本。
2. 为技术栈/入口候选附证据路径；存在多个 manifest 时列候选而不是武断选一个；目录不完整必须标记。
3. 在 `docs/TOOL_CONTRACTS.md` 为每个工具写 Use when / Do not use / 一次完整参数示例 / 返回结果和继续范例。
4. 设计三种用户调查任务：架构概览、具体 bug 路径、跨模块影响检查。给出标准答案及允许的证据，不把建议“4～8 次”当保证。
5. 在固定预算下对比基础 read_file 与 snapshot→search→batch；记录真实 tool calls、工具选择错误、重复读与遗漏。
6. 仅在数据支持时合并/拆分工具、提高 batch 项数或添加 repo map；修改工具 schema 后处理 App refresh/republish 兼容性。

**验收：** 批量工具减少往返且不牺牲必要证据；失败的开放式任务同样记录，不挑选最好案例。

### P08. 安全环境和 Git 探查

**依赖：** P04/P05。

1. `get_environment` 仅暴露 OS/architecture/允许的工具类别与能力；不传用户名、home 下秘密路径或整份 env。
2. `run_probe` 实现 action enum：tool_exists、tool_version 等；配置确定 executable 绝对路径，禁止 PATH 被项目目录劫持。
3. 为 Python/Node/Git 分别研究启动参数和继承环境；对不确定能否保持只读的动作暂不暴露。
4. Git 操作设置受控环境和禁用外部 diff/textconv/pager 的参数；限制 revision/path 输入，显式 `--` 分隔路径；不启用 hooks，不自动修改 safe.directory 全局配置。
5. 统一 stdout/stderr 字节上限、timeout、取消和进程树终止；不能只 kill parent 后留下子进程。
6. 写恶意项目测试：假同名 executable、Git external diff、环境注入、过量输出、永不退出子进程、参数以 `-` 起始、路径空格/中文。

**验收：** 未批准的 action 不能执行；每种固定 action 都有测试和副作用说明。只读 hint 是描述，不是安全实现。

### P09. 多 connection/tunnel 保存和隔离

**依赖：** P04/P05；真实第二账号依赖 P01。

1. 配置中持久保存 connection id/label、transport、tunnel id/alias、credential ref、profile id、enabled；不把套餐名用作授权判断。
2. 首选逐 connection 独立 loopback ingress 凭据；共享端口时在可信认证层映射，而不是相信 URL path 或客户端自报身份。
3. 为 profile revision、cursor、cache、active requests 和日志添加 connection 维度。相同 root 共享物理缓存时，读取前仍重新授权，不能泄漏命中信息。
4. 支持 add/list/enable/disable/remove CLI，删除只删指定配置/自有进程，不删用户项目或共享凭据。
5. 实现每连接配额和全局配额；至少两个 connection 并发读互不阻塞，撤销 A 立即拒绝 A 新请求，B 仍工作。
6. 分别做模拟 client 隔离测试与两个真实 ChatGPT 账号测试；记录两类证据，不混淆。
7. 跨 workspace 共用 tunnel 只有在官方 association/权限实测通过且用户接受共享授权时启用；默认给不同权限边界不同 ingress。

**验收：** 第二账号能读取许可范围、不能访问另一个范围；重启后配置仍在；单连接停用不影响其他连接。

### P10. 官方 Tunnel supervisor 与故障恢复

**依赖：** P01/P09。

1. 让用户选择官方发行客户端路径；通过官方来源下载时验证 release、SHA256 和许可证，记录 binary hash；不执行不明 URL 返回的程序，不默认分发未审查的私有 runtime。
2. 先探测 `--version` 和 help 支持的接口，写版本适配；不把旧对话的命令参数硬编码为永远有效。
3. 每 connection 单独管理 profile/state directory、健康端口、PID/进程所有权和日志。优先复用官方 runtimes 功能；不足时仅包装官方 run/profile，绝不重写 poll/response 协议。
4. 状态机区分 stopped、starting、local_mcp_ready、polling、ready、degraded、auth_failed、backoff。只看进程存在或 `/healthz` 200 不等于远程可调用。
5. 实现指数退避+jitter、上限、人工停止、凭据轮换后的重新加载；认证失败不无限高频重试。
6. 关闭仅终止本程序拥有的进程，检查 PID reuse；Windows 用受控进程组/Job Object，保留不支持行为的说明。
7. 运行断网、睡眠/唤醒、凭据撤销、端口冲突、core 崩溃、tunnel 崩溃和重启测试；只读操作允许客户端重试，但不能伪装 exactly-once。
8. 为用户提供脱敏 doctor 报告，分清 Platform permission、ChatGPT App、Tunnel、local MCP、rootfs 五层错误。

**验收：** 两个 tunnel 可独立保存/启动/停止；单故障不会重置所有连接；无 key 出现在日志/参数/诊断包。

### P11. 元数据索引：有证据才启用

**依赖：** P06/P07 基准已有结果；不阻塞第一条真实连接。

1. 先记录直接扫描在 10k、100k、1m 路径上的 cold/warm 数据；分开 SSD/HDD、Win/Linux，不能混用上游机器数字。
2. 只有重复查询明显成为瓶颈时，引入 `internal/catalog/`；首版仅 path/metadata，不做 embedding 或云端全文上传。
3. 选 SQLite 时固定 driver、schema、事务和迁移；配置磁盘/WAL/条目数上限，记录依赖许可；若简单有界内存索引已足够，不强制数据库。
4. generation、ignore fingerprint、profile/root identity、startup-stale、reconcile、corruption fallback 全部明确；watcher 只作提示，不能证明没有丢事件。
5. 索引能命中但不能证明“没有新文件”。要求完整调查时做 live reconciliation 或标记 coverage 未验证；不能仅验证已有候选就声称全量完整。
6. 分页使用稳定查询键和 generation；旧 cursor 在变化后要失效或基于快照继续，不能混用。
7. 与 direct scan 对照：延迟、RSS、磁盘、重建时间、未发现新文件、遗漏/重复。索引没有收益则默认关闭/撤回。

**验收：** 正确性不劣于 direct；指定负载确有收益；索引损坏不导致 read_file/batch_read 停工。

### P12. 产品预检、轻量管理与打包

**依赖：** A2 和基本故障测试通过。

1. `local-probe doctor` 检查 OS、root、权限、客户端版本、profile refs、端口、可达性和能力缺口，输出机器可读 JSON 与简短人类提示。
2. `local-probe init` 只创建自身配置，不写入用户仓库、不自动扩权；选择 roots 时显示会传给 ChatGPT 的数据类别。
3. CLI 全部工作后再加 loopback 管理页面：roots/profiles/connections/status/logs；不做聊天页面/IDE；管理授权与模型工具完全分离。
4. Windows 先提供可复现的便携包，之后再决定 tray/installer/autostart；不把开机启动设为静默默认。macOS/Linux 的包标注真实测试程度。
5. 固定构建工具链、SDK/依赖、tunnel 兼容范围，生成 checksums/SBOM/第三方说明；不在包里放任何真实配置或 key。
6. 卸载仅移除自有程序/可选缓存；默认保留用户配置并说明路径；永不删除被授权 root 的文件。

**验收：** 干净 Windows 用户环境安装、接入、停止和卸载流程可复现；下载后不能启动的包不得称稳定版。

### P13. 验收、对抗测试与独立审查

**依赖：** P05–P12 对应模块已实现。

1. 建 `scripts/acceptance.ps1` 和可在 CI 运行的测试命令；生成夹具而不是提交大仓库或真实秘密。
2. 固定硬性失败项：越权、静默截断、预算失控、命令绕过、凭据泄漏、错误 scope 复用、假称内容完整；任意发生即停止发布。
3. 执行第 4.2 节任务与 baseline 对照；对真实 Pro 的每次任务保存匿名证据清单和指标，不保存敏感对话全文。
4. 运行两个 profile/至少两条 connection 并发、故障恢复和 soak；初始目标为一小时持续只读混合负载，再根据结果提高时长；完成此目标才填写通过。
5. 独立审查者只获得需求、威胁模型、commit、构建步骤和已知风险，避免把实现者结论当答案。专项检查 Windows 路径、MCP auth、跨连接 cursor、子进程和断网。
6. 把失败、跳过、环境阻塞和残余风险一并写入 `docs/IMPLEMENTATION_STATUS.md`；生产安全和 Windows 行为必须有对应环境证据。

**验收：** 已实现、自测、独立审核、release-ready 四种状态分开；只有门槛齐全才发布。

### P14. 写文件扩展，独立于只读版发布

**依赖：** V1 只读版验收；用户明确启用 write profile。

1. 先增加 diff preview，不写磁盘；需要修改的目标通过同一 root/policy 检查。
2. 实现 expected strong version、临时文件写入、fsync/原子替换策略、失败恢复及审计；明确不同 FS 的原子性边界。
3. `apply_patch`、`write_file` 使用 write 工具标识，默认需要确认；不靠 readOnlyHint 绕过账户政策。
4. 并发修改时拒绝 stale patch；一次多文件修改不假称全局事务，提供明确部分失败或事前备份/恢复机制。
5. 单独测 symlink swap、权限变化、磁盘满、杀进程、编码/换行与 Git diff；通过后才公开 write profile。

**验收：** 只读 connection 绝不能调用写工具；关闭写扩展不影响读取功能。

## 七、依赖图、任务分工和合并规则

```text
P00 -> P02 -> P03 -> P04 -> P05 -> P07
  |                    |       ^
  +-> P01 -------------+       |
                       +-> P06 -+
                       +-> P08
                P05 + P04 -> P09 -> P10
                P06 + P07 -> P11（按证据可跳过）
                A2 -> P12 -> P13 -> V1
                                  -> P14（独立授权）
```

Pro 本轮完成总体设计及 P03 中的数据核心，审查预算/编码/一致性等高返工风险决策。后续执行任务包：

- **执行包 A：P04/P05。** 输入本计划、readcore 契约；只做安全 root adapter 和官方 MCP，不加索引/UI/shell。必须带 Windows 与 MCP auth 测试。出口为可评审 PR 和真实调用待验记录。
- **执行包 B：P06/P07。** 在 A 的接口稳定后做搜索/概览/行范围和质量夹具；不得私自加自动执行项目脚本。出口为 baseline 对照报告。
- **执行包 C：P08。** 独立实现有限探查器；固定 action、净化环境、进程树测试；不接收任意 shell 文本。
- **执行包 D：P09/P10。** profile/connection/tunnel 生命周期与隔离；不得把 Admin key 写进配置或让模型管理授权。
- **执行包 E：P11/P12/P13。** 性能证据后决定索引；产品化及独立审查。需要新的高成本架构选择或安全反例时再回 Pro，不为格式化反复切模型。

当前环境没有已确认可调用的独立 Codex 执行器，不假称已经派发。任务包是下一执行者可直接领取的工作，而不是后台任务。所有包必须注明允许修改的路径、依赖 commit、测试命令、未通过项和返工点。

## 八、风险台账和停止规则

| 风险 | 早期检查 | 处理/停止规则 |
|---|---|---|
| 目标 Pro 无法调用自定义工具 | P01 真实只读调用 | 阻止 A1 发布，不阻止离线内核工作 |
| Tunnel 权限/association 不齐 | P01 分层预检 | 由所有者授权，不伪造、不用公开无认证端点绕过 |
| Windows 路径隔离未验证 | P04 平台测试 | 不能发布 Windows 可远程访问版 |
| 上游 AGPL/其他许可影响 | P00 来源审查 | 不复制未批准代码；许可变更另行决定 |
| 模型少调用但漏关键证据 | P07/P13 质量对照 | 撤回工具简化，不用 call 数掩盖质量下降 |
| index 并未加速且可能漏新文件 | P11 对照/coverage | 保留 direct，默认关闭 index |
| 多 profile 共用缓存/游标越权 | P09 对抗测试 | 零容忍，阻止合并与发布 |
| Git/解释器启动执行了项目代码 | P08 恶意项目 | 禁用该 probe，修复再测 |
| 凭据在日志/命令参数出现 | P10/P12 检查 | 删除暴露面；是否轮换由用户授权，不自行处理账户 |
| 巨型文件/阻塞 FS 导致不可取消 | P03/P04/P06 | 普通本地文件限定、预算、必要时隔离 worker；不承诺强制取消任意 OS I/O |

## 九、本轮实际执行范围与完成条件

本轮的第一交付物是本计划本身，包含从取材到发布/写扩展的完整步骤。计划保存成功后，优先完成：来源/兼容性纠偏记录；P02 的核心契约；P03 的可运行内核、单元/竞争测试及本地文件 demo；实现分支与 PR；实际进度报告。

不在缺少真实账号交互、runtime credentials、Windows 机器或外网构建能力时假称完成 P01、P04 的平台验证、P05 的真实 MCP/Tunnel 连通和 A2 多账号测试。不得向用户索要账号密码/Cookie。必要的后续输入仅是目标 OS/目标账户能力预检、授权 roots 和产品许可证等最少决策。

对本轮所有假设记录原因、后果和可替代方案。核心可运行不代表产品可安装；代码量不是完成度。提交顺序和验证记录是本轮验收依据。

## 十、来源与复核入口

以下是本计划的调查来源；源码/文档声明与本项目实测必须分开。检索日期：2026-09-07。

- 编排规则：`https://github.com/LE-saber/deep-project-orchestrator`，尤其 `skill/deep-project-orchestrator/SKILL.md` 及 software-projects/verification references。
- OpenAI Developer Mode：`https://developers.openai.com/api/docs/guides/developer-mode`。
- OpenAI Help Center：`https://help.openai.com/en/articles/12584461-developer-mode-and-mcp-apps-in-chatgpt-beta`。与上一来源存在政策口径差异，须实测，不作绝对套餐承诺。
- OpenAI Tunnel：`https://developers.openai.com/api/docs/guides/secure-mcp-tunnels`。其文档明确区分 Platform tunnel permissions 与 ChatGPT developer-mode access，并说明私有连接不等于公开插件分发。
- 官方客户端：`https://github.com/openai/tunnel-client`；调查 commit 见 P00。
- 官方 MCP Go SDK：`https://github.com/modelcontextprotocol/go-sdk`。
- Go 路径边界：`https://go.dev/blog/osroot`。`EvalSymlinks` 后再打开存在 TOCTOU 风险，os.Root 也不等于完整 sandbox。
- LCA：`https://github.com/LongNgn204/local-coding-agent`；调查 commit 见 P00；README 标注 AGPL-3.0，repo-support 工作流给出概览→检索→窄范围读取。
- ChatCMD：`https://github.com/int04/ChatCmd`；调查 commit 见 P00；MIT LICENSE；`docs/adr/0020-repository-index.md` 同时记录索引设计与百万文件负面性能结果，未由本轮独立复现。
- Codex Free：`https://github.com/hypnguyen1209/codex-free`；调查 commit 见 P00；其 README 描述 loopback token、官方 tunnel runtime 和监督机制，不能代替本项目端到端证据。
