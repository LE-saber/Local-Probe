# Local-Probe Windows Preview

这是 Local-Probe 的 **Windows 本地 GUI/tray 预览版**。它把已经实现的配置投影、开发者规则投影和有界审计读取能力组合成桌面观察器，方便查看当前状态和导出脱敏诊断。它不是生产版本，也不是 MCP/Tunnel 的进程控制器。

## 构建与启动

要求：Windows、仓库要求的 Go 工具链，以及可执行 PowerShell 脚本的本地环境。

从仓库根目录运行：

```powershell
.\scripts\Build-LocalProbePreview.ps1
.\scripts\Start-LocalProbePreview.ps1
```

默认产物是：

```text
bin\local-probe-preview.exe
```

也可一次完成构建与启动：

```powershell
.\scripts\Start-LocalProbePreview.ps1 -Build
```

指定配置、审计目录或预览程序路径：

```powershell
.\scripts\Start-LocalProbePreview.ps1 `
  -ConfigPath D:\path\to\local-probe.json `
  -AuditDir D:\path\to\audit `
  -ExecutablePath .\bin\local-probe-preview.exe
```

脚本默认读取：

- 配置：`<仓库根目录>\.runtime\local-probe.json`
- 审计目录：`%APPDATA%\Local-Probe\audit`

直接运行 `bin\local-probe-preview.exe` 时，程序也会按其安装位置推导仓库内的 `.runtime\local-probe.json`，审计目录仍使用当前用户的 `%APPDATA%\Local-Probe\audit`。缺少配置或审计目录时，Preview 显示明确的 `unconfigured` / `unavailable` 状态，不会替用户创建配置、启动服务或猜测运行状态。

## 当前界面能力

窗口包含以下只读页面：

- **Overview**：配置 revision、root/profile/connection 数量、审计摘要，以及可用时的连接状态摘要。
- **Connections**：连接、profile、transport 和是否配置 Tunnel 的脱敏投影；没有受信运行时状态源时显示 unavailable。
- **Developer Rules**：开发者模式、允许的 connection 和固定规则/variant/slot 摘要；此处不能编辑或启用规则。
- **Logs / Diagnostics**：从审计文件读取并筛选后的结构化事件与诊断摘要。
- **About**：版本、Preview 边界和运行说明。

窗口底部提供刷新和诊断导出。`Start`、`Stop`、`Reconnect` 会显示但保持禁用，原因固定为 `production_gate`；当前版本不会据此启动、停止或重连 MCP/Tunnel。

窗口顶部的“字号”按钮可在 100%、125%、150%、175% 和 200% 之间循环切换，默认使用 150%。该设置只影响当前 Preview 进程中的界面绘制，不会修改配置或影响 MCP/Tunnel。

## 托盘与生命周期

- Preview 使用单实例保护；同一用户会话中已有实例时，不再启动第二个实例。
- 点击窗口关闭按钮只会把窗口隐藏到系统托盘。
- 托盘菜单提供打开窗口、刷新、导出诊断、About 和 Exit。
- `Exit` 只退出 Preview 自身，不停止、不重启，也不接管现有 MCP/Tunnel。

因此，现有 MCP/Tunnel 仍由原来的脚本或服务生命周期负责。Preview 退出不代表连接被关闭；同样，Preview 显示 unavailable 也不能单独证明 MCP/Tunnel 已停止。

## 日志与诊断导出边界

默认审计读取器只选择 `audit.jsonl` 和符合固定命名规则的轮转文件，并拒绝 symlink/reparse 和非普通文件。默认读取上限是 16 个文件、512 KiB、8192 行、128 条记录，单行上限 64 KiB；达到上限会显示 bounded/truncated，而不是继续无界扫描。

展示和导出的字段采用 allowlist，不包含原始配置、文件内容、命令行、环境变量、凭据、Tunnel ID、网络端点或本机路径。导出前还会在统一 support bundle 边界执行第二次结构校验与脱敏检查：字段、数组项、文本和总 JSON 大小都受限，出现敏感标记或路径形态时导出失败。Preview 导出的 JSON 最大为 64 KiB。

保存诊断时只接受新的 `.json` 目标；已有文件不会被覆盖，symlink/reparse 或非普通目标也会被拒绝。导出结果适合先由本机操作者人工复核，再决定是否对外提供；它不是自动上传功能。

## 明确未实现

当前 Windows Preview 不包含：

- MCP/Tunnel 的生产级启动、停止、重连或进程 ownership；
- 配置、开发者规则或命令放行规则的 GUI 编辑；
- 凭据、token、key、Cookie、原始命令输出或文件内容展示；
- HTTP、loopback 管理站点、named pipe broker 或任何额外网络监听；
- 安装器、自动更新、代码签名、正式发布包或生产就绪声明；
- Linux/macOS GUI。

这些边界是 Preview 的真实能力范围。现有已经开发好的 MCP、文件发现/读取和本地契约继续独立工作；本轮没有为了桌面界面补做此前尚未实现的命令执行、索引或写入功能。
