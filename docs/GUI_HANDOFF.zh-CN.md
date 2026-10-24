# Local-Probe GUI 开发交接书

日期：2026-10-24。对象：接手 GUI 的设计/前端/桌面工程人员。
项目云端地址：https://github.com/LE-saber/Local-Probe

仓库默认交接入口为[英文版](GUI_HANDOFF.md)，本文保留完整中文需求。
## 0. 接手结论与源码版本

项目已有可用的 Windows GUI 和经过本机测试的新操作后端。此次工作是沿用已认可的页面风格，补齐双用途连接、设备配对、开发者授权、审批与作业的真实 GUI 接入，不是重新制作一套脱离宿主的展示站。

**后端尚未全部封板。** 操作引擎可运行，但桌面桥接、完整多连接宿主、在线规则和选择性授权版本还有缺口。不能通过前端模拟状态或删除权限门来补齐这些缺口。GUI 负责人负责交互、页面及本地桥接适配；涉及权限、运行宿主与撤权机制的新增契约，应与后端负责人协作落实。

当前 R12 源码交付分支为 `feat/r12-dual-mode`，最初开发基线为 `dd0a6b73981d8006403913e400d698c3b8fee291`，本次整合了 main 的四语言 README 更新。请直接克隆该分支，记录实际 `git rev-parse HEAD`；仅克隆旧 main/最初基线拿不到 R12。此前“尚未提交、需源码快照”的提示属于打包前状态，现以[仓库开发指南](R12_DEVELOPMENT.md)为准；不要 reset/clean 覆盖现有工作。

本文路径均相对仓库根目录。传递源码时应排除 `.tools/`、`.runtime/`、`.secrets/`、运行配置、令牌、私钥、SSH 主机信息、备份和日志；不要直接打包整个本机工作目录。本文不包含真实服务器地址或凭据，也不授权发布、推送、修改系统策略或操作用户真实连接。

## 1. 产品目标与本次范围

Local-Probe 让 AI 通过受控连接访问用户授权的电脑文件，并在用户明确授权时执行开发任务。

| 用途 | 面向谁 | 用户要完成的事情 |
|---|---|---|
| Web 访问本地 | Web AI 的标准 MCP 客户端 | 连接本机服务，访问获准文件；当前 GUI 主要覆盖此路径 |
| Agent 远程操作 | 另一执行环境中的原生 Agent | 运行轻量 stdio 桥接，经 Tunnel 或自建中转连接目标；配对、授权、请求本地批准并操作 |

Agent 已能在自己的当前环境中合法直接操作同一工作区时，优先使用原生工具。不能仅凭客户端叫 Codex、路径相同或 IP 相同就认定同机，也不能借本项目绕过原生沙箱/审批。

首个 GUI/操作目标是 **Windows + WebView2**。Linux 已有 Agent/配对程序构建，用户另有一台 Linux 电脑供后续跨机测试；Linux 操作目标目前不支持执行/写入，此次不要求移植 Linux GUI。

本项目不是 IDE、AI 推理服务、远程桌面或任意端口代理。GUI 不应扩展成完整代码编辑器或通用终端。

## 2. 现状：哪些可复用，哪些还没接入

| 模块 | 实际状态 | GUI 工作 |
|---|---|---|
| 现有五页 GUI | 已有概览、连接、访问范围、活动、设置；真实桌面桥接 | 保留布局和已修复行为，增量扩展 |
| 旧连接管理 | 保存/选择历史连接、连接/重连/断开已接；桌面 Service 仍是单 controller | 增加用途和远程参数；完整多实例需后端控制面 |
| 文件授权/规则 | 根目录选择、增删/暂停、deny/ignore 读取/预览/离线提交已实现 | 保留树/列表；不得宣传为全局在线即时撤权 |
| 开发者旧设置 | 保存模式与旧命令模板；`execution_available` 仍关闭、门码为 `network_enforcement_required` | 不能把它直接当成新受信任操作界面 |
| 新设备配对 | 持久化身份、公开 bundle/CSR、指纹、签发、撤销及租约已实现 | 缺少 GUI 与可信本地管理适配 |
| 新操作引擎 | 命令/版本/脚本/build/test、文本写入/补丁、本地审批、作业账本、取消已运行 | 缺 GUI 宿主接线、审批队列与作业视图 |
| 双连接作业 | 两连接可并行各一个任务，取消一方不影响另一方 | 不等于 GUI 已有多个独立运行连接 |
| 自建 Relay | Linux 测试部署已完成，真实公网只读链路通过 | GUI 尚不能完整配置/管理；新操作链路未做真实中转验收 |
| 备份/诊断/主题/DPI | 已有原生对话框、备份校验、脱敏导出及主题/DPI修复 | 属于必须保持的回归基线 |

