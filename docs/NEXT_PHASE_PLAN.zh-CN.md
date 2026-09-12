# Local-Probe 下一阶段执行路线（R0–R9）

日期：2026-09-12

本文件把 `docs/MASTER_PLAN.zh-CN.md` 的 P00–P14 细化成可以交给执行者的近期执行包。它是规划和停止条件，不是已实现功能清单；实际事实以 `docs/IMPLEMENTATION_STATUS.md` 为准。任何“验收”在对应测试、证据和审查完成前都不能写成已完成。

当前事实基线：P04 已有 Windows/WSL2 等平台有界验证，P05 已完成一次 ChatGPT Business“极高”真实链路，P06 的四个发现工具已实现并完成真实链路验证，R2 的范围读取和 workspace snapshot 第一增量已接入本地 MCP；R4/P08 已有 Windows 固定 PE/句柄守卫、挂起进程复核、单进程 Job、严格 version profile、`version_probe` `slots:[]` 兼容、`fixed_command` exact argv/typed slots 本地解析与纯 `ResolveVariant`、root ID 交叉校验、同一 guard handle 的执行期 SHA256、canonical `ResolvedInput` digest、绑定 digest+revision 的 confirmation v2（旧 v1 token fail closed）、config revision lease、local commandexec fail-closed bridge、结构化 `ProcessOutcome` 与 `command.result` 本地接线、R4-AUDIT-01（AuditRecorder、同步 `command.reject`、`command.admission`/`command.start` 的有界异步入队、即时校验/入队错误 fail closed）、audit.v2 command producers、NET-01 networkguard contract/fake、R4-NET-02 固定 8-family opaque WFP deny plan、跨平台 DisabledBackend、一次性确认本地核心以及 fixed typed input 的本地 `Prepare`/`Confirm`/`BuildRequest` 边界；prepared input 绑定 profile revision、variant 和 resolved-input digest，Request 不承载可变 argv。`PathResolver` 仍是字符串回调，不能证明 root 授权、deny/reparse 或 final identity，fixed execution 仍在 confirmation/admission/start/runner 前以 `unsupported_profile` 拒绝，因此没有新 fixed 执行能力。`command.start` 仅表示确认后的 launch-dispatch intent，不是 OS 已启动证明。尚未完成 WFP/真实网络断开、broker/service、EnforcementCapability 铸造、CLI/supervisor 生产接线或远程 MCP `run_probe`；异步落盘失败只标记 `degraded`，尚待 supervisor 阻断新执行。本轮也未做 Windows 手工、网页、Tunnel 或真实网络测试；P09/P10 及发布级审查仍未完成。

## 一、能力分层与不变边界

### 1. 默认只读层

默认远程能力只允许已认证 connection 对应 profile 的只读文件范围：

- `read_file`、`batch_read` 读取有界普通文件范围。
- `list_directory`、`find_files`、`search_text`、`tree_directory` 做有界发现和 literal 文本检索。
- 后续的 `discover_tools`、`get_environment` 优先做成 Go 内部无进程查询：只返回固定的 OS、架构、已批准工具类别和能力，不执行外部程序，不读取整份环境变量，不泄露用户名、home 或秘密路径。
- `BoundScope → rootfs.Source → os.Root` 仍是文件访问唯一安全边界；任何 scanner、catalog、index、缓存或加速器都不能绕过它。

`policy.BoundScope.AllowsPath` 已由主代理补上撤权前置校验：scope 失效时路径授权立即返回
false，不会因为旧 scope 仍持有路径/deny 数据而继续通过。该策略修复不等于 typed
`PathResolver` 的 final identity 证明，二者保持独立验收。

默认层不改变进程 cwd，不执行 Shell，不接受绝对路径、任意命令、任意环境或任意网络请求。

### 2. 开发者固定探针层

P08 的 `tool_exists` 和 `tool_version` 只能由本地配置的 command profile 驱动。`codex -v` 只能是一个本地注册的 exact argv profile，例如 `codex_version`，不能变成模型传入的命令字符串。

开发者模式必须：

