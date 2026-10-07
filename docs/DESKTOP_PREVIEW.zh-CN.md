# Windows Desktop Preview

本文描述当前 Windows Desktop Preview 的本机启动和验证边界。项目自有源码采用 MIT（见根目录 LICENSE）；第三方代码保留各自许可证。当前版本是未签名的功能预览，不是 production-ready 的稳定发布物。第三方许可证汇总由打包脚本依据 `go list -m all` 生成，发布前须复核。

## 构建包

在 Windows 仓库根目录先构建两个宿主产物，然后生成带版本源码快照和依赖许可证的唯一包目录及 ZIP：

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\Build-LocalProbeDesktop.ps1
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\Package-LocalProbeDesktop.ps1
```

包会生成在 `dist\desktop-preview\Local-Probe-Desktop-Preview-<UTC 时间>-<随机 ID>`，同名目标已存在时脚本会失败，不覆盖旧包。打包器使用明确的目录 allowlist（源码、界面资产、示例配置、脚本、文档和本地第三方 WebView2 fork），不会以 `git ls-files` 作为输入清单。仅 `configs\*.example.json/yaml` 配置样例进入包；项目 LICENSE 会被复制，`.runtime`、`.secrets`、`.tools`、审计/日志数据、用户授权目录和缓存不会被复制。发布准备从已审查 commit 的干净本地 clone 构建，不从个人运行树打包。请仍在归档前复核包内容和生成的 `third_party\NOTICE.txt`。

检查包结构与干净解包：

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\Test-LocalProbeDesktopPackaging.ps1
```

该检查创建一次唯一测试包并保留它供人工复核，在包内隔离目录解压，检查排除项和两个 EXE 哈希，并在解包树中构建 Desktop 与 MCP CLI、运行前端契约脚本。它不启动 MCP 进程、HTTP 服务器、Tunnel 或真实账号流程。

## 启动与首次配置

先将 ZIP 解压到一个新的、权限合适的本地目录，再从解压根目录运行：

```powershell
.\bin\local-probe-desktop.exe
```

桌面程序默认从可执行文件的上两级目录定位 `.runtime\local-probe.json`；审计输出默认写入当前 Windows 用户配置目录下的 `Local-Probe\audit`。可以通过 `-config <绝对路径>` 和 `-audit-dir <绝对路径>` 覆盖这两个路径，也可以用 `-transport cloudflare_named` 或 `-transport openai_runtime` 选择连接类型。`-version` 只显示版本并退出。

缺少配置文件时，应用仍可打开界面；它只为配置路径创建父目录，不生成配置、授权根目录、token 或其他凭据。首次运行可能显示未配置/未连接状态，这是预期状态，不代表 ready。WebView2 Runtime 不随包安装：缺失时应用会提示无法启动，不会自动下载或安装；请按组织批准的软件安装流程准备 Runtime。

配置授权必须由操作者明确完成。`Setup-LocalProbePreview.ps1` 仅用于其文档所述的 Cloudflare Preview 配置脚手架，要求传入一个已存在且由你选择的 `-AuthorizedRoot`；它不会再默认授权仓库。该脚本可写 `.runtime`、外部 `.secrets` 中的占位材料并保留既有配置；只有明确指定 `-ForceConfig` 才会覆盖其目标配置。运行前先阅读：

```powershell
Get-Help .\scripts\Setup-LocalProbePreview.ps1 -Full
```

不要把包目录、仓库根目录或包含凭据/个人资料的父目录当作授权根目录。不要把现有真实配置复制到包中，也不要为了预览测试读取或导出真实用户 secret。修改根目录选择/暂停状态会收紧 MCP 的有效读取 scope；旧授权范围的文件读取应被拒绝，旧分页 cursor 在恢复后不应继续有效。所有工作区根目录暂停时，scope 是显式空集合，不回退到仓库或系统根目录；当前 MCP 连接需要至少一个启用根目录才能重新建立。先恢复至少一个授权根目录，再连接。保存成功只代表配置已落盘，只有重新连接后状态检查完整通过才可显示应用成功/ready。

