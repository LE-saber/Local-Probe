# R4 Windows 本地手测说明

本目录只保存手测说明，不包含秘密、脚本、包装器或可执行文件。R4 当前可以在
Windows 本地复核固定探针和 developer-mode 的安全边界；它还没有接入远程 MCP，
因此本说明不要求也不提供 `run_probe`、任意命令或 Cloudflare Tunnel 测试。

## 测试范围

本轮只测 Windows。本地核心已经包含以下保护：

- 固定的 `git`、`python`、`node` tool ID，以及固定 `version_probe` profile 形状；
- Windows 本地绝对 PE 路径检查，拒绝 UNC/device/ADS、路径别名、reparse/symlink、
  非普通文件和 wrapper/脚本；
- 执行前保留最终映像与父目录句柄，使用挂起创建并在恢复前复核实际映像；
- 单进程 Job、kill-on-close、无窗口、私有 cwd/环境、输出上限、超时和进程回收；
- developer mode 默认关闭、profile allowlist，以及绑定 connection/profile/revision/
  command/variant/request nonce 的短期一次性本地确认。

以下项目仍未完成，不能把本地核心当作可远程执行能力：

- 没有已接入操作系统的 network deny 执行器；
- 配置 profile 尚未安全接入 `probe.AuditExecutable`/`ToolVersion` 的生产执行路径；
- `identity.sha256` 当前只有格式校验，没有执行期摘要或签名比较；
- `internal/mcpserver` 没有注册 `run_probe`，远程命令、参数、环境、cwd、timeout 和
  写入能力均保持关闭。

## 前置条件

在仓库根目录打开 PowerShell，确认使用受支持的 Go 版本（`go.mod` 要求 Go 1.25.0；
本机 Windows 验证使用 Go 1.26.0）：

```powershell
go version
git status --short
```

不要在测试配置中填写 token、JWT、API key、真实凭据或敏感项目路径。不要把
`where.exe`、`codex`、Shell、`.cmd`、`.bat`、`.ps1`、wrapper 或任意第三方工具作为
测试目标；本地核心测试使用临时目录和测试夹具即可。

## 推荐测试顺序

1. 运行固定探针和 Windows 进程边界测试：

   ```powershell
   go test -count=1 ./internal/probe
   ```

   预期覆盖 PE/路径别名/reparse、句柄 identity、挂起映像复核、输出上限、超时、
   进程树终止和非 Windows 执行路径的 fail-closed 约束。若要查看具体测试名，可先
   执行 `go test -list . ./internal/probe`；不要因此改变测试目标。

2. 运行 profile、一次性确认和配置解析测试：

   ```powershell
   go test -count=1 ./internal/commandprofile ./internal/confirmation ./internal/config
   ```

   预期确认 developer mode 默认关闭、非 allowlist connection 被拒绝、`network=deny`
   在没有 opaque enforcement capability 时拒绝，以及确认 token 不能重放、篡改或跨
   profile/request 使用。

3. 运行 Windows race 检查：

   ```powershell
   go test -race -count=1 ./internal/probe ./internal/commandprofile ./internal/confirmation
   ```

4. 运行静态检查：

   ```powershell
   go vet ./internal/probe ./internal/commandprofile ./internal/confirmation ./internal/config
   ```

5. 验证远程 MCP 仍然是只读面：

   ```powershell
   go test -count=1 ./internal/mcpserver
   ```

   `tools/list` 应继续只出现 `README.md` 和 `docs/TOOL_CONTRACTS.md` 第七节列出的只读工具，
   不应出现 `run_probe`。测试输出或手工检查中若看到远程 command/profile/confirmation
   输入，说明接线越过了本阶段的安全边界，应停止并回退到审查。

## 手工负例清单

手工检查配置或本地调用路径时，应验证下列输入被拒绝，并且错误不包含绝对路径、文件
内容、完整 argv、环境变量或秘密：

- developer mode 关闭时请求任意 version probe；
- connection/profile 不在 allowlist，或 profile revision、variant、request nonce 不匹配；
- executable 是 UNC/device/ADS/路径别名、reparse/symlink、非 PE、脚本或 wrapper；
- 试图通过 JSON 直接提交 `confirmed: true`、`EnforcementCapability`、SHA256 以外的
  任意字段，或把 `network=deny` 当成已经完成的系统隔离；
- 试图在 MCP 中调用不存在的 `run_probe` 或传递 command、args、env、cwd、timeout。

这些负例不应通过真实 `codex`、shell 或联网程序验证；只用单元测试、配置解析和
`tools/list` 的本地结果确认拒绝行为。

## 结果与清理

通过标准是上述 Windows 测试和静态检查成功，且 MCP 工具列表没有 `run_probe`。测试应
自行清理临时夹具；不要把临时配置、日志、token、JWT 或可执行文件提交到本目录。

本说明不宣称 Linux、其它 Unix、真实账号、Cloudflare Tunnel、长期运行或生产发布已
验证；这些项目需要后续独立的 network deny、profile→probe、执行期 SHA256/签名和
安全审查完成后再安排。