- 默认关闭，由本地用户显式开启；
- 绑定 `connection + profile + command rule`，不能只由 URL、模型提示或 GUI 文本决定；
- 配置 revision 变化立即撤权；
- 每次执行使用本地确认，confirmation v2 绑定 connection/profile/command/variant、config
  revision、request nonce 和 canonical resolved-input digest；旧 v1 token 必须 fail closed，
  确认不能由 MCP JSON 中的布尔字段伪造；
- 禁止 raw command line、Shell、任意 args/env/cwd、任意后缀和模型修改规则；
- 动态参数只能是 typed slots，例如 enum、root-relative path、bounded integer；服务端逐项构造 argv；
- 只返回结构化结果，原始 stdout/stderr 默认不返回。

### 3. 高风险动作层

Git、解释器、网络、写入和可产生项目状态的动作必须单独审查。Git status/diff 可以在固定 profile 通过后作为受控只读动作推进；任意脚本、项目任务、插件、hooks、外部 diff/textconv、安装器和写入仍不属于默认只读层。写入继续由 P14 独立授权，不能混入本路线。

进程工具的 MCP annotation 要保守：运行进程即使目标是“查询版本”，也可能创建日志、cache、子进程、读取配置或联网，不能轻率标为 `readOnlyHint=true`。annotation 只是客户端提示，不是授权或沙箱。

## 二、command profile 建议契约

规则配置应是本地受信任配置的一部分，模型只能引用 `id`，不能提交或覆盖 profile。字段建议如下：

```json
{
  "developer_mode": {
    "enabled": false,
    "allowed_connections": [],
    "default_confirmation": "per_call",
    "network_default": "deny"
  },
  "command_profiles": [
    {
      "id": "codex_version",
      "kind": "version_probe",
      "platform": ["windows"],
      "executable": "C:\\Program Files\\Vendor\\codex.exe",
      "identity": {
        "require_regular": true,
        "reject_reparse": true,
        "sha256": "local-configured-pin"
      },
      "argv": {
        "variants": [
          {"variant_id": "short", "exact": ["-v"]},
          {"variant_id": "long", "exact": ["--version"]}
        ],
        "slots": []
      },
      "cwd": {
        "kind": "private_empty"
      },
      "env": {
        "inherit": false,
        "allow": [],
        "fixed": {}
      },
      "limits": {
        "wall_timeout_ms": 2000,
        "stdout_bytes": 8192,
        "stderr_bytes": 4096,
        "max_processes": 1,
        "max_children": 0
      },
      "network": {
        "mode": "deny",
        "require_enforcement": true
      },
      "confirmation": {
        "mode": "per_call",
        "local_only": true
      },
      "result": {
        "type": "version",
        "return_raw_output": false
      }
    }
  ]
}
```

`argv.variants` 是本地规则定义的命名 exact argv 集合；上例明确允许 `codex -v` 和
`codex --version` 两种变体。远端请求最多只能选择 `command_id=codex_version` 与
`variant_id=short|long`，不能传 `suffix`、`argv`、`executable` 或命令字符串。`fixed_command`
还可在本地规则中使用逐项 literal/slot 模板；slot 只允许 `enum`、`root-relative path`、
`bounded integer`，由服务端逐项组装 argv。`version_probe` 的旧 `slots:[]` 配置继续兼容。
可信本地调用方通过 `ResolveInput` 得到不可变 argv 和 canonical resolved-input digest；digest
对 profile/variant/固定 identity 与带边界的完整 argv 做域分离和长度前缀编码，并由
confirmation v2 绑定 config revision。root-relative path 采用 `/` 规范，限制为 4096 字节并
拒绝 Windows 保留名/非法字符；可信 resolver 对同一 slot 只做一次解析并复用结果，resolved
argv 受 32767 字节保守预算约束。`allow_any_suffix`、通配后缀和任意参数数组首版明确拒绝。

| `kind` | 执行语义 | 首批范围 |
|---|---|---|
| `environment_query` | 无进程、默认层 | `discover_tools`、`get_environment` 的固定 Go 内部查询 |
| `version_probe` | 固定进程、受限环境 | 首批 developer mode，例如 `codex_version` |
| `git_read` | 固定 Git 进程、禁 hooks/external diff | R5 的 `git_status`、`git_diff` |
| `project_exec` / `networked` / `mutating` | 可能执行项目代码、联网或改变状态 | 首版不支持，不得用 annotation 或确认字段绕过 |

契约约束：