截至交接：完整 Go test/race/vet、本机真实运行器/构建测试已通过；本次交接又运行 `node scripts/Test-LocalProbeDesktopFrontend.mjs`，24 个 VM 场景通过。VM 测试不等于像素、原生菜单或跨屏 DPI 实测。

## 3. 必读代码与文件地图

| 路径 | 作用 |
|---|---|
| `AGENTS.md` | 仓库约束、验证要求及权限边界 |
| `internal/desktopweb/assets/index.html` | 正式五页结构、弹窗、控件 |
| `internal/desktopweb/assets/app.css` | 已认可的视觉基线与响应式样式 |
| `internal/desktopweb/assets/app.js` | 页面状态、渲染、RPC、轮询、主题同步 |
| `internal/desktopweb/assets/icons.js` | 现有 SVG 图标体系 |
| `internal/desktopweb/desktopweb.go` | 内嵌资源、固定本地 origin |
| `internal/desktopbridge/service.go` | RPC envelope、dispatch、snapshot、单 controller 生命周期 |
| `internal/desktopbridge/desktop.go` | 历史连接和桌面状态投影、配置更新 |
| `internal/desktopbridge/developer.go` | 旧开发者配置与执行门，不能移除门以假装新后端可用 |
| `internal/desktopbridge/rules.go` | 现有离线规则管理、预览、CAS 与共享影响确认 |
| `internal/desktopbridge/backup.go` | `.lpbackup`、校验和、恢复候选与安全门 |
| `internal/desktophost/host_windows.go` | Win32/WebView2、消息桥接、原生对话框、托盘 |
| `internal/desktophost/theme_windows.go` | 原生窗口框架和 WebView2 的主题一致性 |
| `internal/desktophost/dpi_windows.go` | PMv2、初始尺寸和跨屏 DPI 处理 |
| `internal/operations/{types,engine,journal}.go` | 新操作模型、执行、持久化、本地审批/取消 |
| `internal/deviceidentity/` | 配对、身份保护、撤销和活动租约 |
| `internal/mcpserver/operations.go` | 八个新 MCP 工具与能力过滤 |
| `cmd/local-probe-{target,agent,pair,operations}/` | 当前可运行的后端 CLI |
| `prototypes/typography-review/index.html` | 历史交互样稿，仅作设计参考，数据是演示 |
| `scripts/Test-LocalProbeDesktopFrontend.mjs` | 正式前端 24 个 VM 回归场景 |

**正式 `desktopweb/assets` 优先于历史 HTML 样稿。** 样稿中的演示连接、模拟健康/更新和浏览器目录选择器不能直接移植为真实功能。

## 4. 用户已经认可的视觉与行为约束

- 保留“概览 / 连接 / 访问范围 / 活动 / 设置”主结构，拓扑图、树、卡片、时间线、文字与表格配合；不能再做成整页纯文字表格。
- 保留单一侧栏。不要恢复独立 LP 图标轨道、全局右侧检查器或全局“详情”按钮。侧栏右上角 `< / >` 收展，收起只留图标；每个图标和文字必须在同一按钮中对齐。
- 概览展示连接拓扑，连接控制位于概览。连接页主要用于新建与历史复用，新增后进入历史；切换查看对象不能隐式断开其它运行连接。
- 访问范围左侧树、右侧列表，保留只读、命令、高级自定义规则分区；分支可折叠，避免长期使用卡片堆积。暂停/恢复/移除可用图标，但需提示和可访问名称。
- 活动支持空间、连接、通道、设备、类别、结果、时间和关键字组合筛选；事件保留发生时的名称/对象信息，不能随后改写历史。
- 设置保留外观、字号、关于/更新、程序健康、配置备份。诊断导出在程序健康卡片；备份支持原生保存位置/名称，名称不要求用户填写后缀；恢复进入原生文件选择、格式校验和恢复预览。
- 不恢复“字体规格”“字体已加载”“交互预览·示例数据”等全局提示。必要的离线、未授权、待审批、信任变更等状态必须保留，不能为了简洁隐藏。
- 右键、文字选择/复制、F5、Ctrl+R 保持可用。轮询不能反复替换用户正在选中的文字、输入草稿或滚动位置。

