# Local-Probe R11.3 Desktop Preview — Release 草稿

拟议版本标签：`v0.11.3-preview.1`。本文件不创建标签，也不代表 Release 已发布。

## 发布范围

Windows x64 未签名便携预览，包含 GUI、匹配的 MCP 程序、源码、无秘密的配置样例、使用文档和第三方许可通知。GUI 版本 `R11.3-desktop`；项目自有代码 MIT，第三方组件按各自许可证分发。

新增/合并能力：

- 全新 WebView2 桌面界面、折叠导航、链路结构图、历史连接复用、授权目录树与活动筛选。
- Cloudflare Named Tunnel 与官方 OpenAI Secure MCP Tunnel 的本机受控启动/停止；外部账号、凭据与 Workspace 关联由用户自行完成。
- 目录浏览、只读授权、连接与目录两层文件规则，基于真实 policy 的预览与 revision 校验、离线保存。
- 健康状态与脱敏诊断导出、`.lpbackup` 原生保存/选择/校验恢复。恢复会清空开发者命令授权，不静默扩权。
- DPI 适配及页面/原生标题栏/右键菜单主题同步；确认、有限重试与快速切换合并，不禁用复制、选择或刷新。

## 已知限制

- 默认只读；开发者配置/命令模板不等于执行开放。命令、脚本、构建测试执行、写文件、真实生产 broker/严格网络隔离仍未交付，`execution_available=false`。
- 历史连接可保存和复用；当前 GUI 只管理选定连接，不提供多个真实连接同时运行。
- 不包含官方 Tunnel 客户端、WebView2 Runtime 安装器、自动更新、代码签名或 Linux/macOS GUI。
- 完整标题栏配色依赖 Windows 11 Build 22000+；多电脑/混合 DPI 双屏清晰度没有完整人工验收矩阵。
- 真实账号/Cloudflare/OpenAI/Tunnel 验收与本地单测分别记录；本轮发布准备不重新使用用户凭据测试连接，不修改系统策略。
- `release_ready=false` 仍指生产稳定发布门，不能将 Preview 或 CI 通过当作独立安全审计完成。

## 安装与隐私

解压到新目录，准备 WebView2 Runtime，运行 `bin/local-probe-desktop.exe`。首次配置见 [Desktop 操作说明](DESKTOP_PREVIEW.zh-CN.md) 与 [部署说明](DEPLOYMENT_PREVIEW.zh-CN.md)。旧 GUI 须从托盘退出才能切换版本；备份用户配置时自行保管，不上传到 issue 或 Release。

包只从已审查提交的干净本地 clone 构建，使用打包 allowlist；不包含 `.runtime`、`.secrets`、`.tools`、个人授权目录、账号令牌、备份、审计和运行日志。上传前复查源码/历史敏感扫描、ZIP 清单、第三方许可、两个 EXE 与 ZIP 的 SHA256；无扫描工具可保证识别所有隐私信息。

## 发布前检查

- 项目 LICENSE 和第三方许可证进入包。
- 源码单测、race、vet、前端 VM、干净解包重建和打包排除检查通过。
- 本地非 Quick acceptance 报告不得把 `release_ready` 改为 true。
- 记录精确 source commit、工具链、资产 SHA256；核对远端 main 与该提交一致。
- 未经所有者最终发布决定，不创建正式稳定版、不自动公开 Release。

本轮干净 clone 的 test/race/vet/build、前端 24 场景、两项 fuzz、ZIP 排除/许可/哈希与干净解包重建通过；修正键盘枚举文件名误报后，非 Quick acceptance 为 10 pass、0 fail，6 个外部人工门明确 skip，`release_ready=false`。Gitleaks 历史/源码/包扫描未发现密钥。源 commit 与最终资产 SHA256 将随准备包的外部 manifest 提供；远端 CI 结果另查，不由本地测试代替。实际证据见 [IMPLEMENTATION_STATUS](IMPLEMENTATION_STATUS.md)。
