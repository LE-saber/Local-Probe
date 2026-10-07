# R7 索引候选证据记录

日期：2026-09-15

## 结论先行

R7 已完成第一版本地代码增量，但没有完成产品验收。`internal/catalog` 是
`ProductionReady=false` 的 bounded、local-only、内存 metadata candidate catalog；它未接入
MCP，默认关闭。它只能返回候选，最终必须经过当前 `policy.BoundScope` 和 `rootfs.Source`
的 live verify；要求完整性时仍使用 direct scan 或完整 reconcile。

当前对照的结论是：catalog candidate query 在局部 prefix 查询中更快，但逐候选 live verify
的成本远高于 direct prefix scan，端到端没有启用收益。因此当前产品路径继续使用 direct，
不引入 SQLite/FTS，也不把 catalog 结果解释为存在性或完整性证明。

## 已实现的测试边界

- `internal/catalog` 只保存 canonical root-relative path、类型和有限 metadata，不保存正文、
  绝对路径、句柄或可传输授权能力。
- catalog 绑定 connection/profile/revision；初始状态和 dirty 状态在完整 `Reconcile` 前
  fail closed，generation 变化会使旧查询失效。
- 查询页有界，cursor 和结果保持 local-only；查询会再次检查当前路径授权，但仍必须由调用
  方 live verify。
- `internal/search` 的合成 harness 支持 small、10k、100k、1m，完整消费 continuation，
  检查重复、遗漏、needle 计数、coverage 和显式页数上限。
- catalog comparison harness 从 complete direct manifest 建立候选，再对同一 prefix 做集合
  对照，并逐候选执行 rootfs reopen/metadata live verify。它是 test-only 对照，不是生产接线。

## Direct 10k 证据

以下是已运行的 10k direct harness 记录：

| 操作 | 扫描/打开 | 结果 | 页数 |
|---|---:|---:|---:|
| `find_files` | 扫描 10,000 项 | 返回 9,928 个文件 | 10 |
| `search_text` | 扫描 10,000 项，打开 9,928 个文件 | 命中 39 个 | 40 |

service-cold-same-process 约 67.3 秒，同一 fixture 的 warm repeat 约 7.2 秒。这里的
“cold”只表示服务进程内该基准阶段的冷路径，不表示清空了 Windows/OS 文件缓存；OS cache
状态未知。该证据包含完整 continuation，不是只跑第一页。

## Direct/Catalog 对照

small/10k/100k 对照已通过集合正确性、重复/遗漏检查和候选 live verify。以下性能数值来自
**切换到当前 8-repeat warm query 之前的单次 warm query 记录**，用于说明方向，不是当前
8-repeat 版本的新测量；8-repeat 修改后的大规模 timing 尚未重跑。

| 规模 | Direct prefix | Catalog candidate query | 比例 | Live verify | Manifest / build |
|---|---:|---:|---:|---:|---:|
| 10k | 17.6009 ms | 2.9996 ms | 0.1704 | 1,241 项，7.2202439 s | 228.1331 ms / 36.3883 ms |
| 100k | 101.8275 ms | 25.474 ms | 0.25017 | 6,233 项，38.4371078 s | 1.8794183 s / 612.6804 ms |

100k 观测 RSS 约为 direct manifest 50 MB→96 MB、catalog build 100 MB→158 MB。这是本机
阶段观测，受 Go allocator、测试 fixture、杀毒软件和 OS cache 影响，不是内存上限或发布承诺。

局部 query 比例低于 0.5，说明候选筛选本身可能较快；但 live verify 是强制步骤，端到端
耗时因此明显高于 direct。故不能依据 query 单项速度开启 catalog。

## 尚未完成与生产硬门

1. 1m direct/catalog 证据尚未运行；需要记录完整 continuation、延迟、内存、失败原因和真实
   磁盘类型，并清楚区分 service-cold、warm 与未知 OS cache。
2. physical root identity 和 ignore fingerprint 尚未成为 catalog 的生产绑定硬门。
3. watcher overflow 目前不能被当作完整状态；生产语义必须在 overflow、取消、并发 reconcile
   或 dirty 时禁用 catalog 并 direct fallback。
4. 必须有并发 reconcile 的 generation final check、严格 cancellation、变化目录/新文件遗漏、
   损坏记录和 live verify 失败回退证据。
5. 仍需验证撤权、deny、reparse/重命名及新旧 generation 的组合行为，并完成独立审查。

这些条件完成前，catalog 继续保持 local-only、`ProductionReady=false`、不接 MCP、默认关闭。
SQLite/FTS 也保持未实现，除非后续证据表明内存候选方案在满足完整性和端到端收益后仍有必要。

## 复现入口

默认测试使用小型 fixture，不创建大型持久文件。大规模测试必须显式指定规模和证据开关，
并受 harness 的页数与单页超时约束：

```powershell
$env:LOCAL_PROBE_SEARCH_HARNESS_SIZE='10k'
$env:LOCAL_PROBE_SEARCH_HARNESS_ALLOW_LARGE='1'
$env:LOCAL_PROBE_SEARCH_HARNESS_EVIDENCE='1'
go test -count=1 -run '^TestSyntheticSearchHarnessCorrectnessAndEvidence$' -v ./internal/search
go test -count=1 -run '^TestCatalogCandidateComparisonEvidence$' -v ./internal/search
Remove-Item Env:LOCAL_PROBE_SEARCH_HARNESS_SIZE
Remove-Item Env:LOCAL_PROBE_SEARCH_HARNESS_ALLOW_LARGE
Remove-Item Env:LOCAL_PROBE_SEARCH_HARNESS_EVIDENCE
```

这两个测试命令的输出属于本机证据；若规模、主机、磁盘或 harness 实现发生变化，必须重新
记录原始 JSON 日志，不得把本文件中的旧 timing 当作新环境的性能承诺。