- `executable` 必须是绝对路径；Windows 本地核心会重新检查 regular file、PE、reparse/symlink、
  handle identity 和挂起进程实际映像。当前 profile 要求小写 64 字符 `sha256` pin，执行期从
  同一最终映像 guard handle 计算并比较摘要；签名/Authenticode 校验尚未接入，模型不能传路径。
- `argv.variants[].exact` 是服务端拥有的完整参数序列；远端只能引用已配置的 `variant_id`。slot 只允许枚举、受限路径或有界整数，不能允许自由字符串。路径必须是 `root_id + relative path`，不能拼接 OS 绝对路径。
- `kind`、variants、exact argv 和 slot 定义均不可由 MCP 修改；规则 revision 改变后旧授权失效。
- `cwd` 只允许 `private_empty` 或已授权 root-relative 目录。cwd 本身不是文件系统沙箱；没有 OS 级隔离时不得声称子进程只能访问该 root。
- `env` 默认清空，不继承 `PATH`、`HOME`、`USERPROFILE`、`PYTHONPATH`、`NODE_OPTIONS`、`LD_PRELOAD`、`GIT_EXTERNAL_DIFF`、pager 或 hooks 相关变量。任何秘密值禁止通过 MCP 参数传入。
- `network=deny` 只有在 OS 层确实执行时才成立；当前 local commandexec bridge 在没有可信
  capability 时拒绝，无法证明时必须拒绝该 profile 或标为 local-only，不能用配置字段冒充隔离。
- `confirmation` 必须由本地 UI/CLI 产生一次性、短期、绑定 request/command revision 的授权；模型文本中的“我确认”不算确认。
- `result` 优先是 `version`、`exists`、受限 `paths`、`exit_status` 等结构化结果。绝对路径、完整 argv、原始输出默认不出远程端。

## 三、P08 前置硬门：Windows 本地 TOCTOU 核心第二增量已实现，生产硬门仍未全部通过

Windows 本地 launcher 已使用固定路径/PE 检查、父目录与最终映像句柄守卫、挂起进程、Job
Object 和 resume 前实际映像复核；profile 配置和执行期只接受固定 version args，SHA256 从同一
最终映像 guard handle 计算并比较，config revision lease 覆盖到执行结束，local commandexec
bridge 在 network capability 缺失时 fail closed。不能再把它描述成单纯
audit-by-path→execute-by-path。
但 R4 仍未完成：OS network deny 执行器、capability 铸造、CLI/supervisor 生产接线、可信最终
resolver、签名校验和 MCP 暴露均缺失。fixed_command 的 typed runtime values 已进入仅供本地
调用方使用的 `Prepare`/`Confirm`/`BuildRequest` 边界，prepared input 绑定 profile revision、
variant 和 resolved-input digest；不过当前 `PathResolver` 仍是字符串回调，不能证明 root 授权、
deny/reparse 或 final identity，因此 fixed execution 仍 fail closed。当前 local commandexec 已将
version-probe 的结构化 `ProcessOutcome` 接入 `command.result`；这不等于 CLI/supervisor 的生产
接线。已有 pre-open writable/mapped handle 残余风险；`LockFileEx` 的字节范围锁不覆盖 mapped
view，不能作为完整修复。Unix 当前按路径启动，不能仅靠事后检查保证执行的是被审计文件；本轮不做
Linux 测试。

### Windows 硬门

1. **已实现（Windows 核心）**：使用显式 application name，不让命令行解析决定实际 executable。
2. **已实现（Windows 核心）**：只允许经过检查的 PE executable；拒绝 `.cmd`、`.bat`、`.ps1`、`.lnk`、`.url` 等 wrapper/脚本。
3. **已实现（Windows 核心）**：以 `CREATE_SUSPENDED` 创建并立即加入 Job Object。
4. **已实现（当前核心）**：resume 前查询实际进程映像并比较路径/handle identity；执行前从同一
   guard handle 核对配置的小写 SHA256。签名/Authenticode 尚未接入。
5. **已实现（Windows 核心）**：验证失败终止 Job；验证通过才恢复主线程。
6. **已实现（Windows 核心）**：保留 Job Object kill-on-close、单进程限制、无窗口、输出/超时和完整子进程回收。

### Unix 硬门