### 字体、颜色和尺寸

当前 CSS 默认侧栏 232px、顶栏 48px、正文 14px；正文栈为 Geist / HarmonyOS Sans SC / 系统中文字体，技术值为 Geist Mono / Cascadia Code / Consolas。中文标题使用本机华文中宋（STZhongsong）并有回退。现有名字不代表字体已随包分发；新增字体需处理许可证、离线资源和回退，不能依赖 CDN。

暗色基线：背景 `#181818`、卡片 `#222222`、边线 `#3c3c3c`、正文 `#f2f0ec`、暖灰强调 `#d6cfc2`。不要改成蓝紫色主题。浅色背景为 `#f5f5f1`，卡片白色；具体层级沿用现有 CSS。

字号 12/14/16/18 的调整必须覆盖导航、标题、正文、控件和弹窗。表格表头与行列必须对应，长连接名/ID/路径不能挤乱操作列；用明确列定义、截断/展开、min-width/overflow 处理，不以破坏 table 语义补布局。

### DPI 与原生主题是独立验收项

宿主已有 Per-Monitor V2、WM_DPICHANGED、WebView2 自动缩放。不要用 CSS zoom、页面整体 transform 或位图拉伸修复清晰度，也不要重复叠加系统和页面缩放。

浅/深模式必须同步页面、标题栏/上边框与右键菜单；不能简单固定成某种颜色。已有 `window.ready`、`window.theme`、应用确认和失败重试逻辑，需保留快速深→浅→深切换的最终一致性。原生菜单颜色需宿主/WebView2 配合，不能只改 CSS。

## 5. 应完成的用户流程

### 5.1 新建与复用连接

第一步选择“Web 访问本地 / Agent 远程操作”，再选择支持的传输、目标和授权配置。保存配置不自动连接、配对、启用写权限或批准任务。

| 组合 | 当前实际支持 | GUI 表现 |
|---|---|---|
| Web + OpenAI Secure Tunnel | 既有标准 MCP/官方运行时路径 | 保留；账号实际支持单独验证 |
| Web + Cloudflare Named Tunnel | 既有标准 HTTPS MCP 路径 | 保留 |
| Agent + Cloudflare WSS | 新配对桥接/目标 CLI 已有 | 新增配置和配对流程 |
| Agent + 自建 Relay WSS | 新 CLI 已有，公网只读已验证 | 新增端点、可信 CA、凭据引用和状态 |
| Agent + 官方 Tunnel 标准 MCP | 不等于现有配对 WSS 操作链 | 不将 `/v1/peer` 当成通用官方 Tunnel；未接能力明确不可用 |

GUI 的旧 `ConnectionTransport`/SavedConnection/RPC 还没有等价的 Relay 配置字段；不能只往旧 connections.save 填入新字符串。需后端先定义一致的保存契约。自建 Relay 外层 TLS 在中转终止，文件/操作内容由端点内层 mTLS 保护；普通 Web 客户端不能直接替代持有配对密钥的原生桥接。

历史列表至少区分名称、用途/目标、通道、授权、运行状态、操作。查看、设为默认、启动、停止是不同动作。离线不能显示在线，Tunnel 就绪不能显示为客户端已认证，更不能显示可执行。

### 5.2 配对与设备

提供目标/Agent 角色、公开 bundle/CSR 导入导出、完整指纹核对、连接范围、证书有效期和撤销入口。只展示公开材料和可安全复制的指纹；私钥不得进入 JS、DOM、剪贴板、日志或普通备份。

指纹需用户通过可信方式核对，不因服务器同时返回 bundle 和指纹就自动信任。信任变化/证书过期应阻断连接并明确说明，不能重试后自动接受新设备。

### 5.3 开发者模式与运行器

明确区分只读、受信任主机执行、尚不可用的严格隔离执行。`trusted_host` 使用当前用户文件/网络权限，不是沙箱；必须在授权和审批位置说明这一实际后果。

旧 developer.saveProfile 的 command_profiles 与新 operations.Catalog 是不同模型。新 UI 应管理明确的运行器路径、SHA-256、脚本类型和最少环境变量；通过原生浏览/受控宿主取值，不让远程 Agent任意配置可执行文件。

实际版本来自获批的 kind=version 作业；“已安装/启动时哈希已核对”不能显示为“版本验证成功”。开发者开关不等于可执行，后端 capability 和失败原因应独立显示。

### 5.4 本地审批中心

