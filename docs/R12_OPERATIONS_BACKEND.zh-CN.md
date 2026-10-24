# R12 受信任操作后端开发版

日期：2026-10-24。当前为独立 CLI/MCP 后端增量；没有替换既有 GUI，没有更改用户连接或授权，没有重新连接公网 Relay。

## 已实现

Windows 目标可接收配对 Agent 的命令、脚本、版本、build/test、文本文件创建/更新和精确文本补丁申请。目标所有者通过本地 CLI 检查完整计划并逐次批准，MCP 没有批准接口，`confirmed=true` 无效。

作业持久化，支持主体/连接隔离、幂等、取消、分页输出、审批失效、运行租约及重启后不自动重跑。两个连接可同时各运行一个作业，取消其中一个不影响另一个。Linux Agent 已准备构建；Linux 目标目前保持只读，启用操作参数会明确拒绝，不能称为 Linux 执行功能已经交付。

## 执行契约与准入

`trusted_host` 使用当前 OS 用户实际的文件和网络权限，**不是沙箱**。运行器可以访问工作目录之外的当前用户文件，也可能访问该用户的身份/作业存储。命令按结构化 executable/argv 启动；脚本正文冻结到私有文件后交给明确的运行器，不拼接远程 shell 命令。

需同时满足以下条件，没有自动升级旧配置：

1. `local-probe.config.v2` 中显式可写 profile：`read_only=false`。v1 继续拒绝写权限，默认 profile 仍只读，文件写入核心也独立检查。
2. 配对连接选择 `purpose=agent_remote`、`execution_policy=trusted_host`。
3. `developer_mode.enabled=true`、`allowed_connections` 包含连接，profile.tools 包含所需工具。
4. 目标启动时显式配置本地运行器目录表、位于所有授权根之外的私有作业目录。
5. 本地所有者逐次批准精确计划摘要；运行前复核主体、配置版本、执行映像身份及 SHA-256。

配置片段（并非完整文件，根和凭据等字段须保留）：

```json
{
  "profiles": [{
    "id": "developer", "roots": ["workspace"], "read_only": false,
    "tools": ["server_info", "read_file", "probe_tools",
      "prepare_operation", "submit_operation", "operation_status",
      "operation_output", "cancel_operation",
      "prepare_file_change", "commit_file_change"]
  }],
  "connections": [{
    "id": "agent-remote", "label": "Agent", "enabled": true,
    "profile_id": "developer", "credential_ref": "agent-credential",
    "purpose": "agent_remote", "execution_policy": "trusted_host"
  }],
  "developer_mode": {
    "enabled": true, "allowed_connections": ["agent-remote"],
    "default_confirmation": "per_call", "network_default": "deny"
  }
}
```

`network_default=deny` 保留既有隔离配置的默认值；明确选择的 `trusted_host` 是独立执行契约，不提供网络拒绝保证，不能把此字段当作网络隔离证据。旧 GUI 和旧隔离门保持原行为。当前操作适配器仅接入配对目标，Web/桌面高级入口尚未接线。

## 运行器与启动

[空目录表示例](../deploy/operations/catalog.example.json)不自动授权任何程序。条目使用所有者核对的物理安装路径、SHA-256 和必要的环境变量：

```json
{
  "version": "local-probe.operations.v1",
  "tools": [{
    "id": "python", "executable": "C:\\TrustedTools\\python.exe",
    "sha256": "<本机实际映像的64位小写SHA-256>",
    "script_type": "python", "environment": ["SystemRoot=C:\\Windows"]
  }]
}
```

script_type 支持 python/node/powershell/cmd；普通命令可省略。映像更新后哈希不符会拒绝，维护者需重新核对。默认不继承完整宿主环境、PATH 或令牌；build/test 如需 PATH/GOROOT 等，须明确配置。TEMP/TMP 使用每个作业的私有临时目录。

当前 cmd 脚本不接受额外 args，作业目录不能包含空格或 cmd 元字符；输入应直接写入并批准脚本正文。不满足时在准备阶段拒绝，避免将 Windows CRT 参数转义误当成 cmd shell 转义。Python/Node/PowerShell 不受此 cmd 限制。

私有作业目录不得位于授权根内，也不得包含授权根。Windows 创建时使用当前用户/SYSTEM DACL，打开时核对实际受保护 DACL 的两条权限记录，拒绝共享目录；记录用当前用户 DPAPI 保护。存储为单引擎独占；本地控制 CLI 可同时读写独立审批记录。

```text
local-probe-operations -action init -journal <新的私有作业目录绝对路径>
local-probe-target ... -operations-journal <私有作业目录> -operations-catalog <运行器JSON绝对路径>
```

两个启动参数必须一起提供；身份、配置、审计和传输参数按[配对说明](R12_PAIRING_AND_TRANSPORT.zh-CN.md)填写。不提供操作参数时仍只读。

## 申请与本地审批

Agent 调用 prepare_operation：

```json
{
  "nonce": "本次意图的唯一请求ID", "kind": "script",
  "tool_id": "python", "root_id": "workspace",
  "script": "print('hello')\n", "args": [], "timeout_seconds": 60
}
```

kind 支持 command/script/version/build/test/file_write/file_patch。准备只校验和保存计划，不执行或改变用户工作文件。probe_tools 仅列出已审核运行器身份；实际版本需批准 kind=version 并读取真实进程输出，不能把安装发现当成版本验证。

获得 ID/digest 后 submit_operation 进入 awaiting_approval。文件可使用 prepare_file_change / commit_file_change；commit 同样提交审批请求，不是立即绕过审批写入。