1. 优先使用 `openat2`/`O_NOFOLLOW` 等无 symlink 路径解析。
2. Linux 优先使用 `execveat(AT_EMPTY_PATH)` 或等价的 fd-based 执行，使已打开文件对象成为执行对象。
3. 使用独立 process group，并在可用时增加 pidfd/cgroup 等生命周期约束。
4. 不支持安全 fd 执行的平台不得静默降级为普通路径启动；只能 fail closed 或标为 local-only/reduced assurance。

### `where.exe` 和 `codex -v` 的特殊要求

- `where.exe` 优先实现成不启动进程的内部可信目录解析。若实际调用，必须是固定绝对 profile；其输出只表示候选，不授予执行权。
- `codex -v` 必须使用实际 binary 的绝对路径和固定 argv。`.cmd/.bat/.ps1` wrapper 默认拒绝。
- 即使 `-v` 看似只读，也必须使用私有 cwd、隔离的 `HOME/USERPROFILE/CODEX_HOME`、闭 stdin、精简环境、输出限制和已证明的网络策略。
- 任一 profile 不能证明不会读取配置、写 cache、联网或启动子进程时，不得标记为无副作用只读探针。

## 四、大项目读取与搜索路线

### 4.1 近期直接读取优先

在索引之前先稳定 direct scanner：

- 修复显式 stable ordering，避免不同分页看到非确定顺序；
- 测试 cursor replay、篡改、撤权、generation 变化和 coverage 完整性；
- 增加全局 handles/files/dirs/wire/admission budgets，而不只限制单次 batch；
- 继续保留 deny 优先、ignore 跳过、不跨 symlink/junction/reparse 和 `BoundScope → rootfs.Source → os.Root` 唯一边界；
- 后端的 walker、catalog 和 index 选择不直接暴露成更多模型工具。

当前应明确跟踪的瓶颈：walker 分页可能重复扫描前缀，`ReadDir` 顺序未显式稳定，每项 `Info` 可能产生额外 stat，generation 仍是弱证据，literal 扫描目前偏单线程，大小写语义主要是 ASCII/有限 UTF-8。它们先通过基准和夹具确认，再决定是否优化。

### 4.2 读取 range 扩展

`read_file`/`batch_read` 以 `range.kind` 增量扩展为 `bytes | lines | tail`，不另加独立 tail 工具：

- `bytes` 保留当前 byte-range 语义；
- `lines` 必须说明前缀扫描预算、长行策略和 continuation；
- `tail` 使用反向有界 block scan，不能为了最后 N 行读取无限前缀；
- 三者都返回实际扫描/读取预算、版本证据和是否完整，不能假称强快照。

### 4.3 P07 snapshot 与搜索

- `workspace_snapshot` 只做 bounded outline、manifest 候选、语言统计和 evidence paths；不是强快照，不运行项目脚本。
- `search_text` 保持 literal 默认；只有基准证明需要时才增加 RE2 regex、include/exclude、context 和更丰富 coverage。
- `find_files` 可增加受限 metadata，但不能转成任意 stat 扫描或泄露 denied 子树。
- `rg` 只能作为固定、审计过的可替换加速器：固定 executable/参数、`--no-config`、净化环境、预先授权候选、结果回到 rootfs policy 复核；不能把 root 直接交给 rg 绕过 rootfs。
- watcher 只能产生 dirty hint；发生 overflow 后必须全量 reconcile，不能把 watcher 状态当成完整索引。

### 4.4 索引后置

P11 先做 10k/100k/1m 条目的 cold/warm 基准，比较 direct、内存 catalog、SQLite metadata、FTS 的延迟、内存、漏项和变化目录行为。索引只产生候选，最终必须 live verify；没有收益则默认关闭。symbol index 和统一 `workspace_query` 只有模型评测证明能减少遗漏和往返时才增加，避免工具爆炸。

## 五、audit.v1 基础设施与 audit.v2 command 扩展

日志先于 GUI 和 supervisor 落地，采用 typed JSONL。每条记录至少包含：

`ts`、`event`、`schema`、`instance`、`correlation`、`parent`、`component`、`type`、`severity`、`outcome`、`error_code`、`duration`、`connection`、`profile`、`revision`、`budget`。