提供待审队列、详情、批准/拒绝和状态反馈。详情需绑定：请求主体、连接/设备、工作区、操作类别、运行器和映像哈希、argv、完整脚本、资源限制、文件新旧内容/补丁结果、摘要和审批期限。

新旧内容/脚本/输出均按不可信数据处理，使用纯文本/安全高亮，严禁未转义 innerHTML。不得截断关键参数后不提供查看完整内容。用户批准的摘要必须正是看到的那一份，配置或内容变更后原审批失效。

批准只能从可信本地 UI 进入宿主，不注册成远程 MCP 工具；不能把 JS 中一个 `confirmed=true` 视为权限。不要默认勾选信任声明、批量默许未知计划或在刷新后重放批准。

### 5.5 作业与输出

使用真实状态：prepared、awaiting_approval、queued、running、succeeded、failed、cancelled、interrupted、unknown、rejected、expired。unknown 必须与 failed 分开，提示可能已经产生副作用，提供检查/恢复材料，不能自动重跑。

输出在任务结束后按 stdout/stderr 分页读取；后端返回 base64 字节，不保证 UTF-8。需要明确解码/原始数据处理，不能以乱码证明命令失败。当前没有实时输出流，GUI 不应伪造逐行运行日志。

保留 Agent 的 60 秒轮询租约语义：**GUI 本地查看状态不能偷偷代替 Agent 心跳延长远程作业**。本地状态投影应读取只读快照，而不是无条件调用会续租的 Agent status 路径。

远程 cancel_operation 等待回收后返回；本地 CancelLocal 只是持久化请求，GUI 要等待真实终态再显示已停止。进程树清理无法确认时必须保持故障/unknown 并关闭准入，不能继续显示安全就绪。

### 5.6 文件规则、健康和备份

deny 优先；ignore 仅影响发现，不是保密机制。规则编辑需展示影响连接、预览与 revision/CAS 冲突，冲突时保留草稿并重新加载。现有 rules.apply 是离线管理，不证明外部 CLI 目标已停止；在线撤权与选择性 epoch 未完成前，不做“已立即全局生效”的提示。

健康报告区分配置校验、进程、传输、认证、权限、执行器、证书有效期。检查更新在没有真实更新源时明确未配置，不伪报最新版。

现有 `.lpbackup` schema 为 `local-probe.backup.v1`。恢复须校验并预览，不直接解析任意 JSON 成可执行权限。保留恢复时清空/撤销开发者授权的防护；不要把身份私钥或操作账本自动纳入普通配置备份。

## 6. 现有桌面接口与待补契约

正式页面通过 `window.chrome.webview.postMessage` 与宿主通信，固定 origin 为 `https://localassets/index.html`。CSP 禁止任意网络连接和内联脚本；不要为图标、字体或调试 fetch 放宽策略。

实际 envelope：

```json
{"id":"gui-request-1","method":"snapshot","params":{}}
```

响应为 `{id,ok,data,error}`。data 目前是 Snapshot：desktop、connection、workspace、view 及可选规则/备份选择数据。不是通用 JSON-RPC 2.0，不应擅自改 envelope 或向远程公开它。window.theme 由宿主单独处理，确认实际主题后返回。

| 已存在方法 | 当前作用 |
|---|---|
| snapshot | 当前桌面快照 |
| connections.save/select/remove | 保存、选择、移除历史配置；旧参数无 purpose/Relay 等新字段 |
| connection.connect/reconnect/cancel/disconnect/setTransport | 当前单 controller 的生命周期 |
| workspace.pick/add/update/remove | 原生目录选择与授权管理 |
| rules.read/preview/apply | 读取/预览/离线规则提交 |
| developer.configure/saveProfile/removeProfile | 旧开发者配置，未绑定新操作引擎 |
| settings.backup/pickBackup/restore | 原生备份保存/选择/预览/恢复 |
| logs.export / diagnostics.export | 受控日志/诊断导出 |
| window.theme | 宿主主题应用与确认 |

**以下只是待实现的 GUI 本地契约方向，不是已存在 RPC 名称：**