所有者在本地操作：

```text
local-probe-operations -action list -journal <目录>
local-probe-operations -action inspect -journal <目录> -id <ID>
local-probe-operations -action approve -journal <目录> -id <ID> -digest <已核对摘要> -ack-trusted-host
local-probe-operations -action reject -journal <目录> -id <ID> -digest <摘要>
local-probe-operations -action cancel -journal <目录> -id <ID> -digest <摘要>
```

inspect 显示主体/连接、工作区、参数、脚本正文、原文件与补丁结果；本地用户依据这些内容批准。审批绑定摘要、主体、配置版本及准备后五分钟的期限，不能复用于其它计划。

cancel 是本地所有者的持久化取消请求，后台读取后终止对应任务；CLI 返回仅表示请求已落盘，需通过 list/inspect 确认终态，不等于已确认进程退出。Agent 的 cancel_operation 则等待执行器回收后返回。

运行中至少每 60 秒轮询 operation_status 维持租约，租约到期取消。执行超时默认 60 秒，上限 30 分钟。operation_output 在结束后按 stdout/stderr 和字节偏移分页，返回 base64 原始字节，不将 cmd 本地编码冒充 UTF-8。cancel_operation 等待子树回收后返回终态。

相同主体/连接/nonce 的相同请求返回已有作业，即使该作业已改变文件；不同内容拒绝。网络失败应查询原 ID，不换 nonce 自动重跑。重启将未启动任务标记 interrupted、已记录启动但结果不确定的任务标记 unknown，均不自动执行。

## 文件语义与恢复材料

- UTF-8 文本上限 128 KiB。创建使用独占模式；已有文件绑定内容摘要和文件身份。
- file_patch 接受最多 32 个 `{old,new}` 精确编辑，每个 old 必须唯一出现；不模糊合并。计划保存旧内容与最终结果，审批绑定两者。
- 提交复核只读/可写权限、规则、目录链、重解析点、硬链接和版本。固定目录链句柄并核对实际根解析位置，拒绝路径穿越与绕链。
- 已有文件通过禁止其他写入/删除共享的句柄更新，先保存私有 before image。**不是原子替换**：磁盘失败/崩溃时可能部分写入，明确 unknown 并保留恢复材料，不自动重放/覆盖恢复。

```json
{
  "nonce": "patch-1", "kind": "file_patch", "root_id": "workspace",
  "path": "main.py", "edits": [{"old": "answer = 1", "new": "answer = 2"}]
}
```

本地 `-action before -id <ID> -digest <摘要>` 可读取 base64 原始备份；此命令不恢复文件，需先检查当前内容。当前无多文件事务、递归删除、完整 unified diff 或自动灾难恢复。

## 资源与存储限制

Windows Job Object 实际配置：最多 32 进程、1 GiB 汇总提交内存、与请求时长对应的累计用户态 CPU 时间预算，另有墙钟超时、每路输出 64 KiB 和退出后子树清理。TerminateJobObject 后继续核对内核进程列表为空，最多等待五秒；无法确认时保留 unknown 并关闭执行准入，不把父进程退出或管道关闭当作全部子进程退出。内核读取验证通过；不是 CPU 百分比配额，也不是防恶意代码沙箱。[Windows Job 基本限制](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-jobobject_basic_limit_information)、[扩展限制](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-jobobject_extended_limit_information)、[Job 进程列表](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-jobobject_basic_process_id_list)。

账本最多 128 个历史作业，约 64 MiB 存储预算和有限文件数；满额拒绝新请求。尚无自动归档，不适合无人维护长期队列。记录用临时密文、同步写入及禁止覆盖的完整原子发布；损坏记录/存储异常关闭准入并取消活跃任务。超大 JSON 序列化申请在写入前拒绝，不关闭整个引擎。

## 本机验证与后续

已实际运行 Python 3.13.1、Node v24.19.0、PowerShell 7.6.5、cmd 版本和批准脚本，并生成合成文件；Go 项目实际编译出 EXE 并执行单元测试，禁用依赖下载。官方 SDK 经本机 WSS/mTLS 完成申请→CLI批准→实际写入→查询/冲突拒绝/撤销。另有双连接并行、取消、环境净化、输出限制、补丁、记录发布、重启恢复和只读兼容回归。默认测试跳过真实安装运行器的 opt-in 场景，本轮已另行显式运行。

开发中的失败保留：首个真实进程夹具直接启动 race 测试 EXE，在净化环境中以 `0xc0000135` 退出；改为单独构建 CGO=0 的合成 helper，未放宽宿主环境继承。配置内容不变但 revision 更新的撤权回归最初失败，现将 revision 纳入计划 epoch。审批 ACL 校验最初以 SDDL 字符串比较导致合法目录误拒绝，改为核对实际控制位和每条 ACE，合法/共享目录回归均通过。没有把失败用例跳过或弱化断言。

最终全项目结果见[进展](R12_PROGRESS.zh-CN.md)。独立构建位于 `bin/r12-operations/`，Linux 构建不等于实机验证。

仍需在线规则/配置控制、连接级选择性 epoch、多连接生产宿主、GUI 用途选择/配对/审批/作业视图和 Web 高级入口。当前配置整体变化会使旧审批失效，不能声称连接级热更新已全部完成。

按用户要求先继续本地开发；之后使用新增 Linux 电脑作为 Agent、Windows 作为目标，再通过公网 Relay 验证真实跨机操作。届时再取得 Linux 访问方式；本轮未扫描局域网、连接 Linux 或修改真实中转。
