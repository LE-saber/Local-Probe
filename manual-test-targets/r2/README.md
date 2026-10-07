# R2 Windows 手工靶场

本目录用于验证 R2 的行范围、日志尾部、工作区轮廓和安全边界。它只包含公开的
测试标记，不包含密码、令牌或真实业务数据。调用时使用配置中的 `root_id=project`
和相对路径；不要把本机绝对路径发送给 MCP。

## 靶场内容

- `crlf-utf8.txt`：CRLF 文本，包含中文和 emoji，最后一行没有换行符。
- `no-trailing-newline.txt`：UTF-8 文本，明确没有尾部换行符。
- `long-line.txt`：小型长行（约 4 KiB），用于验证扫描/输出预算，不是大文件压力测试。
- `manifest/go.mod`、`manifest/package.json`：用于工作区轮廓的 manifest 候选和语言统计。
- `manifest/src/main.go`：用于 outline、证据路径和语言统计。
- `denied/.env`：无敏感拒绝标记；配置应将 `manual-test-targets/r2/denied/**` 加入
  `deny_patterns`，因此它不应出现在 snapshot、tree、find 或 read 结果中。

## 建议调用顺序

1. `read_file(root_id=project, path=manual-test-targets/r2/crlf-utf8.txt, range={kind:lines,start_line:1,max_lines:2})`。
   预期保留 CRLF 字节，并返回 1-based `start_line/end_line`；不得替换成 `\n` 或返回绝对路径。
2. `read_file(root_id=project, path=manual-test-targets/r2/crlf-utf8.txt, range={kind:tail,tail_lines:2})`。
   预期返回最后两个记录，最后一行没有额外换行符。
3. 对 `no-trailing-newline.txt` 做 `range.kind=tail` 和旧版 `offset/max_bytes` 调用，确认
   旧输入仍能工作，并比较 `range_kind`、`complete`、`scanned_bytes`。
4. `workspace_snapshot(root_id=project, path=manual-test-targets/r2, max_depth=3, max_entries=64)`。
   预期只返回相对路径；应识别两个 manifest 和 `src/main.go`，并给出 `coverage`、`budget`
   与 `evidence_paths`。它不是强一致仓库快照，也不执行 manifest 或源码。
5. 读取 `manual-test-targets/r2/denied/.env`，或把该路径传给 snapshot/tree/find。
   预期为 `denied` 或被过滤，不得泄露内容、绝对路径或 cursor。
6. 将 `max_scan_bytes` 调小到不足以找到请求行，确认返回有界的预算错误/不完整结果，且不会
   自动读取整棵工作区。

Windows 终端可用十六进制查看器确认 `crlf-utf8.txt` 的 `0D 0A` 和最后一行的无尾部
`0A`。若 Git 的行尾策略改写了该夹具，可在 Windows 运行同目录的
`prepare-line-endings.ps1` 后再执行第 1 步；不要把行尾转换当成 Local-Probe 的解码行为。
