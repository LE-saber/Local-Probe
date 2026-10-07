# 上游调查、来源锁定与复用决定

日期：2026-09-08。任务：P00。本文只记录本轮实际完成的来源锁定和读取证据；
它不是对上游产品质量、许可证兼容性或 Local-Probe 已接通能力的背书。

## 1. 执行结论

- 目标仓库当前工作分支为 `feat/readcore-foundation`，从已有的
  `origin/feat/readcore-foundation` 建立本地跟踪分支；没有覆盖 `main`。
- 上游检出均位于目标仓库外的 `<clone-parent>\_references`，使用
  detached HEAD 和 sparse-checkout，只保留调查文件。没有运行上游安装脚本，也没有把
  上游源码、提示词、UI、生成文件或二进制复制进 Local-Probe。
- LCA 的许可证是 AGPL-3.0-or-later；ChatCMD 和 Codex Free 的许可证是 MIT；
  OpenAI `tunnel-client` 是 Apache-2.0；官方 MCP Go SDK 的 README 说明新贡献使用
  Apache-2.0、既有代码使用 MIT。Local-Probe 的公开许可证仍由所有者另行选择。
- 四个计划锁定的上游 commit 均成功 clone、稀疏检出并通过 `rev-parse` 复核；没有
  上游下载失败。官方 MCP Go SDK 在本轮选择维护中的稳定 release `v1.7.0`，并同时
  记录 tag 对象和其剥离后的 commit（见下表）。
- 本轮亲测仅限 Git 对象、检出状态、文件存在性和本地文档/元数据读取；没有把上游
  README 的“支持 ChatGPT/Pro”、权限说明、性能数字或 tunnel 流程写成 Local-Probe
  的实测结果。

## 2. 固定快照与实际读取文件

“文档关键段”表示读取了与本项目边界、文件读取、预算、连接、权限和故障状态直接
相关的章节；未声称完成整个上游仓库的源码审计。源码文件只用于定位接口/生命周期，
没有移植实现。

| 来源 | URL 与锁定身份 | 外部检出 | 实际读取文件 | 许可证与结果 |
|---|---|---|---|---|
| Local Coding Agent (LCA) | `https://github.com/LongNgn204/local-coding-agent`<br>commit `95144e610ddcc3bb5a879117803907c008b1a88e` | `_references/lca` | `README.md`（文档关键段）；`LICENSE`；`SECURITY.md`；`server/README.md`；`server/package.json`；`server/package-lock.json`（lockfile 元数据）；`skills/repo-support/SKILL.md` | `AGPL-3.0-or-later`。只借鉴交互思想；不复制代码、提示词或 UI。 |
| ChatCMD | `https://github.com/int04/ChatCmd`<br>commit `ad299fc6058d136cd6839ba14eb1e8de5122722c` | `_references/chatcmd` | `LICENSE`；`Cargo.toml`；`docs/mcp_method.md`（filesystem、batch、catalog 章节）；`docs/tool-resource-budgets.md`；`docs/adr/0020-repository-index.md`；`crates/chatcmd-mcp/src/tool_catalog.rs`（catalog 生成与 capability 关键段） | `MIT`。只参考契约/预算/索引的公开描述；不复制 Rust runtime。 |
| Codex Free | `https://github.com/hypnguyen1209/codex-free`<br>commit `cb487c744e2220909548ec112d3fe2a5e26cb2f1` | `_references/codex-free` | `README.md`（native tunnel、权限、loopback 关键段）；`Cargo.toml`；`docs/ARCHITECTURE.md`（架构与 native tunnel 章节）；`LICENSE`；通过 `git ls-tree` 定位并读取 `src/openai_tunnel.rs`、`src/quickstart.rs` 的生命周期/凭据关键段 | `MIT`。只把其明确的 sidecar、健康检查和凭据隔离作为设计输入；不复制 Rust 实现。 |
| OpenAI tunnel-client | `https://github.com/openai/tunnel-client`<br>commit `9f77746a5498289f04e1ae6d3e0c830f3871af52` | `_references/tunnel-client` | `README.md`；`LICENSE`；`docs/onboarding.md`；`docs/permissions.md`；`docs/configuration.md`；`docs/connectors.md`；`docs/troubleshooting.md` | `Apache-2.0`。只记录官方客户端/文档的使用边界；本轮没有下载、运行或打包其发行二进制。 |
| 官方 MCP Go SDK | `https://github.com/modelcontextprotocol/go-sdk`<br>tag `v1.7.0`，tag object `25cb00203c6b693780f602ab4041c06f7f4b9570`，peeled commit `bc72835f62eb94d0fb484439f886b6885b075f36` | `_references/mcp-go-sdk` | `README.md`；`go.mod`；`LICENSE`；`examples/http/README.md`；`examples/http/main.go`；`examples/http/logging_middleware.go` | README：新贡献 Apache-2.0、既有代码 MIT；`go.mod` 声明 Go 1.25.0。计划 P05 使用 SDK 前仍需按依赖锁定和许可证通知要求复核。 |

