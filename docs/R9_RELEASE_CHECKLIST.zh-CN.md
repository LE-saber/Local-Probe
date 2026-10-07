# R9 发布与独立审查清单

日期：2026-09-16

本清单是发布门禁和复现入口，不是发布公告。当前结论固定为：
`production_ready=false`、`release_ready=false`，不应据此制作或分发稳定版。R9 的本地
`releasecheck` 与 Windows acceptance 只能整理证据，不能替代供应链材料、真实运行证据或
独立安全审查。

## 状态定义

每一项必须独立记录四种状态，不能用一个“完成”覆盖它们：

| 状态 | 含义 | 当前 R9 规则 |
|---|---|---|
| `implemented` | 代码、脚本或材料已经落盘 | `internal/releasecheck` 与 `scripts/acceptance.ps1` 已实现；workflow 若存在也只算入口定义 |
| `self-tested` | 在指定环境按复现命令实际运行并保存了结果 | 由本轮主代理在实际命令后填写；代码存在、静态阅读或 workflow 存在不算通过 |
| `independently-reviewed` | 不同上下文的审查者依据固定 commit、威胁模型和反例完成审查 | 当前未完成 |
| `release-ready` | 所有适用发布、运行、安全和支持门均满足 | 当前固定为否；所有当前 acceptance 报告必须为 `release_ready=false` |

## 已实现的本地增量

### `internal/releasecheck`

- `ProductionReady=false` 是固定常量；该包没有签名、SBOM、CVE、网络、进程或文件写入副作用。
- 证据只能由进程内 typed `Evidence` 构造；模型、网络请求和 JSON 不能制造或反序列化证据。
- required evidence 使用固定 vocabulary：
  `authorization_bypass`、`silent_truncation`、`budget_unbounded`、`command_bypass`、
  `credential_leak`、`scope_reuse`、`false_complete`、`unsigned_artifact`、`sbom_missing`、
  `license_missing`、`provenance_missing`、`install_matrix_missing`、`soak_missing`、
  `migration_rollback_missing`、`cve_scan_missing`、`support_workflow_missing`。
- `duplicate_evidence` 与 `status_conflict` 是额外固定 hard failures；不允许自由文本替代
  code，也不能靠 `release_ready=true` 覆盖 hard failure 或 pending。
- `Report` 使用 private snapshot；getter 返回副本，JSON projection 有界且拒绝输入反序列化。
  `Evaluate` 重新计算 aggregate `release_ready`。
- `commit=unknown` 会强制 `provenance_missing` hard/pending，即使输入 evidence 声称 pass
  也不能放行。

### `scripts/acceptance.ps1`

- 仓库根目录由脚本位置固定，不能把检查重定向到另一棵树。
- 支持 `-Quick` 和 PowerShell `-WhatIf`；`-Json` stdout 有最大字节数，输出 schema 为
  `r9.windows_acceptance.v2`。
- tracked safety policy 只枚举 Git tracked path 名称；识别为 fixture 的条目只读取有限
  metadata，不读取文件正文。
- `.runtime`、名称中含 key/token/secret 的 tracked 路径和超过上限的 tracked fixture 会
  触发 hard failure；脚本不创建百万文件 fixture。
- 普通模式可执行本地 repository boundary、Git 状态、Go test/race/vet、Windows cross-build
  和 `git diff --check`；Quick 跳过 race/cross-build；WhatIf 不执行检查。
- real signing、Tunnel connectivity、ChatGPT/web flow、install/update/uninstall、dual-
  connection soak、1m fixture 都是明确的 `SKIP`/非自动化 gate。
- 无论普通、Quick、WhatIf 或异常兜底报告，`release_ready` 都固定为 `false`；
  `local_automated_checks_passed` 只表示本地检查集合，不是发布结论。

## 本地复现命令

以下命令在 Windows PowerShell 中运行，命令不会把 key、token 或 JWT 写入输出。普通预检的
实际 PASS/FAIL/SKIP 结果由主代理在本轮运行后补入状态记录；本清单不预填未运行的结果。

```powershell
Set-Location '<clone-root>'

go test -count=1 ./internal/releasecheck
go test -race -count=1 ./internal/releasecheck
go vet ./internal/releasecheck
git diff --check

# 普通本地 acceptance；必须解析为 release_ready=false
$normalJson = & pwsh -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass `
  -File .\scripts\acceptance.ps1 -Json
if ($LASTEXITCODE -ne 0) { throw 'normal acceptance returned non-zero' }
$normal = $normalJson | ConvertFrom-Json
if ($normal.release_ready -ne $false) { throw 'release_ready must remain false' }