## 验证层级与限制

### R11.3 主题同步确认与恢复（2026-10-07）

最新独立 GUI 为 `bin/local-probe-desktop-r11.3.exe`，版本 `R11.3-desktop`。当前窗口正常时不必立即重启；需要换版时从托盘退出旧 GUI 再启动新版。此次未停止用户 GUI 或连接，不覆盖旧程序。

页面就绪后独立同步原生主题，不再依赖后台配置读取成功；宿主确认设置并回读 WebView2 菜单主题后才视为成功。失败追加最多三次重试，快速切换以最后一次选择为准；持续失败会提示检查 Windows/WebView2 支持。原生成功/失败会记入现有宿主诊断日志。复制、文本选择、右键和刷新保持默认行为。Windows 11 Build 22000+ 的标题栏支持要求不变，不改 Windows 系统主题。

验证：根模块 test/race/vet、fork edge test/race、前端 24 个 VM 场景通过；独立隐藏窗口中实际 Profile 深→浅→深回读与真实页面到生产主题 RPC 通过，后台 snapshot 未响应也能恢复原生主题。用户报告间歇故障已自行恢复，确切触发原因未复现；未声称新版可见窗口的人工颜色验收已完成。换版后可按下节顺序检查深浅主题、文本右键复制及刷新。

### R11.2 标题栏与右键菜单主题（2026-10-07）

该阶段独立 GUI 为 `bin/local-probe-desktop-r11.2.exe`，版本 `R11.2-desktop`。从系统托盘退出旧 GUI 后运行新版；关闭窗口通常仅隐藏到托盘，不会释放单实例占用。主题修改与保存不改连接配置，也不启动/停止连接。

顶部主题按钮及“设置 → 外观 → 主题”同步页面、Windows 原生标题栏/边框/标题文字和 WebView2 内置右键菜单；刷新会恢复已保存的主题。CSS color-scheme 同时使浏览器内置表单/滚动条采用对应模式。右键、复制、文本选择和 Ctrl+R/F5 保持原有行为，未使用自制菜单代替浏览器菜单。完整自定义标题栏色依赖 Windows 11 Build 22000+；旧系统/Runtime 不支持时显示同步失败提示，页面与其它功能继续可用，不修改 Windows 系统主题。

验证：根模块全项目 test/race/vet、依赖 fork edge 包 test/race、前端 21 个 VM 场景 PASS；独立隐藏原生窗口验证 DWM dark BOOL 切换，独立临时 WebView2 profile 实际验证深→浅→深设置/回读。新版实窗启动因旧版 GUI 仍在托盘被拦截，没有停止用户实例；视觉颜色仍待人工检查。fork 全包的额外 vet 有既有 secure_host_windows.go unsafe.Pointer 回调转换警告，详见实施状态。

人工顺序：切到深色，检查标题栏及页面背景右键菜单；切到浅色再检查；在文本框选中文字并右键确认复制菜单；Ctrl+R 后检查页面重新加载与原生主题保持一致。无需连接真实 Tunnel 或修改授权。

### R11.1 离线文件规则（2026-10-07）

本轮独立 GUI 为 `bin/local-probe-desktop-r11.1.exe`，未覆盖旧程序。先从托盘退出旧 GUI（点击窗口关闭通常只是隐藏），然后运行新版；默认仍读取同一 `.runtime/local-probe.json`，也可用 `-config <绝对路径>` 指定测试配置。GUI 版本为 `R11.1-desktop`。本轮未生成新的 ZIP 或 MCP 产物。

人工检查顺序：

