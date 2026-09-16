# Local-Probe

让 ChatGPT Web 通过原生自定义 App / MCP 高效调查被授权的本地文件和开发环境。优先级：**有效读取 > 简单环境探查 > 可靠稳定 > 文件修改**。

## 先读计划

**[总体计划与逐步骤实施手册](docs/MASTER_PLAN.zh-CN.md)** 是本项目的主要交付物，包含 P00–P14 的输入、具体文件、命令、实施步骤、依赖、测试和停止条件。它作为空仓库的第一个提交保存，早于任何代码。

[实际进度与验证记录](docs/IMPLEMENTATION_STATUS.md) · [Windows Preview 使用说明](docs/PREVIEW.zh-CN.md) · [下一阶段执行路线](docs/NEXT_PHASE_PLAN.zh-CN.md) · [接口与边界](docs/TOOL_CONTRACTS.md) · [R4 Windows network deny 设计](docs/R4_WINDOWS_NETWORK_DENY.md) · [威胁模型](docs/THREAT_MODEL.md) · [兼容性预检](docs/COMPATIBILITY.md) · [上游来源与复用决定](docs/UPSTREAM_REVIEW.md)

## 当前状态：内核与本地 MCP 增量，以及 Windows GUI/tray Preview

当前实现了兼容旧输入的有界字节读取、`bytes|lines|tail` 范围读取、确定性批量预算、输入顺序保持、部分失败、UTF-8 安全续读、版本变化检测及本机命令行演示。P04 已有配置/策略校验、`policy.BoundScope`、基于 Go `os.Root` 的只读 rootfs、绑定到 `readcore` 的 Source adapter，以及 Windows 本轮和历史 WSL2 的有界特殊文件、链接边界与 loopback SMB 验证；P05 已完成官方 SDK 的认证 loopback MCP listener、Cloudflare Access/Tunnel 真实链路和 `server_info`、`ping`、`read_file`、`batch_read`；P06 的目录发现、文件查找、literal 搜索和 tree 分页也已由 ChatGPT Business“极高”验证。R1 的 direct-search 第一增量与 audit.v1、R3 的无进程环境发现、R2 的 `workspace_snapshot` 已接入本地 MCP；R2 新工具和范围读取目前有本地 Windows 集成测试，尚未做真实 Business 复测。

本轮还提供 Windows 原生 GUI/tray Preview：只读展示总览、连接配置投影、开发者规则、经过筛选的日志/诊断和 About，并可导出有界、二次脱敏且不覆盖已有文件的诊断 JSON。它是本地观察器，不是生产控制面；`Start`、`Stop`、`Reconnect` 固定为 `production_gate`，不会管理 MCP/Tunnel 进程，也不编辑配置、展示凭据或新增网络监听。详见 [`docs/PREVIEW.zh-CN.md`](docs/PREVIEW.zh-CN.md)。

rootfs 已对绝对/相对 root、普通文件、逐组件 symlink/reparse 拒绝、Scope 撤权、hardlink 和元数据 identity 做有界实现与平台测试；这不是完整文件系统隔离或独立安全审查。本轮 WSL Ubuntu-22.04 未安装 Go，未运行 Linux runtime 测试；Windows 本轮验证和历史 WSL2 记录均不能替代独立安全审查。本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台、全量 TOCTOU 和生产发布仍是残余风险。演示程序仅打开本地操作者明确指定的文件，不能改成接收远程路径后直接部署。

## 运行已有代码

使用受支持的 Go 版本。当前 `go.mod` 要求 Go 1.25.0；Windows 本机验证使用 Go 1.26.0。该版本要求服务于 `os.Root` rootfs 和后续官方 SDK 集成，不代表产品已经完成发布工具链、真实认证或 MCP 链路。

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./...
go run ./cmd/readcore-demo -file ./README.md -offset 0 -max-bytes 4096
```

### Windows Preview

在 PowerShell 中构建并启动：

```powershell
.\scripts\Build-LocalProbePreview.ps1
.\scripts\Start-LocalProbePreview.ps1
```

也可以一次构建并启动：

```powershell
.\scripts\Start-LocalProbePreview.ps1 -Build
```

默认读取仓库 `.runtime\local-probe.json`，审计目录默认为 `%APPDATA%\Local-Probe\audit`；两者都可用脚本参数覆盖。关闭窗口只会隐藏到托盘，托盘 `Exit` 只退出 Preview，不会停止 MCP 或 Tunnel。该版本保持只读与 `production_gate` 边界，完整用法和限制见 [`docs/PREVIEW.zh-CN.md`](docs/PREVIEW.zh-CN.md)。

演示返回结构化 JSON。`next_offset` 是下次读取的字节位置；续读时同时传入返回的 `version.token`：

```sh
go run ./cmd/readcore-demo -file ./README.md -offset 4096 -max-bytes 4096 -expected-version TOKEN_FROM_PREVIOUS_RESULT
```

上面的 offset 只是参数示例，实际必须使用前一结果的 `next_offset`，不能猜测 UTF-8 边界。版本是弱元数据版本，不能用于证明强快照一致性。

## 目标架构

```text
ChatGPT 原生自定义 App
       -> MCP / 官方 Secure MCP Tunnel
       -> 认证 ingress -> connection/profile/root 策略
       -> 高层概览、检索、批量读取、有限环境探查
       -> 本机文件系统 / Git
```

多账号需求落实为多个受控 connection/ingress，不保存 ChatGPT 密码或 Cookie，也不把 tunnel ID 当作身份认证。原生 Tunnel 的可用性要在真实目标账户上预检，不承诺所有订阅和所有模型均可调用。

## 接续开发

下一条关键路径见 [`docs/NEXT_PHASE_PLAN.zh-CN.md`](docs/NEXT_PHASE_PLAN.zh-CN.md)：先按 [`manual-test-targets/r4/README.md`](manual-test-targets/r4/README.md) 完成 Windows 本地负例与固定映像边界复核，再补齐 network deny、profile→probe 接线、执行期 SHA256 和独立审查；在这些硬门完成前不接入 MCP `run_probe`。Windows Preview 已作为只读载体实现，但 R6 生产 runtime/控制链、R7 index 默认启用收益和 R9 发布审查仍按依赖推进。当前仍不开放 raw command、Shell、任意 args/env/cwd/timeout 或写入能力；真实 Business 调用通过不等于所有账号、Pro、长时间运行或生产发布已通过。audit 运行时故障 fail-closed、全局配额和部分生产事件来源仍未实现。

本仓库暂未选择对外发布许可证。当前代码是本项目独立实现，未复制 LCA、ChatCMD 或 Codex Free 源码；任何后续复用必须先审核上游许可并保留必要通知。