# Quick：只缩短本地检查集合，不改变发布结论
$quickJson = & pwsh -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass `
  -File .\scripts\acceptance.ps1 -Quick -Json
if ($LASTEXITCODE -ne 0) { throw 'Quick acceptance returned non-zero' }
$quick = $quickJson | ConvertFrom-Json
if ($quick.release_ready -ne $false) { throw 'Quick release_ready must remain false' }

# WhatIf：不访问仓库、不运行 Go/Git 命令，只生成有界跳过报告
$whatIfJson = & pwsh -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass `
  -File .\scripts\acceptance.ps1 -WhatIf -Json
if ($LASTEXITCODE -ne 0) { throw 'WhatIf acceptance returned non-zero' }
$whatIf = $whatIfJson | ConvertFrom-Json
if ($whatIf.release_ready -ne $false) { throw 'WhatIf release_ready must remain false' }
```

如需复现脚本的 tracked safety 行为，只查看名称和 fixture metadata，不要将真实密钥或大
夹具加入仓库：

```powershell
git ls-files --cached --full-name
pwsh -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass `
  -File .\scripts\acceptance.ps1 -Json
```

脚本输出可以保存到临时目录供人工审阅，但临时报告不是发布产物；报告中的 `information`
只允许安全的 branch/head/Go version 摘要，不能扩展成凭据或完整错误输出。

若 `.github/workflows/release-preflight.yml` 随变更提交，需在 GitHub Actions 中记录真实 run
的 commit、环境、结果和报告摘要；workflow 文件存在或被本地交叉构建通过，不能代替实际 CI
run。CI 仍应验证 `release_ready=false`，而不是把本地自动检查改成发布授权。

## 2026-09-16 本轮自测记录

主代理已在 Windows 本机执行一次普通（非 `-Quick`）acceptance。结果为 10 PASS、0 FAIL、
6 SKIP、0 hard failure、0 hard skip；`go test ./...`、`go test -race ./...`、`go vet ./...`、
`git diff --check` 与 Windows amd64 cross-build 均通过。报告中的
`repository_verified=true`、`local_automated_checks_passed=true`、`release_ready=false`。

跳过项为 real signing、Tunnel connectivity、ChatGPT/web flow、install/uninstall、
dual-connection soak 与 1m fixture。该结果只把 R9 的 `self-tested` 标记为本机自动检查通过，
不改变 `independently-reviewed=否` 和 `release-ready=否`。GitHub Actions workflow 尚未在
远端实际运行，因此也没有 CI 通过声明。

## 发布硬门与当前阻塞

以下任一项缺失都不能进入 release-ready：

| 门 | 当前状态 |
|---|---|
| 根 `LICENSE`、`NOTICE`、第三方依赖/许可证清单 | 阻塞；仓库当前没有这些根级材料 |
| SBOM、CVE 扫描报告、签名/Authenticode、provenance | 阻塞；未形成可复核的发布证据 |
| portable package 与干净 Windows 安装 | 阻塞；没有可发布包 |
| migration、rollback、崩溃恢复 | 阻塞；未完成 |
| install/update/uninstall 矩阵 | 阻塞；acceptance 脚本只明确跳过 |
| 至少一小时双 connection 只读 soak 与故障隔离 | 阻塞；未完成 |
| 独立安全审查 | 阻塞；当前只有实现者/Luna 的实现和本地审查记录 |
| R4–R8 production gates | 阻塞；WFP/broker/capability、可信 launcher/runtime、MCP/Tunnel health、生产 CLI/桌面接线等未完成 |
| 支持流程与失败升级路径 | 阻塞；未完成 |

R7 的 1m/百万文件测试按 2026-09-16 用户决定不运行，不作为 R9 入口条件，也不应通过创建
大规模 fixture 来“补齐”报告；R7 catalog 仍为 `ProductionReady=false`、local-only、默认
关闭，必须 live verify。1m 未运行不能被报告写成通过，也不会改变 R9 上述硬门。

## 证据记录模板

提交 R9 审查材料时，为每个 gate 保存以下最小信息：

```text
gate: <fixed code or named gate>
commit: <verified commit, never an unverified free-form value>
implemented: yes|no
self-tested: yes|no|pending
independently-reviewed: yes|no|pending
release-ready: yes|no
evidence: <脱敏文件路径或 CI run reference>
limits: <环境、次数、预算和未覆盖范围>
```

不能用“所有测试通过”“workflow 已存在”或单次 Business“极高”+Cloudflare 成功调用替代
缺失的供应链、安装、soak、独立审查和生产接线证据。当前 R9 的最终报告必须继续保持
`release_ready=false`。
