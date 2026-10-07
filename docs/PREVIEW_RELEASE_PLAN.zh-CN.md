# Windows Preview 仓库整理与发布计划

本文档是当前 Windows Preview 上传 GitHub 前的执行计划。它只描述本轮要整理、验证和发布的内容，不把尚未完成的生产能力包装成已完成。

## 1. 环境与代码基线

探查日期：2026-09-19。

| 项目 | 当前基线 |
| --- | --- |
| 系统 | Windows x64（NT 10.0.26200.0） |
| PowerShell | 7.6.5 |
| Go | 1.26.0 windows/amd64 |
| Git | 2.46.2.windows.1 |
| cloudflared | 2026.8.2 |
| Git 分支 | `feat/cloudflare-mcp-ingress` |
| 探查基线提交 | `dd17589`（代码结构映射） |

代码结构、依赖、测试和风险的详细只读映射位于 `.planning/codebase/`。

## 2. 本轮发布目标

1. Git 仓库只保留构建、运行和维护 Preview 所需的源码、脚本、示例、测试夹具和文档。
2. `.runtime`、`bin`、`tmp`、凭据和第三方可执行文件不进入源码提交。
3. 新用户从干净 clone 开始，能够根据根 README 完成环境检查、初始化、构建、配置和启动。
4. Preview GUI、MCP origin 和 Cloudflare Tunnel 的实际边界在文档中一致，不沿用过时描述。
5. 所有上传内容通过秘密扫描、格式检查、Go 测试、race、vet、构建和干净目录部署演练。

本轮是 **Preview 源码发布**，不是生产发布授权。不会声称 R9 release gate、代码签名、安装器、自动更新或独立安全审查已经完成。

## 3. 文件分类与目标布局

### 3.1 提交到 GitHub

- `cmd/`：MCP、Windows Preview 和本地 demo 入口。
- `internal/`：有效实现与测试。
- `configs/`：不含秘密、无本机绝对路径的示例配置。
- `scripts/`：环境检查、初始化、构建、启动和验收入口。
- `manual-test-targets/`：小型、确定性、无秘密的测试夹具。
- `docs/`：用户、开发、架构、安全和历史状态文档。
- `.github/workflows/`、`.planning/codebase/`、根配置和 Go 模块文件。

### 3.2 只保留在本机，不提交

- `repo/.runtime/`：本机运行配置、日志和临时状态。
- `repo/bin/`、`repo/dist/`、`repo/tmp/`：构建和测试产物。
- `Local-Probe/.secrets/`：Tunnel token 等凭据。
- 第三方可执行文件和参考仓库：不得混入源码提交。

### 3.3 本地遗留整理

仓库父目录存在早期误写出的 `cmd/`、`internal/`、`manual-test-targets/`、`path_kind.go` 和根级 `MASTER_PLAN.zh-CN.md`。它们不是当前 Git 工作树，且仓库内对应实现更新。本轮先把这些明确遗留项移动到一个带日期的外部归档目录，验证仓库与 Preview 不依赖它们后再由维护者决定是否永久删除。

`_references/`、`.obsidian/` 不是发行内容；`.secrets/` 和当前 `_tools/` 是本机状态。它们不上传，也不在本轮擅自删除。

目标源码布局保持简单：

```text
Local-Probe/
├── cmd/
├── internal/
├── configs/
├── scripts/
├── manual-test-targets/
├── docs/
├── .github/workflows/
├── .planning/codebase/
├── README.md
├── AGENTS.md
├── go.mod
└── go.sum
```

## 4. 实施顺序

### 阶段 A：可移植部署入口

- 增加 Windows 环境检查脚本，报告 Go、Git、PowerShell、cloudflared、运行配置和凭据的状态，但不读取或显示凭据值。
- 增加统一 Preview 初始化入口：创建本地运行布局、生成安全占位配置，并引导用户放置 Tunnel token。
- 去除示例和面向用户文档中的当前机器绝对路径。
- 为第三方 cloudflared 提供明确、可验证的安装位置和安装方法；不把第三方 EXE 提交到源码分支。

### 阶段 B：文档重构

- 重写根 `README.md`，首页只保留产品能力、5–10 分钟快速开始、架构概览、限制和文档索引。
- 更新 `docs/PREVIEW.zh-CN.md` 与当前一键连接、工作空间管理、托盘和诊断能力一致。
- 新增开发指南和架构说明；历史计划、阶段证据和旧 Tunnel 路线保留为维护资料，但不作为新用户首要入口。
- 更新 `docs/IMPLEMENTATION_STATUS.md`，明确本轮 Preview 发布范围和仍未实现项。

### 阶段 C：本地目录整理

- 先逐项核对父目录遗留文件与仓库最新版。
- 只移动已确认的旧副本到外部归档；不移动 `.secrets`，不读取秘密内容。
- 清理由构建可再生的仓库内 `.runtime/bin/tmp` 产物前，先完成一次验证并记录重新生成命令。

### 阶段 D：验证与上传

- `git diff --check`
- 跟踪文件秘密/本机绝对路径扫描
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- Windows Preview 构建与脚本契约测试
- 从干净 Git 导出目录执行环境检查、初始化和构建演练
- 检查 GitHub 远端分支是否分叉，提交并推送当前功能分支

## 5. 发布停止条件

出现以下任一情况时不推送发布整理提交：

- 跟踪文件包含 token、key、Cookie、JWT、私有路径内容或真实凭据。
- 干净 clone 仍依赖维护者机器上的绝对路径。
- Preview 无法从源码构建，或核心测试/race/vet 失败。
- 文档声称了实际未启用的命令执行、写入、生产 supervisor 或 release-ready 状态。
- 本地整理目标无法确认是旧副本，可能包含尚未合并的有效工作。

## 6. GitHub 交付形式

本轮先把完整可构建源码和部署文档推送到 `feat/cloudflare-mcp-ingress`，不直接合并默认分支。构建生成的 EXE 不提交到源码历史；若需要分发二进制，应在后续单独创建带校验值的 GitHub Preview Release，并明确它不是生产签名安装包。