1. 进入“访问范围 → 高级自定义”，核对左侧文件树、右侧有效规则和目录；选择“连接规则”或“编辑目录规则”。连接与目录规则叠加，编辑框仅加载所选层的内容。
2. deny/ignore 每行一个相对 glob。在待判定路径中输入授权根下的相对路径，目录以 `/` 结尾，点击“预览规则与影响”。deny 表示禁止访问，ignore 仅隐藏发现/搜索结果，不能代替访问拒绝。预览不读取/扫描工作文件。
3. 修改输入后旧预览和离线勾选失效，须重新预览。无变更不允许保存；无效规则、旧 revision 等显示错误而不改配置。先用测试配置检查这些拒绝情形。
4. 确认全部受影响连接，手动停止其它使用同一配置的 MCP/GUI 实例，再勾选离线声明、保存并确认。GUI 只负责停止自己的连接；它无法核实其它实例。停止失败不保存；revision 冲突保留外部内容并保持停止，重试时草稿仍在但须重新预览。
5. 保存成功后不会自动重连；明确点击连接使新配置生效，再检查文件规则。留空保存仅清空所选层，不清除另一层。此功能不会写入或删除工作文件，也不提供 write_allow 或在线全局撤权。

本轮自动证据：全项目 test/race/vet PASS、前端 19 个 VM 场景 PASS；原生 WebView2 已检查真实读取及三类路径判定、暗色多行输入文字颜色、文本全选、右键复制菜单及 Ctrl+R 后重新加载配置。原生 UI 测试仅预览，没有自动保存/改权限；后端生产桥接持久化与失败流程使用独立合成配置验证。未实际覆盖用户剪贴板。测试窗口已停止；合成配置/空工作目录保留在系统临时目录中，本轮没有删除用户文件。

严格网络专项实验按用户要求暂缓，历史失败仍有效；实际命令、脚本、构建执行仍关闭，工具版本用户入口及生产多连接并发尚未交付。不要把可编辑开发者配置误认为已有执行能力。

### R10.2 高清缩放与开发者配置

GUI 在 COM、原生对话框与 WebView2 创建前启用并核实 Per-Monitor V2 DPI awareness。跨屏移动或显示器缩放变化时处理 `WM_DPICHANGED` 的推荐物理矩形；WebView2 保持默认的自动显示器缩放检测，使用真实客户区物理像素 bounds，不人为固定 DPR，不用 CSS zoom/transform 放大位图，也不强制灰度字体平滑。初始窗口按当前 DPI 计算，并限制在该屏幕工作区内，适应高缩放的小屏幕。旧系统或有效 DPI 上下文不符合要求时明确报错，不静默降级成模糊模式。支持范围为 Windows 10 1703+ / Windows 11 amd64 与可用的 WebView2 Runtime，不声称其他 OS 或强制兼容性缩放也已验证。