### 2.1 读取到的可用事实（仍属于上游陈述）

以下内容是上游文档或源码明确写出的行为，不能替代 Local-Probe 的测试：

- LCA 的 server README 列出 `read_file`、并发/行范围的 `read_many`、`find_files`、
  `search_text`、`workspace_snapshot` 等工具；默认 loopback MCP 地址为
  `127.0.0.1:8787`，另有本地 dashboard。其 SECURITY.md 明确说这不是 OS sandbox，
  `full` 模式下命令可能以当前用户权限运行；文档还描述 safe/balanced policy、
  bearer token 和浏览器 Origin 检查。`repo-support/SKILL.md` 建议先概览、再检索、
  最后窄范围读取，以减少往返和上下文压力。
- ChatCMD 的 `fs_read_text_v2` 描述了 line/byte range、bounded streaming、
  `expectedVersion` 和 continuation；`fs_batch_read` 描述了输入顺序、逐项错误和
  aggregate output cap。预算文档定义了时间、文件、字节、输出和进程树上限。
  ADR 0020 把 direct filesystem 作为正确性来源，索引失效时回退 direct；其记录的
  1,000,000-path 结果是负面性能发现（indexed find 慢于该测试的 direct late-match，
  batch 500 也慢于 sequential），不能外推成 Local-Probe 的 benchmark。
- ChatCMD 的 `tool_catalog.rs` 从同一个 MCP router 生成排序后的工具名和 canonical
  manifest，并对结构化契约计算 hash；这是可参考的“避免手工 catalog 漂移”做法，
  不是 Local-Probe 已有功能。
- Codex Free README/ARCHITECTURE 描述 Rust + official `rmcp` 的 Streamable HTTP
  bridge；native tunnel 是独立 sidecar。其 `openai_tunnel.rs` 关键段显示了 loopback
  health URL、`/readyz`、`/metrics` 成功轮询指标、运行时 key 与 MCP bearer 的子进程
  注入，以及启动超时/子进程退出处理。文档声称会校验固定 runtime release 的
  SHA-256、使用干净环境并在 ready 后报告成功；这些均未在本轮运行验证。
- tunnel-client 文档把 tunnel metadata 管理、runtime 使用和 key 创建分开；运行时
  key 与 admin key 的用途不同，文档建议 runtime principal 具备 Tunnels Read + Use，
  管理者另需 Read + Manage。connector 文档描述 control plane poll/response、
  main MCP channel 和 POST/Streamable HTTP 行为；troubleshooting 区分 `/healthz`
  liveness、`/readyz` readiness、平台权限错误和运行时 401/403。文档中的权限/UI
  结论仍需目标账户实测。
- 官方 Go SDK README 说明 `mcp.NewStreamableHTTPHandler` 与
  `mcp.StreamableClientTransport` 的 server/client 用法；`examples/http` 是 cityTime
  工具的最小 HTTP 示例。SDK `go.mod` 要求 Go 1.25.0，因此 P05 不能把旧工具链作为
  发行承诺。

## 3. 许可证与复用决定

1. **LCA：禁止在未作许可证审查前复制。** AGPL-3.0-or-later 对网络服务和衍生作品
   有额外义务；本项目只采用“概览→搜索→窄范围/批量读取”的产品思路，不采用其
   代码、提示词、UI、品牌或生成资产。
2. **ChatCMD/Codex Free：本轮不复制代码。** 两者的 LICENSE 允许范围不同于本项目
   尚未选择的发行许可证；如果未来确需复用，必须锁定具体文件/commit，保留版权和
   LICENSE，评估依赖与衍生作品义务，并补充第三方声明。当前只参考公开契约和架构
   观察。
