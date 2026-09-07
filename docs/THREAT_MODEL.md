# 威胁模型与发布闸门

## 当前状态

当前代码为无网络的 K0 读取内核和显式本地文件 demo。**没有生产级安全 root adapter、MCP 鉴权、Tunnel 或多账号隔离实现。** 以下是必须兑现的安全设计及验收条件，不是已通过的安全认证。

## 信任边界

可信：设备所有者的显式配置、受保护凭据、已认证 ingress 构造的 Scope、经审查的 Source/OS 打开器。

不可信：模型生成的参数、本地项目/README 中的指令、文件名与内容、用户上传的工具输出、网络请求、自报账号/profile 名、未验证的 cursor 和第三方二进制。

拥有本机管理员权限的攻击者、恶意内核或设备所有者主动授予错误 root，不在应用可独立解决的范围内；不能因此忽略常见 symlink/junction/路径竞争和秘密外泄。

## 主要攻击及约束

| 攻击 | 必须具备的防护 | 当前情况 |
|---|---|---|
| README 提示模型读取并外泄私钥 | deny policy 作用于读/搜/概览/命令；工具不自行扩权；数据外传提示 | 待 P04/P05；提示词不能代替 policy |
| 模型把 profile 改成 admin 或伪造 root | 可信 ingress 绑定身份，root/tool 检查，未知连接失败 | 内核只检查受信任 Scope；真实 auth 待实现 |
| `..`、UNC、ADS、symlink/junction swap | handle-relative root API、平台测试、特殊文件拒绝 | 仅词法限制已实现；路径隔离未完成 |
| A 账号使用 B 的缓存/游标/任务 | connection/profile revision 绑定，撤权失效，独立配额 | 待 P09，模拟 Scope 测试不算真实多账号 |
| 巨型文件/长行/超量请求拖垮进程 | 单项和整体预算、bounded concurrency、全局 admission、deadline | byte batch 预算已实现；全局调度/wire 限额待实现 |
| Git external diff、PATH 劫持、解释器环境注入 | 固定 action/executable/args、净化 env、禁止任意 shell、进程树终止 | 没有命令工具，待 P08 |
| localhost 管理页面被恶意网站访问 | loopback、认证、Host/Origin、CSRF；不通过 tunnel 暴露管理 API | 没有管理 HTTP 页面 |
| runtime key 在参数/日志/支持包泄漏 | secret reference、受保护存储、脱敏、admin/runtime 分离 | 没有存储或使用真实凭据 |
| index 漏掉新文件后声称搜索完整 | freshness/coverage/reconcile，必要时 live scan | 没有 index；计划 P11 证据后才决定 |
| 弱 metadata token 被当作强快照 | 标明 strength，强审查使用不可变来源，不承诺仓库事务 | 已明确弱版本限制，真实 snapshot 未实现 |

## 生产 Source 放行条件

先锁定受支持 Go 工具链，最低具备 os.Root 或采用经审查的等价句柄相对 API。不能将 `EvalSymlinks`、字符串 startsWith 和随后 Open 组合当作抗竞争安全边界。os.Root 也不是完整 OS sandbox，硬链接、挂载、特殊文件和本地管理员风险仍需具体策略。

Windows 必须测试 junction/reparse、ADS、drive-relative、UNC、保留名称、大小写与 Unicode；Linux 必须测试 symlink swap 和特殊文件。未测试平台不得通过跨编译获得“安全已验证”标签。

## 网络开放闸门

在生产 Source、ingress auth、policy、全局预算、完整响应限制、取消和日志脱敏没有通过对应测试前，不增加可远程访问的 listener。Tunnel 只是传输通道；必须单独处理本地 MCP 的认证与权限。模型调用的工具不得修改 roots/profiles/tunnel 配置或取得 key。

## 隐私边界

用户授权的文件片段会发送给 ChatGPT，因此不能宣称“源码永不离开本机”。只读权限仍可泄漏秘密；本地索引、诊断包和日志应默认不保存正文。最终用户需明确选择授权 roots，并按实际账户的数据政策决定是否传输。

## 独立审查

发布前由独立上下文的审查者获得需求、固定 commit、构建步骤和攻击测试目标；要求提供可复现反例或通过证据。本轮仅有实现者自测，未完成独立安全审查。