依据：[Windows PMv2 初始化](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setprocessdpiawarenesscontext)、[DPI 变化建议矩形](https://learn.microsoft.com/en-us/windows/win32/hidpi/wm-dpichanged)、[WebView2 显示器缩放检测](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2controller3)。

自动测试覆盖 96/120/144/168/192/240/288 DPI 的尺寸计算与原生隐藏窗口消息处理。可选 `LOCAL_PROBE_DPI_WEBVIEW=1` 测试使用独立临时 WebView2 数据目录和隐藏窗口，检查当前真实 DPI、页面 DPR、viewport 与客户区物理像素匹配；不操作用户 GUI、不读取真实配置或启动连接。它不能代替真实双屏和不同 GPU/驱动的视觉验收。人工应在 100%、125%、150%、175%、200%、250%、300% 缩放下检查文本、SVG、菜单与文件窗口，并拖到不同 DPI 的第二块屏幕、最大化/还原和改变字体大小，确认无需重启、不产生位图拉伸或遮挡。应使用显示器推荐分辨率，并关闭 EXE 属性中的“高 DPI 缩放替代”。不能承诺有限本机测试已覆盖所有电脑。

“访问范围 → 命令”提供开发者配置状态和受限命令模板管理；“权限与高级模式”可显式开启/关闭配置并选择允许的 connection。默认关闭；关闭清空 connection 授权但保留模板；移除 connection 会同时撤销其开发者授权。选中的连接共享当前命令模板，**不是逐 connection/逐目录独立命令 allowlist**。模式、连接选择与模板均通过严格配置校验、当前 revision 的 CAS、先停止自有连接再保存，不自动重连，也不启动命令进程。新建连接不会自动获得开发者授权；备份恢复继续清除所有命令模板与模式授权。

模板编辑器支持配置模型已定义的 `version_probe` 和 `fixed_command` JSON：绝对本地 `.exe` 路径、预先审批的 SHA-256、固定 argv/受限 typed slots、私有空 cwd、净化 env、输出/时间/子进程上限、逐次本机确认和强制网络拒绝。模板字段校验不等于验证磁盘上真实可执行身份，也不等于安全执行该程序；不提供任意 Shell 输入框。编辑失败保留当前会话中的草稿供重试。模板与路径只用于本地管理视图，不进入日志/诊断导出。

**实际本地/远程命令执行尚未完成。** Windows WFP 后端仍是 `DisabledBackend`，缺少受限 broker/可信 suspended launch 与生产身份/网络验证；GUI 永远将 `execution_available` 设为 false，不注册 `developer.run`、`command.run` 或 MCP `run_probe`，也不会使用 fake backend 或把配置字段当作 OS enforcement。接下来需单独实现并审查该执行链；管理员服务安装、系统网络策略更改以及真实命令运行仍需明确授权，不在本次 UI/DPI 改动中自动进行。

### R10.1 配置备份与恢复

在“设置 → 配置备份”点击创建备份会打开 Windows 保存窗口；选择目录并填写基础文件名即可，程序自动添加 `.lpbackup` 后缀。使用新名称；不会覆盖已有文件。该格式为带 `local-probe.backup.v1` 标识、创建时间、经过配置模型验证的内容和 SHA-256 完整性校验的 JSON。校验不是数字签名，不证明文件来源可信；请自行妥善保管包含本地路径和凭据引用的备份。它不包含凭据内容、工作文件、审计记录或界面外观偏好。

点击恢复备份会打开专用文件选择窗口。只有格式、版本、内容、大小和完整性校验通过，才显示连接、授权配置、目录数量及创建时间，并要求再次确认。确认绑定到已验证的内存快照和当前配置 revision，5 分钟过期且不可重复使用；选定文件随后被替换不会改变确认对象。恢复前必须安全停止当前自有连接；配置保留为 `.bak`，所有目录暂停，命令授权清除，连接不自动启动。取消选择、无效文件、过期选择、revision 冲突或停止失败不会替换当前配置。尚无配置时也可从有效备份恢复，现有配置损坏时不会直接覆盖它。

早期内部 `.desktop-backup.json`、普通配置 JSON、日志或诊断 JSON 不是这个文件格式；旧文件不删除、不静默转换。请用 R10.1 的“创建备份”生成可通过本版本校验的文件。文件对话框实际交互仍需人工验收；自动化测试验证的宿主回调、文件读写和 RPC 流程不能替代它。

把验证结果分开记录，不能互相替代：

1. **程序启动**：Windows EXE 能启动进程；若 WebView2 Runtime 缺失，记录该本机环境未满足前置条件。`-version` 不验证图形界面。
2. **本机 UI**：在当前机器上打开窗口并检查未配置、添加/暂停/恢复根目录、连接状态、导出取消/关闭等操作。界面显示 ready 仅表示当前本机受控启动与健康检查满足代码条件，不证明用户端已收到数据，也不证明外部系统接受了连接。
3. **真实账号链路**：须在单独授权的测试根目录和测试凭据下，按对应传输方式实际登录/连接并由目标账号侧确认结果。它可能启动本机子进程和网络连接；本包默认测试不会做此事，不要拿个人或生产 secret 代替测试凭据。

本轮自动化证据仅涵盖仓库单测/竞态检查、官方 MCP Go SDK 的 in-process server 与 loopback 客户端集成测试、干净解包构建/前端契约，以及 Windows 本地启动或 UI 检查中明确记录的项目。SDK 集成测试没有启动 Windows MCP 子进程。请以实际测试日志为准；未执行的验证不能推定通过。

当前仍有稳定发布前的未关闭事项，包括发布审查、目标 Windows 机器上的签名/安装/升级/卸载与安全评估、WebView2 Runtime 运维策略，以及经批准的真实账号端到端验证。许可证已选定为 MIT；该预览不应被称为安全审计完成或 production-ready。
