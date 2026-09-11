# 接口契约与实现边界

本文件区分**当前 readcore.v0** 与**计划中的 MCP 产品接口**。不应把 Go 内核接口注册到公网后宣称完成 Local-Probe。

## 一、当前调用契约

`Engine.ReadBatch(ctx, trustedScope, requests)` 接收一个已经由可信调用方决定的 Scope 和一组 Request。Scope 内部保存 connection、profile revision 和复制后的 roots 集合，不从模型 JSON 解码。本节只描述脱离服务层的 readcore 调用；认证 ingress、配置存储、动态撤权和生产文件打开器由后文的 MCP 适配层另行约束。

Request 的 `file.root_id` 和 `file.path` 选择授权范围内的文件。path 使用 `/` 分隔的规范相对路径；offset 为 0-based 字节位置。`max_bytes=0` 使用单项默认最大预算；负数、越过配置上限、非法路径和未授权 root 产生单项错误。

```json
{
  "file": {"root_id": "project", "path": "src/example.go"},
  "offset": 0,
  "max_bytes": 4096,
  "expected_version": "previous-source-version"
}
```

expected_version 可省略。要续读时使用前一结果返回的 version.token 与 next_offset，不根据字符串长度或行数猜位置。

## 二、Source/Handle 是受信任边界

`Source.Open` 必须在真实打开动作中完成 root containment、secret deny、普通文件限制、平台链接/路径策略和权限撤销检查。不能只依赖 core 的 `ValidPath`；它仅检查词法形式。

成功返回的 Handle 必须稳定指向同一个已打开文件对象，允许有界 ReaderAt，并提供有界 Metadata。Metadata 不应每次扫描/哈希整个巨型文件。失败时不能把绝对路径、私有配置或秘密塞进错误消息；核心也会对错误做脱敏。

内核接管成功返回 handle 的 Close。适配器必须并发安全，响应 context 取消，但无法强制中断任意阻塞的 OS ReaderAt。首版生产适配器应限制为受支持的普通本地文件系统。

演示程序不是生产 Source：它只映射本机操作者预先打开的一个文件句柄，不解析模型传来的 OS 路径，也不监听网络。

## 三、预算、公平性与并发

当前默认：32 项/batch，4 个 worker，32 KiB/项，128 KiB 总正文，8 MiB 总逻辑 I/O，10 秒超时。构造器另有安全硬上限。

执行前按请求需求做确定性 max-min 分配，总配额不超过正文/I/O 限额较小者；舍入余量按输入顺序分配。结果始终保持输入顺序，不以完成速度抢占预算。短文件/失败项剩余配额本版本不回收，因此简单稳定但有时预算利用率较低；后续是否二轮回收由基准决定。

worker 上限是**每个 batch** 的，不是所有账号的全局上限。后续服务层必须增加全局并发、每连接配额、公平排队和句柄限制。

`bytes_read` 是 ReaderAt 实际返回的逻辑字节，包括随后因 UTF-8 裁切或版本变化被丢弃的字节；不代表 OS 的物理磁盘读取量。`returned_bytes` 是正文 UTF-8 字节数。它们均不是 token 数，也不是包含 JSON 转义/元数据的完整 wire 字节数。

MCP 层必须独立限制请求体、字符串、序列化后的完整响应大小和传输队列。本内核不能替代这些限制。

## 四、结果与错误

BatchResult 包含 schema_version、按输入排序的 items、returned_bytes、bytes_read 和 failed。每项包含 file、offset/end_offset、size_bytes、version、content、eof、next_offset、allocated_bytes、bytes_read 和可选 error。

发生单项失败时保留错误码及已消耗 I/O 统计，但不返回正文、成功 EOF 或 continuation。非法/未授权请求不回显原始文件字段。全局批量大小/零权限 Scope 错误直接作为调用错误返回。

当前错误码：`invalid_request`、`denied`、`not_found`、`unsupported_encoding`、`stale_version`、`budget_exhausted`、`deadline_exceeded`、`cancelled`、`unavailable`。未来 unsupported_type 等需要由正式适配层增加明确映射，不能假装当前都已实现。

## 五、UTF-8 及继续读取

支持普通 UTF-8 文本。非 EOF 页末的不完整字符会退回到完整边界；next_offset 指向实际已返回正文末尾。页首落入字符中间、非法编码、NUL 和 EOF 的不完整字符返回错误，不用替换字符改写源码。

