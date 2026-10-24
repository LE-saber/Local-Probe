# R12 本地操作后端验证证据

日期：2026-10-24。环境：Windows amd64，Go 1.26.5。分支 `feat/r12-dual-mode` 工作树开发构建；没有提交、推送或发布。

## 最终检查

| 检查 | 结果 |
|---|---|
| `go test ./...` | PASS |
| `go test -race ./...` | PASS |
| `go vet ./...` | PASS |
| Windows 目标/Agent/配对/本地控制 CLI 构建 | PASS，独立输出目录 |
| Linux amd64 Agent/配对/默认只读目标交叉编译 | PASS；未在新增 Linux 电脑运行 |
| cmd/internal/deploy/docs 的 Gitleaks 脱敏扫描 | 未命中 |
| `git diff --check`、相关文档相对链接 | PASS |

完整日志在 Git 忽略目录 `.tools/r12-operations-finalized-{test,race,vet}.log`。最终构建为 `bin/r12-operations/{windows-amd64,linux-amd64}/`，各有 `SHA256SUMS.txt`。

## 实际功能验证

- Python 3.13.1、Node v24.19.0、PowerShell 7.6.5、cmd 实际版本与批准脚本全部 PASS。脚本实际创建临时文件。cmd 额外 shell 参数在准备阶段拒绝。
- Go 合成项目真实 `go build` 产出 EXE，真实 `go test` 执行单元测试 PASS。没有下载依赖或安装新工具。
- 本机官方 MCP SDK、Agent 桥接、WSS、内层 mTLS：工具发现、真实执行策略声明、申请、等待本地批准、文件提交、状态查询、冲突拒绝和配对撤销通过。
- 独立本地 CLI EXE 在目标运行期间 inspect/approve；没有 acknowledgement 时拒绝，批准后实际写入。
- 不批准/拒绝/取消不产生工作文件副作用；相同 nonce 不重复执行，同 nonce 不同计划拒绝。任务改变文件后，重复 prepare 仍返回原 ID。
- 两个连接实际同时运行进程，跨主体取消拒绝，取消一个不影响另一个。本地所有者也能不依赖 Agent 取消运行作业。
- 进程环境不继承未批准标记；输出超限终止；取消和父进程正常退出后，核对 Job 内进程列表为空。专门测试不继承输出管道的子进程，避免仅靠 EOF 推断子树已经停止。
- Windows Job 内核读取证明 32 进程、1 GiB 总提交内存和累计用户态 CPU 时间限制已设置。不是网络/文件沙箱证明。
- v1 拒绝可写 profile，v2 显式可写权限往返保留，默认只读不变；只读 profile 在操作层与写入核心均拒绝写入。
- 文件身份/内容冲突、硬链接、路径穿越、deny、备份失败、独占创建与精确补丁上下文不唯一均拒绝；用户期间修改保持不变。
- 存储损坏拒绝、单引擎 OS 锁、审批记录完整原子发布、共享审批目录拒绝、目标重启 unknown/interrupted 且不重跑通过。

真实运行器为 opt-in 检查，设置 `LOCAL_PROBE_OPERATIONS_REAL_TOOLS=1` 和四个明确的运行器路径环境变量，再执行：

```text
go test -race ./internal/operations -run 'Test(InstalledPython|RealGoBuild)' -count=1 -v
```

最终一次真实 Go build/test 为 14.77 秒；之后的 cmd 参数准入补充检查也重新运行四种已安装运行器，全部 PASS。常规回归默认跳过这些 opt-in 场景，不能将默认 skip 写成实测。

## 失败保留及边界

初版 race 测试 EXE 作为子进程夹具因净化环境缺 DLL，以 `0xc0000135` 退出；改用 CGO=0 的独立合成 helper，没有继承全部宿主环境。相同配置内容的新 revision 最初未撤销旧审批，已将 revision 纳入 epoch 并通过回归。SDDL 字符串对比最初误拒绝合法私有目录，现按实际控制位及 ACE 验证；合法与共享目录测试同时保留。

已有文件采用锁定句柄更新并先保存私有 before image，不是原子替换；写入失败/崩溃可能部分更新，状态明确 unknown，需人工核对恢复。本轮未声称实现多文件事务。

没有访问用户真实项目文件，没有新建账号，没有修改系统网络策略，没有连接用户新增 Linux 电脑或公网 Relay，也没有替换 GUI。Linux 作为 Agent 的实机与真实 Relay 操作验收安排在后续开发完成后。

在线规则/选择性授权 epoch、完整多连接宿主、GUI/Web 高级入口仍未完成，详见[操作后端说明](../../R12_OPERATIONS_BACKEND.zh-CN.md)。
