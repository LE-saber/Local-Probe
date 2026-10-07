# Local-Probe 开发指南

本文面向维护者和贡献者，补充根 README 的部署说明。它描述当前 Preview 分支的实际开发入口，不代表所有规划阶段已经实现。

## 开发环境

- Windows Preview 的主要验证平台是 Windows x64。
- `go.mod` 要求 Go 1.25.0；CI 使用固定的 Go 1.26.5，开发机建议使用 Go 1.26.x。
- 使用 Git、PowerShell 5.1/7；Go 模块首次构建需要网络或已有模块缓存。
- 第三方 Tunnel 程序和秘密只放在仓库外，不复制到 Git 工作树。

先确认工作树和工具链：

```powershell
git status --short --branch
go version
git --version
pwsh --version
```

Windows 本地环境检查：

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Test-LocalProbeEnvironment.ps1 `
  -RepoRoot (Get-Location).Path
```

## 代码地图

| 目录 | 责任 |
| --- | --- |
| `cmd/local-probe-mcp` | 固定参数的本地 MCP HTTP 入口；只绑定 loopback |
| `cmd/local-probe-preview` | Windows Preview 进程组合；不接受模型提供的任意可执行路径 |
| `cmd/readcore-demo` | 本地 byte-range 内核演示 |
| `internal/config` | JSON 配置解析、校验、revision 和原子 FileStore |
| `internal/policy`、`internal/admission` | connection/profile/root/tool 授权与 admission 边界 |
| `internal/rootfs`、`internal/readcore` | 只读 rootfs、路径边界和有界范围读取 |
| `internal/search`、`internal/catalog` | 有界目录发现、查找、literal 搜索、tree 和候选 catalog |
| `internal/mcpserver`、`internal/cfaccess` | MCP 工具契约、本地 bearer 和 Cloudflare Access JWT ingress |
| `internal/previewconnect` | Preview 固定的 MCP + Cloudflare/OpenAI Tunnel 生命周期控制器 |
| `internal/previewapp`、`internal/previewui` | Preview 数据模型、Windows GUI、托盘、工作空间和诊断导出 |
| `internal/audit`、`internal/auditreader`、`internal/supportbundle` | 结构化审计、有界读取、脱敏和 support bundle 边界 |
| `internal/runtimeowner`、`internal/supervisor`、`internal/connectionmanager` | R6 非生产生命周期契约和 fake automation core；不能当作生产 supervisor |
| `internal/releasecheck`、`scripts/acceptance.ps1` | 本地证据 gate；当前 release-ready 仍为 false |

更完整的静态代码地图见 [`.planning/codebase/STRUCTURE.md`](../.planning/codebase/STRUCTURE.md)、[架构说明](ARCHITECTURE.zh-CN.md) 和 [接口契约](TOOL_CONTRACTS.md)。

## 常用命令

```powershell
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
```

Windows Preview 构建两个稳定产物：

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Build-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path
```

构建输出位于被忽略的 `bin/`；不要把它们作为源码文件强行加入 Git。一次性构建并启动 GUI：

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Start-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path -Build
```

脚本入口和参数以 `Get-Help .\scripts\<name>.ps1 -Full` 为准。`Setup-LocalProbePreview.ps1` 负责新用户布局，`Test-LocalProbeEnvironment.ps1` 负责安全预检，`Package-LocalProbePreview.ps1` 只应生成不含秘密的 Preview 归档。

## 两条 Tunnel 链路的开发边界

Cloudflare Preview 使用 `127.0.0.1:8788`、Access JWT 和 `cloudflared.exe`；OpenAI Secure MCP Tunnel 使用 `127.0.0.1:8787`、本地 hop token 和官方 `tunnel-client.exe`。Preview controller 按 `-transport` 只选择其中一条并只管理自己启动的两个子进程；OpenAI 独立 PowerShell 脚本仍保留。两套入口不共享身份，不要为了让测试通过而关闭 Host、loopback、Access 或 token 校验。

相关文档：

- [Preview 部署](DEPLOYMENT_PREVIEW.zh-CN.md)
- [Preview 能力与限制](PREVIEW.zh-CN.md)
- [Cloudflare Tunnel + Access](CLOUDFLARE_TUNNEL_SETUP.zh-CN.md)
- [OpenAI Secure MCP Tunnel](TUNNEL_SETUP.zh-CN.md)
- [威胁模型](THREAT_MODEL.md)
- [R9 发布检查](R9_RELEASE_CHECKLIST.zh-CN.md)

## 变更规则

1. 先更新计划、状态或契约，再改变跨模块行为；不要把未来设计写成当前能力。
2. 任何远程可调用工具必须继续经过 connection/profile/root/tool 绑定和全局预算；不得添加 raw shell、任意 argv/env/cwd、任意写入或秘密回显。
3. 文件访问必须沿用 rootfs 的实际打开和 identity 校验；词法路径检查不是隔离。
4. GUI 只接受固定配置和固定生命周期动作；不要把它变成通用进程启动器。
5. 日志、诊断、support bundle 只允许稳定 allowlist 字段，禁止 token、JWT、key、Cookie、完整路径、文件内容、argv、env、stdout/stderr。
6. 对 Windows 路径、reparse/symlink、网络盘、长路径和 Unicode 行为补充有界测试；不要为了测试方便放宽生产边界。

## 提交前检查

```powershell
git diff --check
git status --short --ignored
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Test-LocalProbePreviewPackaging.ps1 `
  -RepoRoot (Get-Location).Path
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\acceptance.ps1 -Quick
```

若本轮涉及完整发布候选，还要执行非 Quick acceptance、Go race/vet、干净目录构建和实际 Windows Preview 手工检查。只完成本地测试不能证明 ChatGPT Workspace、Cloudflare Access、OpenAI Tunnel 或网页端端到端成功。

## 证据和发布纪律

`docs/IMPLEMENTATION_STATUS.md` 记录已实现与未完成项；`docs/evidence/` 只保存不含凭据的机器可读证据。每条验证记录应包含实际命令、环境、限制和时间，不要把“脚本存在”写成“脚本已成功运行”。

本仓库自有源码采用 MIT（见根目录 LICENSE）；第三方代码保留各自许可证，发布前须复核打包生成的 NOTICE。Preview 源码上传、Preview ZIP 和生产发布是三个不同的交付动作。