小到不足一个字符的预算返回 budget_exhausted，提示缩小 batch 或提高页预算；不会无限返回相同的零进展 continuation。空文件和恰好到 EOF 返回成功且没有 continuation。

对于本节的 readcore 直接调用，当前仍是 byte-range，不含按行定位、日志 tail、目录遍历和内容搜索；MCP 产品层的 P06 发现/搜索工具见第八节。

## 六、一致性不是快照保证

打开后检查 expected_version，读取前后比较 Metadata。检测变化则丢弃该项正文。version.strength 可为 metadata 或 snapshot，值由可信 Source 提供。

metadata 是弱版本：相同 size/mtime 的内容修改可能无法检测，读取完成后文件也仍可能变化。未来 strong snapshot 必须有真实不可变副本或其他验证机制，不可仅重命名字段。多个文件读取也不是仓库事务快照。

当前 next_offset 是内核位置提示，**不是签名 MCP cursor**。生产 cursor 必须绑定 connection、profile revision、root、文件/查询、版本、过滤条件和过期时间，处理撤权和篡改；这一功能尚未实现。

## 七、MCP 工具面（当前与未来）

当前本地 MCP 服务已注册并按 connection/profile allowlist 暴露：`server_info`、`ping`、`read_file`、`batch_read`、`list_directory`、`find_files`、`search_text`、`tree_directory`、`get_environment`、`discover_tools`。`workspace_snapshot`、`run_probe`、`git_status` 和 `git_diff` 仍是未来能力，不能从当前工具列表推断已实现。

发现/搜索/目录树工具已经返回 request_id、coverage、warnings、预算和 continuation；`read_file`/`batch_read` 的 byte-range 结果按第四节约束。coverage 必须说明忽略、deny、编码、扫描上限和未支持类型，不能把部分扫描标成全量。原生 MCP 的 readOnlyHint 只描述工具性质，不替代本地权限控制。

## 八、P06 发现、文本搜索与目录树工具（第一增量）

这四个工具都只接受已认证 connection 对应的 `root_id` 和规范化相对路径。根目录的
`path` 省略或使用空字符串；绝对路径、`..`、反斜杠、NUL 和平台保留名都会被拒绝。
服务端从配置快照取得 root，模型不能传入或替换本机绝对根路径。显式 deny 优先于
ignore；ignore 只影响发现结果，不授予读取权限。默认不跨 symlink、junction 或其他
reparse 边界。每个结果都包含 `coverage.complete`、计数和 `warnings`，因此部分扫描
不能被误认为全量结果。`continuation` 是带 HMAC 的短期游标，绑定 connection、profile、
配置 revision、root、起始路径、查询/模式、大小写和预算；篡改、过期、撤权或目录/文件
generation 变化都会要求重新开始。

四个工具的 `budget.max_open_files` 和 `budget.max_open_directories` 是**单次调用**的
成功打开句柄预算，不是账号、连接、进程或机器级全局配额。`coverage.opened_files`、
`coverage.opened_directories` 记录本次调用成功打开的句柄数；`coverage.replayed_entries`
记录为恢复签名游标而消耗的目录记录数。目录源保留原生 `ReadDir(n)` 的目录流顺序；
在同一个 directory generation 内分页顺序稳定，但不承诺字典序/字母序，也不会为了排序
把大型目录整体载入内存。

可能出现 `open_file_limit` 或 `open_directory_limit`。如果目录打开预算已经被恢复游标
所需的目录栈完全消耗，继续生成同一个游标不会取得进展；此时返回
`open_directory_limit_no_continuation`、`complete:false` 且不返回不可推进的
`continuation`。调用方应把它视为有界的部分结果，而不是重复重试同一游标。

### `tree_directory`

Use when：需要在一个授权目录范围内以类似 `tree` 的方式查看层级，并希望一次只接收一页
扁平、且在同一 directory generation 内保持目录流顺序的路径条目。

Do not use：需要执行 `tree.exe`、Shell、改变进程当前目录或读取文件正文时。它不会创建
会话级 `cd` 状态；每次调用都必须重新提供 `root_id`、相对 `path` 和（如有）签名游标。
目录本身以深度 `0` 返回，普通文件以叶子条目返回；symlink、junction 和 reparse 条目
只返回类型元数据，绝不会继续下钻。

