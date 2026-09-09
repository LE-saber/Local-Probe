# 威胁模型与发布闸门

## 当前状态

验证记录更新：2026-09-09。

当前代码为无网络的 K0 读取内核、显式本地文件 demo，以及 P04 的部分安全边界实现。已有 `config`/`policy.BoundScope`、基于 Go 1.25+ `os.Root` 的只读 rootfs 和 `readcore` bound adapter；**P04 的 Windows/WSL2 平台有界验证已完成，但没有真实 MCP 鉴权、Tunnel 或多账号产品链路，P01/P05 仍未实测，P04 仍未作为发布闸门放行。** 以下仍是必须兑现的安全设计及验收条件，不是已通过的安全认证。

## 信任边界

可信：设备所有者的显式配置、受保护凭据、已认证 ingress 构造的 Scope、经审查的 Source/OS 打开器。

不可信：模型生成的参数、本地项目/README 中的指令、文件名与内容、用户上传的工具输出、网络请求、自报账号/profile 名、未验证的 cursor 和第三方二进制。

拥有本机管理员权限的攻击者、恶意内核或设备所有者主动授予错误 root，不在应用可独立解决的范围内；不能因此忽略常见 symlink/junction/路径竞争和秘密外泄。

## 主要攻击及约束

| 攻击 | 必须具备的防护 | 当前情况 |
|---|---|---|
| README 提示模型读取并外泄私钥 | deny policy 作用于读/搜/概览/命令；工具不自行扩权；数据外传提示 | 待 P04/P05；提示词不能代替 policy |
| 模型把 profile 改成 admin 或伪造 root | 可信 ingress 绑定身份，root/tool 检查，未知连接失败 | 内核只检查受信任 Scope；真实 auth 待实现 |
| `..`、UNC、ADS、symlink/junction swap | handle-relative root API、平台测试、特殊文件拒绝 | `os.Root`、root/path 校验、逐组件 symlink/reparse 拒绝、Windows handle identity/hardlink/junction、Unicode/组合字符、长路径、ADS/保留名/非法路径、Windows symlink+junction swap、WSL2 FIFO/socket/hardlink/symlink swap 及临时 loopback SMB `GetDriveTypeW=4` remote-root 测试均已在明确环境/次数下通过；本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台和独立审查仍是残余风险 |
| A 账号使用 B 的缓存/游标/任务 | connection/profile revision 绑定，撤权失效，独立配额 | 待 P09，模拟 Scope 测试不算真实多账号 |
| 巨型文件/长行/超量请求拖垮进程 | 单项和整体预算、bounded concurrency、全局 admission、deadline | byte batch 预算已实现；全局调度/wire 限额待实现 |
| Git external diff、PATH 劫持、解释器环境注入 | 固定 action/executable/args、净化 env、禁止任意 shell、进程树终止 | 没有命令工具，待 P08 |
| localhost 管理页面被恶意网站访问 | loopback、认证、Host/Origin、CSRF；不通过 tunnel 暴露管理 API | 没有管理 HTTP 页面 |
| runtime key 在参数/日志/支持包泄漏 | secret reference、受保护存储、脱敏、admin/runtime 分离 | 没有存储或使用真实凭据 |
| index 漏掉新文件后声称搜索完整 | freshness/coverage/reconcile，必要时 live scan | 没有 index；计划 P11 证据后才决定 |
| 弱 metadata token 被当作强快照 | 标明 strength，强审查使用不可变来源，不承诺仓库事务 | rootfs 已生成 size/mtime/mode 弱 token 并标记 metadata；真实 snapshot 未实现 |

## 当前已验证的 P04 部分实现

- `config`/`policy.BoundScope` 已覆盖配置上限、重复 key、显式 `enabled` 语义、connection/profile revision 和 root/path deny；deny 支持 Windows 大小写不敏感匹配与 `**`。
- rootfs 从配置快照持有 `os.Root`，只读打开普通文件，逐组件拒绝 symlink/reparse；handle metadata 纳入 Windows volume/file identity、link count 和 attributes，hardlink/junction/reparse 拒绝测试通过；`BoundScope` adapter 让 Engine 的 Open、Metadata、ReadAt 都重新检查撤权。Source revision 也阻止 root ID 复用时旧 Source 接受新 scope。
- root 配置拒绝 UNC，并使用 `GetDriveTypeW` 拒绝 remote drive；临时 `New-PSDrive -Name Z -PSProvider FileSystem -Root '\\localhost\C$' -Persist` 映射中返回 4，`TestWindowsRejectsRemoteMappedRoot` 通过，随后 `Remove-PSDrive -Name Z -Force` 清理且 PSDrive/LogicalDisk/`net use` 均无 `Z:`。identity 查询错误 fail-closed 为 `ErrUnavailable`，不降级到 `m0` 绕过 hardlink/reparse 检查。
- Windows amd64 / Go 1.26.0 已通过 rootfs Unicode/组合字符、长路径、ADS/非法路径、symlink+junction swap（普通 `-count=5`、race `-count=1`）测试；WSL2 Ubuntu-22.04（Linux 6.6.87.2、Go 1.26.2，非裸机）已通过 FIFO/socket/hardlink/symlink swap（普通与 race 各 `-count=5`）及 `go vet ./...`。Linux rootfs 交叉编译也通过。上述均为实现者自测/编译证据，不是独立安全审查；详细命令和结果见 `docs/evidence/P04-platform-verification.json`。

## 生产 Source 放行条件

先锁定受支持 Go 工具链，最低具备 os.Root 或采用经审查的等价句柄相对 API。Windows 与 WSL2 的特殊文件、Unicode/long path、ADS/非法路径、symlink/reparse swap 和临时 loopback SMB remote-root 有界测试已通过；这只表示明确环境和次数下的实现者证据，P04 仍未作为发布闸门放行。仍需处理本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台，并完成独立安全审查。不能将 `EvalSymlinks`、字符串 startsWith 和随后 Open 组合当作抗竞争安全边界。os.Root 也不是完整 OS sandbox，硬链接、挂载、特殊文件和本地管理员风险仍需具体策略。

Windows 的 handle identity、hardlink、junction/reparse、UNC、ADS/非法路径、Unicode/组合字符、长路径和 symlink+junction swap 测试已通过；临时 `Z:` loopback SMB 映射的 `GetDriveTypeW=4` remote-root 测试也通过并完成清理。WSL2 Ubuntu-22.04 已实测 FIFO、socket、hardlink、symlink swap 及 race/vet。上述平台证据仍不覆盖本地管理员主动竞态、裸机/非 NTFS/其他 Unix 平台，也不因交叉编译而扩大为“所有平台安全已验证”。

## 网络开放闸门

在生产 Source 的全部 P04 闸门、ingress auth、policy、全局预算、完整响应限制、取消和日志脱敏没有通过对应测试前，不增加可远程访问的 listener。当前真实 auth、MCP、Tunnel 和 P01 仍未完成。Tunnel 只是传输通道；必须单独处理本地 MCP 的认证与权限。模型调用的工具不得修改 roots/profiles/tunnel 配置或取得 key。

## 隐私边界

用户授权的文件片段会发送给 ChatGPT，因此不能宣称“源码永不离开本机”。只读权限仍可泄漏秘密；本地索引、诊断包和日志应默认不保存正文。最终用户需明确选择授权 roots，并按实际账户的数据政策决定是否传输。

## 独立审查

发布前由独立上下文的审查者获得需求、固定 commit、构建步骤和攻击测试目标；要求提供可复现反例或通过证据。本轮仅有实现者自测，未完成独立安全审查。
