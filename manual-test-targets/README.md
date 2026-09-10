# Local-Probe 手工测试夹具

这个目录只包含可公开的测试文字，不包含密码、令牌或真实业务数据。默认运行时 root
应把 `manual-test-targets/denied/**` 加入 `deny_patterns`，因此 `denied/` 只用于
验证拒绝行为，不应出现在树、查找或搜索结果中。

## 可复制到 ChatGPT 的测试提示

以下提示假定 `root_id` 是 `project`，并且已连接当前 Local-Probe。每一条都可以单独
发送；不要把本机绝对路径填入参数。

1. `请调用 tree_directory，root_id=project，path=manual-test-targets，max_depth=4，page_size=4，max_entries=20。返回第一页后继续使用 continuation，直到 complete=true；不要读取 denied 子目录。`

   预期：返回扁平的相对路径和 `depth`，应看到 `pagination`、`nested`、`unicode` 等
   可见项；`manual-test-targets/denied` 不应出现在 `entries` 中。第一页应因为小
   `page_size` 返回 continuation，未完成页不能标记 `coverage.complete=true`。

2. `请调用 list_directory，root_id=project，path=manual-test-targets/pagination，page_size=3，max_entries=8，并用 cursor 继续下一页。`

   预期：`item-01.txt` 至 `item-08.txt` 分页返回，每页最多 3 项；最终页才可以完整。

3. `请调用 find_files，root_id=project，path=manual-test-targets，pattern="**/*.txt"，page_size=4，max_depth=5。`

   预期：找到分页目录、嵌套目录和 Unicode 目录中的普通文本文件；拒绝目录中的
   `拒绝目标.txt` 不得返回。

4. `请调用 search_text，root_id=project，path=manual-test-targets，query="LP_MANUAL_UNICODE_20260910"，globs=["**/*.txt"]。`

   预期：只返回 `unicode/中文目录/第二层/🌏-标记.txt`，并给出行号和字节位置。

5. `请调用 read_file，root_id=project，path=manual-test-targets/root-marker.txt，max_bytes=256。`

   预期：正文包含 `LP_MANUAL_ROOT_20260910=LOCAL_PROBE_ROOT_OK`。

6. `请调用 batch_read，读取 manual-test-targets/pagination/item-01.txt 和 manual-test-targets/empty.txt，各从 offset=0 读取最多 256 字节。`

   预期：第一项包含 `LP_MANUAL_ITEM_01_20260910`；第二项是空正文并报告 EOF，不能
   因为空文件报错。

7. `请调用 read_file，root_id=project，path=manual-test-targets/denied/拒绝目标.txt，max_bytes=256。`

   预期：返回 `denied`，不得返回文件正文或绝对路径。这个文件内容是无敏感的拒绝
   标记，仅用于确认策略生效。

不要要求执行 `ls`、`tree.exe`、PowerShell 或任意 Shell。`tree_directory` 是无状态的
目录视图，不会改变进程当前目录；下一次调用应继续显式传入 `root_id` 和相对 `path`。