事件类别固定为：`MCP`、`auth`、`policy`、`fs-search`、`command`、`network-tunnel`、`service-config`、`error`。

禁止记录：token、JWT、key、cookie、请求正文、文件内容、query、完整 argv/env/stdout/stderr 和无必要的绝对路径。路径只能记录 per-install HMAC digest 加相对安全信息；command 记录 rule/template/identity digest、exit、timeout、byte counts；network 只记录 state transitions、ready、HA、backoff，不记录 payload。

实现要求：

- producer-side redaction，不能只依赖下游日志过滤；
- ACL、rotation、retention、bounded ring 和 sanitized support bundle；
- 普通日志允许受控丢弃，但 drop 必须产生 degraded 状态；安全事件 write-through；
- audit sink 不可用时拒绝新增 ingress 或管理变更；
- 日志字段 schema 版本化；当前 sink 统一发出 audit.v2，audit.v1 只作为历史兼容标识保留。
  禁止把原始命令或环境作为“调试方便”写入。command producer 只能记录固定 selector、identity
  digest、exit/timeout、stdout/stderr 字节计数和 network enforcement 状态。R4-AUDIT-01 已接入
  local commandexec：拒绝路径同步发出 `command.reject`；`command.admission`/`command.start` 仅做
  即时校验并进入有界异步队列，校验或入队错误 fail closed，后续落盘失败只标记 `degraded`，
  尚待 supervisor 阻断新执行；不能声称每条记录都在启动前持久化。version-probe 的结构化
  `ProcessOutcome` 已在本地 bridge 映射为 `command.result`；`command.start` 是确认后的
  launch-dispatch intent，不是 OS 已启动证明。不记录 path、argv、env、output 或 token。

运行进程或写入日志本身会产生状态，因此不能轻率使用 `readOnlyHint=true`；新增进程工具的 annotation 必须作为 MCP 契约单独 review。

## 六、P09/P10/P12：先 supervisor，再 GUI

### 6.1 Supervisor 边界

GUI/tray 只是 client，不拥有 tunnel/origin child；per-user supervisor 才拥有 origin、tunnel child 和生命周期。每个 connection 使用独立 state、端口、日志和凭据引用；命令 worker 使用低权限身份。

本地管理优先 named pipe + SID ACL；loopback fallback 必须有随机本地 auth token、Host/Origin/CSRF 检查，管理接口永不穿 Cloudflare Tunnel。退出 tray 不停止 supervisor。

状态机至少包含：

`stopped → starting → local_mcp_ready → polling → ready`，另有 `degraded`、`auth_failed`、`backoff`、`sleeping`、`resuming`。

`ready` 必须同时满足 child ownership（PID + creation time + Job）、认证 MCP `ping/server_info`、配置 revision，以及 Cloudflare `/ready` + HA>0 或对应 tunnel health；进程存在或 `/healthz=200` 单独都不算 ready。

重试使用 `1/2/4/.../60s + jitter`；认证失败进入 circuit breaker；不杀陌生端口进程；检查 PID reuse 和 orphan。sleep/wake、断网、凭据轮换、单 connection 崩溃必须隔离。

### 6.2 GUI/tray 后端契约

GUI 页面后端先定义：overview、connections、roots/profile/egress preview、developer rules validator、logs/support、安全、about。tray 只提供 status、start/stop/reconnect、open、copy sanitized diagnostics、exit；autostart opt-in。

需要 UAC 时使用 one-shot、签名、enum-only helper；不得让 GUI 传 arbitrary command。GUI 不能修改 MCP 规则来绕过服务端 policy。

## 七、发布必补与停止条件

正式发行前必须完成：

- 配置 schema migration、atomic backup/rollback、single-instance lock；
- Credential Manager/DPAPI 等平台凭据保护；
- binary hash、Authenticode/signature、SBOM、许可证、CVE/provenance；
- 全局 admission/rate/concurrency/memory/handle/wire quotas；
- 至少两条 connection 的隔离与撤权；
- redacted support bundle 与隐私删除；
- clean install/update/rollback/uninstall、SmartScreen 和兼容性矩阵；
- 独立安全审查与至少一小时 soak；
- P13 的对抗测试和真实环境证据。

