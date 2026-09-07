# 接口契约与实现边界

本文件区分**当前 readcore.v0** 与**计划中的 MCP 产品接口**。不应把 Go 内核接口注册到公网后宣称完成 Local-Probe。

## 一、当前调用契约

`Engine.ReadBatch(ctx, trustedScope, requests)` 接收一个已经由可信调用方决定的 Scope 和一组 Request。Scope 内部保存 connection、profile revision 和复制后的 roots 集合，不从模型 JSON 解码。当前没有实现认证 ingress、配置存储、动态撤权或生产文件打开器。

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

当前是 byte-range，不含按行定位、日志 tail、目录遍历和内容搜索。这些属于计划 P06。

## 六、一致性不是快照保证

打开后检查 expected_version，读取前后比较 Metadata。检测变化则丢弃该项正文。version.strength 可为 metadata 或 snapshot，值由可信 Source 提供。

metadata 是弱版本：相同 size/mtime 的内容修改可能无法检测，读取完成后文件也仍可能变化。未来 strong snapshot 必须有真实不可变副本或其他验证机制，不可仅重命名字段。多个文件读取也不是仓库事务快照。

当前 next_offset 是内核位置提示，**不是签名 MCP cursor**。生产 cursor 必须绑定 connection、profile revision、root、文件/查询、版本、过滤条件和过期时间，处理撤权和篡改；这一功能尚未实现。

## 七、未来 MCP 工具面

目标约十个高层工具：workspace_snapshot、list_directory、find_files、search_text、read_file、batch_read、get_environment、run_probe、git_status、git_diff。具体 schema 在 P05/P06 冻结，不把底层全部细节扔给模型。

统一产品响应还需 request_id、coverage、warnings、预算和 continuation。coverage 必须说明忽略、deny、编码、扫描上限和未支持类型，不能把部分扫描标成全量。原生 MCP 的 readOnlyHint 只描述工具性质，不替代本地权限控制。
