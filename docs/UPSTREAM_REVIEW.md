# 上游调查、来源锁定与复用决定

日期：2026-09-07。本文件只记录实际读到的证据和当前决定；完整下载、源码移植及性能复现属于后续任务。提交锁定用于调查可重复性，不自动作为发行依赖。

## 已调查内容

| 来源 | 调查 commit / 文件 | 实际证据与决定 |
|---|---|---|
| LE-saber/deep-project-orchestrator | README.md；skill/deep-project-orchestrator/SKILL.md；software-projects.md；verification.md | Pro 接管、计划后具体执行、分支/PR、真实验证；本轮只读该仓库 |
| LongNgn204/local-coding-agent | 95144e610ddcc3bb5a879117803907c008b1a88e；README.md；repo-support/server README 搜索片段 | 借鉴概览→检索→窄读/批量的交互思想。README 标注 AGPL-3.0，不复制代码、提示词或 UI；未完成全部源码审计 |
| int04/ChatCmd | ad299fc6058d136cd6839ba14eb1e8de5122722c；LICENSE；docs/adr/0020-repository-index.md；工具文档搜索结果 | LICENSE 为 MIT；ADR 描述 batch/index/fallback 也明确记录百万路径负面性能结果。只参考语义，不默认移植整个 runtime |
| hypnguyen1209/codex-free | cb487c744e2220909548ec112d3fe2a5e26cb2f1；README.md；docs/ARCHITECTURE.md 搜索结果 | 文档描述官方 runtime、认证 loopback 和监督机制。源码/完整许可证尚未审计，不复制实现 |
| openai/tunnel-client | 9f77746a5498289f04e1ae6d3e0c830f3871af52；官方 Tunnel 页面与仓库公开说明 | 优先使用官方独立客户端，后续审查精确 binary/version/flags/license；本轮未下载运行客户端 |
| modelcontextprotocol/go-sdk | 官方 GitHub README/入口 | 产品 MCP 使用官方 SDK；尚未选择 release、下载或加入依赖 |

准确的选择性下载文件与 PowerShell sparse-checkout 命令在主计划 P00；不要把那里列出的待取材文件一律记作本轮已完整阅读。

## 独立实现决定

本轮新增的 Go readcore 和 tests 为本项目独立实现，未复制上游源码。不将 LCA 的 JS 服务、ChatCMD 的 Rust runtime 与另一套 Agent 全部拼接。先做窄且可测试的契约，再用正式适配器接 MCP 和 Tunnel。

未来直接复用某段代码前：读取其文件和目录许可证；记录固定 SHA/路径；判断是否与项目最终许可证兼容；保留版权/许可通知；记录修改；跑本项目边界测试。未选择对外许可证不代表可以忽略上游义务。

## 为什么没有承诺索引总是更快

ChatCMD 的 ADR 0020 在当前固定版本中记录百万路径 indexed find 慢于某个 direct workload，batch stat 也不总优于 sequential。该结果是上游作者的测试记录，未由本轮复现。Local-Probe 必须分开测网络往返减少、文件系统吞吐和模型证据质量。

索引是可关闭的加速器，不是真相源；新文件缺失与 stale coverage 必须可见。没有证据时不引入文本向量数据库或复杂符号索引。

## 官方接口资料

- Developer Mode: https://developers.openai.com/api/docs/guides/developer-mode
- Help Center: https://help.openai.com/en/articles/12584461-developer-mode-and-mcp-apps-in-chatgpt-beta
- Secure MCP Tunnel: https://developers.openai.com/api/docs/guides/secure-mcp-tunnels
- Root-relative file operations: https://go.dev/blog/osroot
- Go releases: https://go.dev/dl/

Developer Mode 与 Help Center 的套餐口径不完全一致，详见 COMPATIBILITY.md；不能用任何第三方 README 的“支持 Pro”替代本机目标账户测试。

## CI 供应链锁定

GitHub Actions 引用通过 API 解析固定到 commit：checkout v4 → 11d5960a326750d5838078e36cf38b85af677262；setup-go v5 → 40f1582b2485089dde7abd97c1529aa768e1baff。Go 测试版本固定为本次官方下载页面所列 1.26.5。固定版本不代表永不更新；后续更新必须审查和重新运行测试。