以下任一情况立即停止对应包并禁止发布：越权、命令绕过、凭据或文件内容进入日志、TOCTOU 执行错误映像、网络策略无法证明、全局预算失控、错误宣称 complete、跨 connection cursor/scope 复用、无法回收子进程、support bundle 泄露敏感信息。P14 写入能力保持独立，不因只读路线通过而自动放行。

## 八、执行包 R0–R9

### R0：文档与契约冻结

依赖：当前 P04–P06 事实基线。产物：能力分层、command profile、annotation、audit.v1、停止条件和本路线文档。验收：所有未来能力明确标为计划，禁止 raw command/shell/任意参数的 schema 进入设计。停止条件：安全边界或真实验证范围无法写清时，不进入执行代码。

### R1：audit、资源预算与 direct search hardening

依赖：R0、P04/P06。产物：typed JSONL audit、global handles/files/dirs/wire/admission budgets、stable ordering、cursor replay/coverage/generation 测试、walker/ReadDir/Info 基准。验收：超预算可解释且不假称完整；日志脱敏；direct scanner 在大目录和变化目录下无越权和不可控增长。停止条件：任何 scope、cursor、预算或日志泄露失败。

### R2：lines/tail、snapshot 与模型评测

依赖：R1。产物：`range.kind=bytes|lines|tail`、bounded `workspace_snapshot`、literal 搜索扩展的实验结果、任务 baseline。验收：长行、反向 tail、变化文件、未完整 coverage 均可解释；snapshot 不运行脚本，不冒充强快照；模型任务用证据覆盖率、遗漏率、调用数、重复读取和时间评测。停止条件：没有质量收益或出现隐式全量扫描时保持现状。

### R3：无进程环境发现

依赖：R0、P05。产物：`discover_tools`/`get_environment` 的 Go 内部实现和只读 schema；可信目录解析和 path disclosure 规则。验收：不启动外部进程、不读完整 env、不泄露秘密路径；`where.exe` 不作为信任根。停止条件：必须依赖 PATH、用户配置或外部执行才能保证结果时，保持 local-only 或不实现。

### R4：P08 TOCTOU 与固定 command profiles/developer mode

状态：Windows 固定路径/PE/句柄守卫、挂起映像复核、单进程 Job、私有环境/cwd/输出/超时、
严格 version profile（含固定 args 和小写 SHA256）、同一 guard handle 的执行期 hash、canonical
resolved-input digest、绑定 digest+revision 且旧 v1 fail closed 的 confirmation v2、config
revision lease、local commandexec fail-closed bridge、结构化 `ProcessOutcome` 与本地
`command.result` 接线、固定 command audit.v2 producers、NET-01 contract/fake、R4-NET-02
固定 8-family opaque plan/跨平台 DisabledBackend 和一次性确认核心已实现；仍属于 local-only，
不能接入远程 MCP。`command.start` 是确认后的 launch-dispatch intent，不是 OS 已启动证明。
依赖：R0、P04/P05；可与 R1/R2/R3 的非进程部分并行，但远程暴露必须等全部硬门通过。
剩余产物按以下顺序推进（设计见 [`docs/R4_WINDOWS_NETWORK_DENY.md`](R4_WINDOWS_NETWORK_DENY.md)）：

1. **已实现：NET-01 contract/fake**：`internal/networkguard` 已冻结平台无关的 network
   enforcement lifecycle contract，并用无系统状态的 fake 验证每操作 opaque lease/capability/run、
   完整 IPv4/IPv6 outbound/inbound、bind/listen、loopback、children、inherited handles、existing
   flows、DNS/proxy 与 cleanup coverage；admission 最多 30 秒，cleanup 默认 5 秒，cleanup 失败
   阻塞后续 admission。它不铸造 `commandprofile.EnforcementCapability`，不触碰 WFP/Windows
   Firewall，也不等于 production network deny。