| 契约方向 | 要求 | 可复用后端 |
|---|---|---|
| 连接详情/能力/多实例状态 | connection ID、用途、目标、传输、各层 readiness、受影响范围；支持独立启动/停止 | config、连接宿主；完整多实例控制面待补 |
| 配对申请/核对/签发/导入/撤销 | 公开材料、指纹、范围；私钥留宿主；本地认证与撤权 | deviceidentity |
| 运行器目录表/执行策略 | 原生选择、哈希核对、显式权限和 revision | operations.Catalog、config v2 |
| 审批列表/详情/批准/拒绝 | 本地所有者上下文、ID/digest、到期与确认；禁止从远程主体字段取得权限 | Journal.Records / Decide |
| 作业列表/详情/输出/本地取消 | 只读投影、不续 Agent 租约；取消后等待终态；输出有界 | Journal、Engine、CancelLocal |
| 健康/证书/诊断 | 真实状态、脱敏、明确未实现能力 | 现有诊断及后续宿主投影 |

优先直接复用 Go API 或可信的类型化本地 IPC。不要拼接 shell 命令调用 CLI，不将私有路径/运行器由远程参数任意指定。若先用固定 CLI 适配，也须固定程序、结构化 argv、超时、权限校验与严格结果解析，并明确临时方案边界。

新快照/错误 DTO 和本地 RPC 必须先与后端对齐，再让页面依赖它们；这里不预设尚未存在的字段就是稳定 API。

## 7. 当前后端限制，设计时必须体现

- 操作目标 Windows；Linux Agent 尚未实机运行。此次测试方向是 Linux Agent → Windows 目标。
- 运行器需要本地明确配置和哈希，默认不继承全部环境。每作业最多32进程、1 GiB汇总提交内存、累计用户态CPU/墙钟预算、每路64 KiB输出；不是网络/文件沙箱。
- 默认作业超时60秒，上限30分钟；审批五分钟；Agent运行状态轮询租约60秒。
- 文本文件上限128 KiB；精确补丁最多32个唯一上下文编辑。已有文件是保存 before image 后锁定句柄更新，**不是原子替换**；异常可能部分写入，必须保留 unknown/人工检查路径。
- 当前 cmd 脚本不接额外 args，作业目录不得有空格/cmd元字符；准备阶段拒绝不支持组合。不能在 GUI 中悄悄改成 shell 拼接。
- 历史作业最多128个，约64 MiB账本预算，尚无自动归档；需明确满额/维护状态。
- 当前全局配置 revision 改动会使旧审批失效；连接级选择性 epoch 和在线规则尚未完成，不能承诺只影响一个连接的配置热更新。
- 已验证两连接作业并发；完整多连接生产宿主仍待做，计划验收至少三条连接独立生命周期。
- 之前的真实公网验证覆盖只读链路。新增命令/写入尚未经过真实 Relay 和另一电脑，不可在 GUI 宣称已跨机验收。

## 8. 工作拆分与责任

| 优先级 | GUI 负责人交付 | 后端依赖/协作 |
|---|---|---|
| P0 基线与设计 | 复用当前正式页面；补双用途、配对、审批/作业的可操作样稿与状态清单 | 核对真实字段，记录接口缺口；演示数据只能留在隔离样稿 |
| P0 类型契约 | 定义并评审本地 DTO、错误与异步状态；保留只读兼容 | 后端确认权限/生命周期，补缺失管理 API |
| P1 单连接真实闭环 | 用途→配置→配对→授权→待审→批准→真实结果/取消 | 目标运行器托管、审批/作业本地投影 |
| P1 多连接与规则 | 独立卡片/拓扑、筛选、影响范围、冲突草稿、明确待接能力 | 多实例宿主、在线撤权、选择性 epoch |
| P1 桌面一致性 | 原生主题、DPI、目录/备份对话框、右键/复制/刷新 | 必要时共同修改 desktophost/fork 并保留实际测试 |
| P2 验收与打包 | 截图、测试记录、独立开发包、文档、明确遗留项 | 后端封板后做 Linux/真实 Relay 联调 |

新增关键交互可先交付独立可操作 HTML 样稿供人工确认；已认可的现有布局无需推倒重做。正式页面不得混入 demo ID、模拟在线、假版本、假健康或自动批准。

## 9. 验收清单

### 功能与权限

- 新建后能在历史找到并复用同一配置；查看/选择不误断其他连接；至少三条连接生命周期以真实宿主为准。
- 只读默认不变。只有显式可写 profile + trusted_host + 开发者连接许可 + 工具许可 + 本地批准，才可执行对应操作。
- 改变脚本/参数/文件版本/授权后，旧审批不能继续；失效/撤权/过期/存储故障可见且不自动放开。
- 两个主体/连接之间看不到或取消不了对方任务；本地所有者可查看/取消自己的所有受管任务。
- 相同 nonce 重试不重复执行；未知结果不自动重跑。规则/恢复冲突保留用户修改和输入草稿。
- 令牌不写日志/URL/localStorage/普通备份；私钥不送到页面。秘密输入若暂经页面，及时移交可信宿主并清空，不回读明文。

