# Local-Probe

让 ChatGPT Web 通过原生自定义 App / MCP 高效调查被授权的本地文件和开发环境。优先级：**有效读取 > 简单环境探查 > 可靠稳定 > 文件修改**。

## 先读计划

**[总体计划与逐步骤实施手册](docs/MASTER_PLAN.zh-CN.md)** 是本项目的主要交付物，包含 P00–P14 的输入、具体文件、命令、实施步骤、依赖、测试和停止条件。它作为空仓库的第一个提交保存，早于任何代码。

[实际进度与验证记录](docs/IMPLEMENTATION_STATUS.md) · [接口与边界](docs/TOOL_CONTRACTS.md) · [威胁模型](docs/THREAT_MODEL.md) · [兼容性预检](docs/COMPATIBILITY.md) · [上游来源与复用决定](docs/UPSTREAM_REVIEW.md)

## 当前状态：K0 内核原型，不是可连接 ChatGPT 的产品

当前实现了有界字节范围读取、确定性批量预算、输入顺序保持、部分失败、UTF-8 安全续读、版本变化检测及本机命令行演示。没有 MCP listener、生产级路径打开器、账号认证、Tunnel supervisor、目录搜索、行范围接口或文件修改功能。

`Source` 是尚需安全实现的受信任边界，词法路径检查不等于文件系统隔离。演示程序仅打开本地操作者明确指定的文件，不能改成接收远程路径后直接部署。

## 运行已有代码

使用受支持的 Go 版本。当前 `go.mod` 的 1.23 是离线算法原型的最低编译要求，不是产品发布工具链承诺；加入生产 root adapter/MCP 前须按计划升级并锁定工具链和 SDK。

```sh
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/readcore-demo -file ./README.md -offset 0 -max-bytes 4096
```

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

下一条关键路径为计划 P01 的真实连接预检，以及 P04/P05 的安全 root adapter + 官方 MCP SDK。搜索与概览随后接入；多连接与故障恢复按 P09/P10 实现。索引是否引入取决于对照基准，不以工具数或代码量判断完成度。

本仓库暂未选择对外发布许可证。当前代码是本项目独立实现，未复制 LCA、ChatCMD 或 Codex Free 源码；任何后续复用必须先审核上游许可并保留必要通知。