2. **已实现：NET-02 固定 plan/disabled backend**：`internal/networkguard/wfp` 只生成一个
   不接受调用方参数的 opaque deny plan，固定八个 ALE family：`AUTH_CONNECT_V4/V6`、
   `AUTH_RECV_ACCEPT_V4/V6`、`AUTH_LISTEN_V4/V6`、`RESOURCE_ASSIGNMENT_V4/V6`；每个 family
   固定 `block`、`dynamic_only`、`target_app` 和 `target_user` identity slots。`AUTH_LISTEN`
   不能由 connect/recv_accept 推导省略：主动连接、被动监听/接收和 bind/resource assignment
   是不同的覆盖语义。跨平台 `DisabledBackend` 只保存每个 opaque lease 的固定 plan，
   `LaunchSuspended`/`Activate` 始终 fail closed，`Activate` 返回零 coverage，`Revoke` 只做
   本地幂等清理。该增量不调用 `fwpuclnt.dll`，不实现 WFP ABI/dynamic session/filter install，
   不请求管理员权限，不修改 Windows Firewall，不铸造 capability；本阶段也没有真实/手动
   网络测试证据。
3. **已实现：developer-mode exact argv/typed slots 本地配置验证**：本地受信配置支持
   `fixed_command` 的 exact argv 和逐项模板，typed slots 限定为 enum、root-relative path、
   bounded integer；`version_probe` 保持 `slots:[]` 兼容。解析会做 root ID 交叉校验、路径 4096
   字节/Windows 保留名与非法字符检查，并由纯 `ResolveVariant` 在 32767 字节保守预算内构造
   argv。可信 resolver 的最终授权仍未被该纯函数取代；该增量不注册 MCP `run_probe`，而且
   commandexec 在 confirmation/admission/start/runner 前对 `fixed_command` fail closed。
4. **已实现：fixed typed input 的本地 prepare/confirm 边界**：`commandexec` 为本地调用方
   提供 `Prepare`、`Confirm` 和 `BuildRequest`；typed runtime values 经
   `commandprofile.ResolveInput` 逐项解析，生成绑定 profile revision、variant 和
   resolved-input digest 的不可变 prepared input，Request 不承载可变 argv，Prepare/Confirm
   各自在关键窗口持有 revision lease。当前 `PathResolver` 仍是字符串回调，只能作为临时
   本地接口，不能证明 root 授权、deny/ignore、reparse/symlink 或 final identity；它不是
   trusted final path binding。fixed execution 仍在 confirmation/admission/start/runner 前
   fail closed，不执行、不注册 MCP。下一步完成可信最终 resolver、Windows UTF-16/escaping
   检查及其硬门；version-probe 已有的 digest/outcome/`command.result` 接线不能被误认为
   fixed_command 已开放。
5. **后续：真实 WFP adapter**：基于受审查的 `fwpuclnt.dll` user-mode ABI 实现动态会话级
   WFP 过滤器，覆盖上述八个固定 family 以及 loopback/适用的 identity 绑定；不创建持久
   Windows Firewall 规则，未能证明的状态 fail closed。该 adapter 尚不能直接接入当前
   `internal/probe`：当前 probe 内部自己创建并 resume 进程，仍需先建立 broker/launcher
   对挂起进程、Job、WFP lease 的统一所有权。
6. **低权限 broker/service**：把需要 UAC/SID ACL 的安装和 WFP 管理放在签名、低权限 broker/service，
   运行期只返回不可伪造的本地 enforcement capability；拒绝任意模型参数和持久化放行规则。
7. **suspended Job integration**：将 capability 生命周期接到现有挂起进程/Job 流程，必须在
   resume 前确认网络状态、句柄 identity、revision lease 和子进程边界仍有效。
8. **VM identity/network adversarial tests**：在 Windows VM 中验证替换、wrapper、pre-open
   writable/mapped handle、IPv4/IPv6、DNS/proxy、existing flow、child process、crash/restart
   和撤权；`LockFileEx` 不能替代 mapped-view 防护。
9. **CLI/supervisor 生产接线与剩余 audit integration**：把 local commandexec 接入正式
   生命周期，并在 sink `degraded` 或网络状态不确定时由 supervisor 阻断新的执行；将已有
   `ProcessOutcome`→`command.result` 本地接线纳入正式生命周期，补齐异常、恢复和丢失事件策略。
   审计不得写入 argv、env、输出、路径或密钥。

此外仍需 Unix fd-based launcher、签名/Authenticode 校验、Windows 本地手测证据和独立审查。
验收：替换/脚本/wrapper/环境注入/子进程/超时/网络测试通过；无法证明的 OS 状态 fail
closed。停止条件：network deny 无法证明、profile/bridge 未安全接线、SHA256/签名约束未执行、
或原始输出会泄露秘密时不接 MCP。