3. **tunnel-client：优先使用官方独立客户端。** Apache-2.0 不等于可以跳过版本、
   SHA-256、许可证和运行时权限审查；P10 才下载/验证用户选定的发行版本。本轮不嵌入
   客户端源码、不把调查快照当成发行二进制。
4. **官方 MCP Go SDK：P05 再引入。** 先固定维护版 tag/剥离 commit、Go 工具链和
   `go.sum`，采用官方 transport/API；若发布包包含其代码或 notice，按 README 的
   Apache-2.0/MIT 分层保留相应通知。本轮没有改 `go.mod` 或下载依赖到目标仓库。
5. Local-Probe 尚未由所有者选定公开许可证；来源调查不能替代该决定，也不授予上游
   项目对 Local-Probe 的任何授权。

## 4. 本轮实际核验与未执行项

### 已成功

- 目标仓库：读取计划和实施状态；建立/切换 `feat/readcore-foundation`；保留已有
  其他执行者的未提交修改。
- LCA、ChatCMD、Codex Free、tunnel-client：`git clone --filter=blob:none --no-checkout`、
  sparse-checkout、detached checkout 成功；每个 checkout 的 `HEAD` 与上表 SHA 完全
  相等，工作树状态为 clean。
- Codex Free：`git ls-tree -r --name-only <SHA>` 成功，确认 `LICENSE` 和
  `src/openai_tunnel.rs` 路径存在后读取；未因旧对话猜测路径。
- MCP Go SDK：远端 tag 查询成功；`v1.7.0` tag object 与 peeled commit 已分别用
  `git rev-parse refs/tags/v1.7.0` 和 `git rev-parse refs/tags/v1.7.0^{}` 核对；
  sparse checkout 的 HTTP 示例可读取。
- 目标仓库未跟踪 `_references/` 内容；新增/保留的 `.gitignore` 覆盖本地配置、
  credentials/secrets、runtime/logs、外部 `_references`、临时目录、bin、coverage
  和测试产物。

### 有意未执行（不是通过证据）

- 未运行 LCA 的 `npm install`、上游测试或任意安装脚本；未读取未列出的完整 server
  源码。
- 未运行 ChatCMD/Codex Free 的 `cargo test`，未运行 tunnel-client、未下载或验证
  release binary。
- 未将 MCP Go SDK 加入目标 `go.mod`，未运行 SDK 示例或 MCP Inspector。
- 未做 ChatGPT Developer Mode/App、OpenAI Tunnel、Pro 模型、workspace association
  或第二真实账号调用；P00 不产生这些能力的证据。
- 未记录任何“上游源码候选下载失败”；本轮没有需要猜测补写的文件。后续若某个文件
  或 release 下载失败，必须在对应状态记录中保留失败原文和替代方案。

## 5. 复核命令

下列命令用于复核固定来源（路径相对于 `<clone-root>` 的
父目录）；它们只读取 Git 元数据/文件，不运行上游安装脚本：

```powershell
$refRoot = Join-Path (Split-Path -Parent (Get-Location).Path) '_references'
git -C "$refRoot\lca" rev-parse HEAD
git -C "$refRoot\chatcmd" rev-parse HEAD
git -C "$refRoot\codex-free" rev-parse HEAD
git -C "$refRoot\tunnel-client" rev-parse HEAD
git -C "$refRoot\mcp-go-sdk" rev-parse refs/tags/v1.7.0
git -C "$refRoot\mcp-go-sdk" rev-parse 'refs/tags/v1.7.0^{}'
git -C "$refRoot\codex-free" ls-tree -r --name-only cb487c744e2220909548ec112d3fe2a5e26cb2f1
git -C "$refRoot\lca" status --short
git -C "$refRoot\chatcmd" status --short
git -C "$refRoot\codex-free" status --short
git -C "$refRoot\tunnel-client" status --short
git -C "$refRoot\mcp-go-sdk" status --short
git status --short --branch
git ls-files _references
```

目标仓库的最后一条 status 可能包含并行执行者的工作树修改；本任务不撤销、覆盖或
提交那些修改。本文件和 `.gitignore` 是 P00 允许的目标仓库变更；本轮不提交、不推送、
不开 PR。
