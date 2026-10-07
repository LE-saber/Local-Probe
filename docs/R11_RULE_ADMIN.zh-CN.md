# R11 第一增量：本地文件规则管理

日期：2026-10-06。这是可以使用和独立测试的**离线规则管理 CLI**，不是七项能力已完成的后端正式版本。GUI、MCP 工具清单和命令执行入口没有改变。

入口：`bin/local-probe-admin-r11-rules-dev.exe`。源码：`cmd/local-probe-admin`、`internal/workspaceadmin/rules.go`。只有明确选择的配置文件会被操作，不搜索或自动打开现有 `.runtime`、凭据或 agent 配置。

## 使用前提

只操作用户明确指定、位于可信私有目录的配置文件。FileStore 的祖先路径重解析、硬链接、ACL 防护仍未通过生产硬门；该程序不宣称解决这些问题，也不是管理员服务。

保存规则前，用户须停止使用同一配置的所有 MCP/Tunnel/GUI 运行实例。`-offline` 是用户对停止状态的明确声明，不是程序检测或自动停止的证明。运行中的旧进程尚不会热加载这些修改；离线保存后重新启动时，现有 policy/rootfs 会使用新规则。本轮未自动停止用户进程，也未接入 GUI 保存规则。

## 查看、预览、确认保存

PowerShell 示例，先将路径换成自己的明确配置路径：

```powershell
$admin = (Resolve-Path '.\bin\local-probe-admin-r11-rules-dev.exe').Path
$configFile = 'D:\YourPrivateDirectory\local-probe.json'
& $admin -config $configFile -connection-id chatgpt-local -action show
```

记录返回的 `revision`。下面的 JSON 通过标准输入传递，不会执行其中的内容。未提供 `root_id` 时编辑当前连接实际引用的 profile；提供时只能编辑该 profile 登记的 root。未给出的规则列表保持原样；显式 `[]` 清空对应列表。

```powershell
$request = @'
{
  "update": {
    "root_id": "project",
    "deny_patterns": [".env", "*.key", "private/**"],
    "ignore_patterns": ["vendor/**", "build/**"]
  },
  "samples": [
    {"root_id": "project", "path": "private/token.txt"},
    {"root_id": "project", "path": "src/main.go"},
    {"root_id": "project", "path": "build/log.txt"}
  ]
}
'@
$revision = '用 show 返回的 revision 替换'
$request | & $admin -config $configFile -connection-id chatgpt-local -action preview -revision $revision -request -
```

检查 `affected_connections`、规则和 `decisions`。deny 是硬拒绝；ignore 仅排除发现/搜索，直接读取仍可能允许。预览只是规范相对路径的策略匹配，不检查文件存在，不扫描目录，不证明文件系统隔离。

确定全部相关运行实例已停止、人工同意这次修改后，使用**同一 JSON、同一 revision 和完整影响列表**：

```powershell
$request | & $admin -config $configFile -connection-id chatgpt-local -action apply -revision $revision -request - -offline -approve -ack-connections 'chatgpt-local'
```

共享 root/profile 可能要求 `-ack-connections 'chatgpt-local,other-connection'`。顺序可以不同，但不能缺项、多项或重复；配置变化必须重新查看和预览。保存后再次 `show` 核对新 revision，重新启动运行实例，再验证实际访问拒绝。

也可用 `-request 'D:\YourPrivateDirectory\rules.json'` 读取普通 JSON 文件。禁止未知/重复字段、大小写替代字段和多个 JSON 对象。请求上限 128 KiB；deny/ignore 合计最多 256 条、64 KiB；预览最多 128 个样本。合法 glob 语义仍沿用现有 policy，没有新增 regex 或执行型规则。

## 实现和验证边界

- 已实现按连接投影、root/profile deny/ignore 编辑、校验、实际 policy 预览、共享影响确认、配置 CAS 和持久化。暂停 root 和禁用连接仍列入共享影响，防止以后恢复时静默改变授权。
- Win32 named mutex 现在固定持有者 Go goroutine 的 OS 线程，防止线程迁移导致错误释放或错误重入。测试包含两个真实 Windows 进程竞争同一配置，结果为一个成功、一个 revision 冲突。
- CLI 子进程测试覆盖查看、修改、重新加载、旧 revision 拒绝；包测试覆盖本地拒绝/忽略策略、生效读取、保留其它配置字段、资源限制、取消和多线程竞争。
- 未实现 write_allow、任务执行范围、在线撤权/热加载、工作文件写入、脚本/build-test、多连接 supervisor 生产接线。不能用此 CLI 代替这些能力或把当前测试通过解释为完整 R11 验收。
- 命令、脚本和构建执行仍需真实受限 worker 与网络/文件隔离验证。临时 worker 身份/临时 WFP 测试规则尚待用户明确授权；常驻服务安装仍需另行确认。

完整计划见 [R11 后端优先计划](R11_BACKEND_FIRST_PLAN.zh-CN.md)。具体命令和本轮结果见 [第一增量验证记录](R11_BACKEND_PROGRESS.zh-CN.md)。