### 视觉、桌面与可访问性

- 宽/窄窗均检查侧栏图文对齐、长连接名/路径、表头对应、树与列表联动、弹窗和键盘焦点。
- 明暗切换覆盖页面、原生上边框与右键菜单，快速切换、失败重试和刷新后仍一致。
- 实际 WebView2 检查100/125/150/200%缩放、窗口缩放及混合DPI跨屏；缺设备的项目标未验收，不能用浏览器样稿代替。
- 正文、错误、脚本、输出可选择复制，右键/F5/Ctrl+R可用；轮询不吞掉草稿、选区或滚动。
- 图标按钮有 tooltip/aria-label，收展状态、键盘操作与深色输入框对比清楚。必要状态不能仅靠颜色区分。

### 自动检查

```text
node scripts/Test-LocalProbeDesktopFrontend.mjs
go test ./...
go test -race ./...
go vet ./...
```

新增 UI/RPC 行为要补相应测试；涉及原生主题/DPI需运行有关宿主测试并做真实窗口人工检查。现有前端 VM 用例只验证逻辑与部分结构，不证明像素、原生对话框或所有电脑清晰度。

构建脚本是 `scripts/Build-LocalProbeDesktop.ps1`，启动脚本是 `scripts/Start-LocalProbeDesktop.ps1`，打包验证是 `scripts/Test-LocalProbeDesktopPackaging.ps1`。使用明确的 GoPath 与新的输出目录，先阅读脚本：Desktop 构建还会生成 `bin/local-probe-mcp.exe`，仅改 GUI OutputPath 不代表不会覆盖其它文件。不要覆盖用户正在运行的 EXE、改真实授权或启动真实 Tunnel 做设计验收。

仓库 go.mod 最低 Go1.25；本轮证据使用Go1.26.5。有本地 go-webview2 fork 和许可证要求；新增依赖/字体需审查许可、离线打包、CSP、包大小与原生兼容影响，默认复用现有技术栈。

## 10. 最终交付物

1. 修改后的正式页面/样式/图标及需要的受控宿主桥接代码，不能只交图片或独立 HTML。
2. 新交互样稿（如使用）、关键页面/明暗模式截图、真实窗口与DPI验收结果。
3. 稳定的本地 DTO/RPC 说明、后端缺口清单及已联调能力矩阵。
4. 自动测试与人工步骤/结果，明确失败和未验收项目；禁止用配置意图替代运行证据。
5. 独立可运行开发构建、操作手册和遗留项；未完成的按钮有真实禁用理由。

此次交接不包含提交/推送/合并/Release 授权。公开交付前另做敏感信息、许可证和包检查。真实 Relay 与用户 Linux 电脑的操作验收，放在后端与 GUI 开发完成后按用户安排进行。

## 11. 继续阅读

- [总体技术实现书](R12_DUAL_MODE_REMOTE_IMPLEMENTATION_PLAN.zh-CN.md)：完整范围和阶段；历史未决项以最新执行决策/源码为准。
- [实际实施状态](IMPLEMENTATION_STATUS.md)、[R12进展](R12_PROGRESS.zh-CN.md)：按日期区分历史与最新实现。
- [受信任执行决策](R12_EXECUTION_DECISION.zh-CN.md)：用户已明确选择 trusted_host，无需重新询问同一产品选择。
- [操作后端用法与限制](R12_OPERATIONS_BACKEND.zh-CN.md)：作业、审批、文件及资源约束。
- [配对与通道](R12_PAIRING_AND_TRANSPORT.zh-CN.md)、[Relay部署记录](R12_RELAY_DEPLOYMENT.zh-CN.md)：连接方式与实际公网边界。
- [本轮后端证据](evidence/r12/2026-10-24-operations.md)：真实本机测试范围，不能替代GUI/跨机验收。

**给接手人的开始指令：** 克隆 `feat/r12-dual-mode` 并记录提交，核对正式GUI资源，复现24个前端VM基线；据本交接书列出页面/本地契约缺口，复用当前风格完成新增交互样稿和类型化适配。对尚未实现的后台控制面提出明确接口需求，逐个接入真实闭环，保持默认只读、逐次本地审批、主题/DPI/右键等既有行为，提交独立构建与验收记录。