完整参数示例：

```json
{
  "root_id": "project",
  "path": "src",
  "max_depth": 4,
  "page_size": 128,
  "max_entries": 4096
}
```

结果使用扁平 `entries`，每项含规范相对 `path`、`name`、`type` 和相对于起始目录的
`depth`，并包含 `coverage`、`warnings`、`budget` 与可选 `continuation`。`max_entries`
限制实际扫描的子项数；根目录不计入该扫描计数，但计入返回条目数。达到页数、扫描、输出
或时间预算时必须 `complete:false`，并在可继续时返回游标；达到 `max_depth` 或链接/特殊
类型边界时也不能宣称整棵树完整。显式 deny 优先于 ignore，二者均不会把被过滤的名称
写入结果。

若 `continuation` 非空，使用相同的 `root_id`、`path`、`max_depth`、`page_size` 和
`max_entries` 再次调用并把游标放入 `cursor`。游标绑定 connection、profile revision、
root、起始路径和全部预算；不能跨连接、改预算或在目录 generation 改变后继续使用。

### `list_directory`

Use when：需要查看一个授权目录的下一页直接子项，且希望看到文件、目录和被识别的
特殊项类型。

Do not use：需要递归找文件或读取内容时；此工具不返回文件正文，也不保证 live listing
是原子快照。它按有界批次迭代目录，保持同一 generation 的原生目录流顺序，不会先把
整棵目录排序载入内存，也不承诺字典序。

完整参数示例：

```json
{
  "root_id": "project",
  "path": "src",
  "page_size": 32,
  "max_entries": 256
}
```

成功结果的核心形状：

```json
{
  "schema_version": "local-probe.search.v1",
  "root_id": "project",
  "path": "src",
  "entries": [{"path": "src/main.go", "name": "main.go", "type": "regular"}],
  "coverage": {"complete": false, "scanned_entries": 32, "returned_entries": 1,
    "replayed_entries": 0, "opened_files": 0, "opened_directories": 1},
  "warnings": ["page_limit"],
  "budget": {"page_size": 32, "max_entries": 256, "max_output_bytes": 262144,
    "max_open_files": 256, "max_open_directories": 256},
  "continuation": "v1.…"
}
```

若 `continuation` 非空，使用同一个 `root_id`、`path`、预算再次调用并把游标放入
`cursor`。不要修改任何过滤条件；若目录 generation 改变，服务会返回 `stale_cursor`，
应重新列举并把结果当作新的 live listing。

### `find_files`

Use when：需要在授权 root 内按文件名/相对路径 glob 找普通文件候选，并接受有界深度、
目录项和输出预算。

Do not use：需要文件正文、任意命令或正则表达式时；不要把用户给出的 OS 绝对路径拼进
`path`。模式使用 `/` 分隔组件，支持 `*`、`?`、字符类以及组件级 `**`；返回项始终是
规范相对路径。

完整参数示例：

```json
{
  "root_id": "project",
  "path": "",
  "pattern": "src/**/*.go",
  "page_size": 64,
  "max_depth": 8,
  "max_entries": 4096,
  "case_sensitive": true
}
```

成功结果使用与 `list_directory` 相同的 `coverage`、`warnings`、`budget` 和
`continuation` 字段，并额外回显受校验的 `pattern`。例如达到深度时会返回
`complete:false` 和 `warnings:["depth_limit"]`；这表示仍有未扫描范围，不应当当作
“没有匹配文件”。

### `search_text`

Use when：需要在授权普通文件中查找 UTF-8 literal，并需要文件相对路径、1-based 行号、
0-based byte 位置和有界行文本/上下文。

Do not use：需要正则、二进制内容、无限长日志尾部或一次性读取整棵仓库时。本增量不
开放 regex；`query` 必须是有效 UTF-8 且不能含换行，NUL 或非法 UTF-8 文件会被跳过并
在 coverage/warnings 中显式标记。`globs` 仅筛选文件路径，不改变 literal 查询。

完整参数示例：

```json
{
  "root_id": "project",
  "path": "src",
  "query": "TODO:",
  "globs": ["**/*.go"],
  "page_size": 20,
  "max_depth": 8,
  "max_entries": 4096,
  "max_read_bytes": 8388608,
  "context_bytes": 256,
  "case_sensitive": true
}
```