### R5：受控 Git read actions

依赖：R4 的固定进程边界和 R1 资源预算。产物：固定 `git_status`/`git_diff` action；禁 external diff/textconv/pager/hooks，显式 `--`，root-relative paths，结构化 coverage。验收：恶意项目、参数以 `-`、外部 diff、环境注入和超量输出测试通过。停止条件：Git 能执行项目代码或无法禁用外部扩展时不暴露。

### R6：P09/P10 多 connection、supervisor 与恢复

依赖：P05、R1、R4。产物：per-user supervisor、per-connection state/port/log、health state machine、backoff/circuit breaker、sleep/wake/credential rotation/kill isolation。验收：至少两 connection 并发和一小时 soak；单 connection 故障不影响另一条；PID reuse/orphan/陌生端口不被误杀。停止条件：跨 connection scope/cursor/cache 泄露或 ready 仅凭进程存在时不进入产品化。

### R7：以证据决定是否启用 index

依赖：R1、R2。产物：10k/100k/1m cold/warm direct/catalog/SQLite/FTS 对照，变化目录 reconcile 和内存/延迟报告。验收：index 只给候选，live verify 无遗漏优势且确有收益；否则默认关闭。停止条件：index 漏项、stale 结果无法标识或没有收益时保留 direct。

### R8：GUI/tray

依赖：R6 及已冻结的 audit/config/supervisor 契约。产物：GUI/tray 只读 overview、连接和规则校验、诊断复制、受控 start/stop/reconnect；UAC signed enum-only helper。验收：GUI 不能绕过 policy、退出 tray 不停 supervisor、support bundle 脱敏。停止条件：GUI 需要持有 credential、tunnel secret 或任意命令权限时后置。

### R9：发布与独立安全审查

依赖：R1–R8 中适用包、P13。产物：migration/rollback、签名发布、SBOM/license/CVE/provenance、安装更新卸载矩阵、独立审查报告、soak 和支持流程。验收：P13 的硬失败项全部为零，已实现、自测、独立审核、release-ready 分开记录。停止条件：任何硬失败项存在即不发布；P14 仍另行审批。

## 九、并行关系与“现在做/暂不做”

可并行：R1 与 R3 可在 R0 后并行；R4 可与 R1/R2 的非进程部分并行，但不能绕过 TOCTOU 硬门；R5 与 R6 可在 R4 通过后分别推进；R7 必须等待 R1/R2 基准；R8 必须等待 R6；R9 汇总全部发布证据。

现在做：继续保持远程能力关闭；fixed typed input 的本地 `Prepare`/`Confirm`/`BuildRequest`
边界已完成，但其字符串 `PathResolver` 不是可信 final path binding。下一步完成可信 path
resolver、root 授权、deny/reparse/final identity 和 Windows UTF-16/escaping 边界；
version-probe 已完成的 resolved-input digest、revision、confirmation v2、结构化 outcome 和
`command.result` 本地接线作为可复用基础，但不能据此开放 fixed 执行。继续禁止 cmd、PowerShell
及其它 shell/interpreter 间接路径；fixed MCP execution 仍不注册。随后按 WFP adapter → 低权限
broker/service → suspended Job integration → VM identity/network adversarial tests →
CLI/supervisor 生产接线推进 R4。自动化检查先行；本轮尚未做 Windows 手工、网页、Tunnel 或
网络测试。硬门全部通过前保持 MCP `run_probe` 不注册。
R1/R2/R3 的读取、搜索和无进程环境能力继续按既有边界演进。日志、审计和资源预算从 R1 起成为基础设施。

暂不做：MCP `run_probe`、raw command line、Shell、allow_any_suffix、模型自定义 args/env/cwd/timeout、未经 network deny/生产接线/SHA256 硬门的 `codex -v`、把 root 直接交给 rg、默认开启 index、统一 workspace_query、自动执行项目脚本、GUI 绕过后端权限以及任何写入能力。

对用户提出的四项调整，当前裁决是：自定义命令可以做，但语义先落在本地规则模板的 exact
argv/typed slots 验证，不开放 arbitrary shell；优先提升 direct 读取能力，索引后置；日志立即建设；
GUI/tray 后置，但先冻结 supervisor、audit、权限和配置边界。