匹配项示例：

```json
{
  "path": "src/main.go",
  "line": 17,
  "line_start_byte": 402,
  "match_start_byte": 415,
  "match_end_byte": 420,
  "line_text": "// TODO: replace this adapter",
  "context": "// TODO: replace this adapter"
}
```

`max_read_bytes`、目录项、深度、页数、打开文件/目录预算、取消和服务端超时都可能产生
不完整结果；此时返回 `complete:false`、相应 warning（如 `read_limit`、`scan_limit`、
`open_file_limit`、`open_directory_limit`、`time_limit`）和
`continuation`。继续调用时保持 query、globs、大小写和全部预算不变。游标保存有界
byte/line/KMP/UTF-8 状态，不缓存整行或整棵树。默认签名 key 是进程随机值，服务重启会
使游标失效；部署者可以通过受保护的 `SearchCursorKey` 提供跨重启 key，但 revision、
generation、撤权和过期仍会使旧游标失效。

## 九、R3 无进程环境发现（当前实现）

R3 增加了两个只读 MCP 工具：`get_environment` 和 `discover_tools`。它们不启动外部
进程、不调用 `where.exe`、不搜索进程环境中的 `PATH`，也不读取完整环境变量。工具的
可信候选由本地配置的 `environment_tools` 提供；配置层只保存逻辑 ID、精确候选文件和
候选目录，并把绝对路径、远程文件系统、symlink/reparse 等平台判断留给
`internal/environment`。候选路径不会从模型请求进入。
Windows 配置中的绝对候选路径允许使用 JSON 友好的 `/` 分隔符，发现层会先规范化为
原生 `\` 再执行绝对路径、UNC/device、ADS、reparse 和远程文件系统检查；这些路径形态
不会因此放宽。

`get_environment` 只接受空 JSON 对象，返回 MCP schema/version、请求 ID、粗粒度 OS、
架构和固定 capability 名称。`discover_tools` 只接受可选的 `logical_ids` 数组（最多
128 项，元素为 ASCII 逻辑 ID）；省略或空数组表示全部本地配置项。未知或重复 ID、
额外字段及非对象参数均返回不含原始输入值的 `invalid_request`。

远程响应始终使用 `LocalDiagnostics=false`，结果仅包含 `approved_logical_id`、
`exists` 和 `candidate_count`；`candidate_paths` 与诊断说明不会出现在 MCP wire 中。
没有配置环境工具时，`get_environment` 仍可用，`discover_tools` 返回空 `tools` 数组。
两个工具都标记为只读/幂等，但 annotation 只是客户端提示，不能替代 connection/profile
工具 allowlist。`tools/list` 和 `server_info.tools` 均按当前认证 connection 的 profile
过滤。CLI 启动时仅把已解析的本地配置转换为 `environment.ToolSpec`，不会从命令行接受
候选路径或诊断开关。

## 十、audit.v1（当前最小实现）

CLI 已接入本地 typed JSONL audit sink，默认目录为用户配置目录下的
`Local-Probe/audit`，也可用 `-audit-dir` 覆盖。启动时若 sink 无法初始化则不监听；目录/文件
权限、文件大小轮转和保留数均有界。普通事件使用有界异步队列，队列满时允许丢弃并将 sink
标记为 degraded；安全事件（当前为最终 HTTP 401/403 的 `auth.reject`）同步 write-through。

MCP 适配层当前记录 `auth.accept`、`mcp.list`、`mcp.call` 和 `mcp.result`。调用事件只记录
受校验的 action（工具名或 `unknown`）及计数/预算字段；参数、请求正文、响应正文、文件路径、
token、JWT、key 和错误 message 均不写入。typed error envelope 只提取有限的稳定 error code，
未知或格式错误的错误结果统一记为 `unavailable`；401/403 不会再由内部 MCP 事件重复记一条
security reject。

尚未完成的部分必须单独看待：当前没有 command、network-tunnel、policy、fs-search 事件生产者，
也没有运行时 sink 故障后的 fail-closed ingress、全局并发/线级配额或管理变更审计。audit 的
`Stats.Degraded` 可供后续 supervisor/GUI 读取，但目前不会自动拒绝已启动 listener 的新请求。
