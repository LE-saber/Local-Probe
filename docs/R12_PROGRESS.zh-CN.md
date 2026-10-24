# R12 开发进展与验证

初始日期：2026-10-23；最新更新：2026-10-24。工作分支：`feat/r12-dual-mode`。起点：`dd0a6b73981d8006403913e400d698c3b8fee291`。

本记录描述开发实现，不是已发布版本。R12 完整源码现通过 `feat/r12-dual-mode` 分支交付，英文入口见[开发指南](R12_DEVELOPMENT.md)和[当前状态](IMPLEMENTATION_STATUS.md)。后文“未提交/未推送”等措辞保留各轮历史状态；本次上传不替换现有 GUI/MCP/Tunnel，也不合并 main 或发布 Release。

## 2026-10-24：仓库源码与英文开发交接

- 将先前未提交修改和新增 R12 源码统一纳入交付分支，保留 MIT 与第三方许可证；同步 main 的四语言首页，英文为默认入口。
- 新增英文开发、GUI、操作及配对说明，更新中文接手文档，避免继续要求接手人只能索取本机快照。
- 本轮完整 test/race/vet 与24项前端VM复测通过；上传检查与未验收范围见[交付证据](evidence/r12/2026-10-24-source-upload.md)。未连接用户 Linux 或真实 Relay，未改权限/系统策略。

## 2026-10-24 第四增量：本地批准的真实操作后端

- 新增 `internal/operations`、`local-probe-operations` 本地控制 CLI、配对目标的可选操作引擎与八个 MCP 工具。支持逐次批准的命令、真实版本、四类脚本、build/test、文本创建/更新及精确补丁。
- v2 增加显式可写 profile，v1/默认 profile 及写入核心保持只读门。还需 trusted_host、开发者白名单、工具许可、本地目录表哈希及精确审批；远程 confirmed 无效。
- Windows 私有账本 DACL/DPAPI、OS 独占锁、完整密文原子发布；主体/连接/计划/配置版本绑定、幂等、取消、五分钟审批、60秒轮询租约、重启不自动重跑。
- 打开审批目录会核对实际 DACL，拒绝共享目录；新增本地所有者取消入口，停止任务无需依赖 Agent。失效/损坏存储立即关闭准入并取消活跃任务。
- 两连接实际并行、取消一方不影响另一方；全局两作业/每连接一作业。内核读取验证 32 进程、1 GiB 汇总提交内存和累计用户态 CPU 时间预算；不承诺网络沙箱。
- Python 3.13.1、Node v24.19.0、PowerShell 7.6.5、cmd 版本与脚本，以及真实 Go 编译/单元测试通过。本机官方 SDK/WSS/mTLS→申请→CLI批准→实际写入/冲突拒绝通过。
- 写入绑定身份/内容版本、规则、目录链、硬链接；保存 before image。已有文件为锁定句柄更新，非原子替换，异常明确 unknown 并保留人工恢复材料。
- 已准备 Windows 后端和 Linux Agent 构建；本轮未连接用户 Linux 或真实 Relay。操作目标仍限 Windows，GUI/Web 高级入口、在线规则、选择性 epoch 与完整多连接宿主尚未完成。
- 用法与限制见[操作后端说明](R12_OPERATIONS_BACKEND.zh-CN.md)。
- 最终源码完整 test/race/vet、Windows 构建与 Linux 交叉编译均 PASS；敏感信息扫描未命中，diff/文档链接通过。实际版本/脚本及 build/test 的 opt-in 检查单独运行通过，子进程退出列表核对与本地取消通过；见[验证证据](evidence/r12/2026-10-24-operations.md)。没有用先前只读中转结果代替本轮操作测试。

## 2026-10-24 第三增量：授权服务器上的公网 Relay 验证

- 经用户授权部署到既有 Ubuntu 24.04 x86_64 服务器，使用既有 nobody 账号；仅开放 Relay TCP 8080，保留原 443 与其他服务。新 TLS 前端只向固定回环 Relay 转发三个受限路由，采用 TLS 1.3、systemd 私有凭据、资源限制和单元日志限流。
- 限制前端只绑定公网 NAT 网卡，避免已有 Docker 的明文 HTTP/2 误连；仅对新 listener 设置 MSS=1100 后，公网 TLS 超时消失，未改变全局 MTU/路由或关闭证书校验。
- 公网重复测试发现旧等待槽未及时感知目标断开，首轮 PASS/FAIL/FAIL。修复为单一持续读循环和有界队列，断开立即释放；race 定向 30 次通过。修复部署后，关闭临时 SSH 转发，直接公网 race 连续三次 PASS（1.67/1.63/1.62 秒），覆盖真实文件读取、范围拒绝及内外两层认证。
- Linux 主机实际运行前端测试通过。两个单元都以 nobody 运行，原 xray 的 443 监听未变。当前是私有 CA 的短期测试证书，叶证书 2026-11-07 到期，没有自动续期。
- 最终修复源码完整 test/race/vet 均 PASS；cmd/internal/deploy/docs 脱敏敏感信息扫描未命中，diff 与文档链接检查通过。没有用修复前结果代替本轮最终检查。
- 部署、故障、哈希、验收范围及维护步骤见[公网 Relay 运维记录](R12_RELAY_DEPLOYMENT.zh-CN.md)。真实公网中转已经验证；Agent/目标仍在同一 Windows 电脑，不能称不同目标电脑的完整操作已经验收。命令/写入/GUI 等后续范围不变。

## 2026-10-24 第二增量：持久化配对、原生桥接与 Relay 只读闭环

### 已实现

- 新 `internal/deviceidentity`：随机设备/Agent 身份、公开 bundle/CSR 指纹、所有者签发、证书绑定设备/Agent/连接、公钥级终端撤销；授权文件与撤销标记只写一次，拒绝覆盖和损坏记录。
- Windows 新身份目录设置当前用户/SYSTEM 的受保护 DACL，记录以当前用户 DPAPI 加密；Unix 使用私有权限，不声称实现磁盘加密。目标运行时只加载叶密钥，不加载所有者 CA 签发私钥。
- Registry 将可信连接授权放入私有 context，逐请求复核；运行中撤销取消活跃租约，租约同时受证书到期约束。CLI 变更仍要求离线；GUI/IPC 撤销尚未接入。
- `local-probe-pair` 可在本地创建、检查、签发、安装、查看及撤销。签发要求已有、启用的 `agent_remote` 配置连接；公开输出不含私钥。
- `local-probe-agent` 是可运行的官方 SDK stdio 桥接，逐请求转发工具发现与调用，不缓存过期工具列表，不自动重放调用；支持 Tunnel WSS 与 Relay WSS。
- `local-probe-target` 将配对主体接入现有只读 MCP/policy/rootfs，认证入口与 bearer/Cloudflare 模式互斥；验证原有文件范围仍生效。支持回环 Tunnel origin，或 4 路主动出站 Relay 通道。
- 新 `local-probe-relay`：按静态路由绑定两种独立角色凭据，仅转发内层 TLS 密文；有界通道、等待超时、心跳、速率/消息限制、取消和关闭。当前只监听回环地址，公网 HTTPS/WSS 由用户受控反向代理提供。
- 新命令行程序支持 SIGTERM/中断退出、显式私有外层 CA 文件；不提供跳过证书验证选项。

### 实现调整与限制

首版 Relay 用按角色认证的静态 WSS 等待通道替代计划中的动态控制连接/票据接口；不提供动态路由或任意地址转发。两端仍必须独立通过配对的内层 mTLS 和目标授权。调整理由和完整操作顺序见 [配对与通道说明](R12_PAIRING_AND_TRANSPORT.zh-CN.md)。

R12-02 的离线身份/配对与运行期撤销 API 已实现；证书续期/轮换、管理 IPC/GUI、身份灾备仍未完成。R12-03/04 已有只读通道闭环，但缺少真实外部 Tunnel/公网/不同电脑验收和正式运维交付，不能标记整阶段完成。

### 本轮验证与故障

- 配对包/CSR 替换、修改连接元数据、重复签发、损坏记录、重启后撤销、活跃租约取消及并发签发测试通过。
- 官方 MCP 经直接 WSS 和 Relay WSS 的桥接通过；越范围读取返回 `denied` 且无文件内容；伪造 bearer 不能进入 peer ingress；角色混用、目标离线及任意目的地址被拒绝。
- Windows **真实目标 EXE + 真实 Agent EXE** 集成测试通过：官方 SDK `CommandTransport` 通过 stdio 连接 Agent，经本机 HTTPS Relay 和内层 mTLS 读取合成工作区的实际文件。这是同机多进程证据，不是跨机或公网验收。
- 公共配对 JSON 解码器 5 秒 fuzz 通过，237,185 次执行；嵌套深度/输入大小/重复和未知键受限。
- 本轮首个最终全量回归出现 peer 调用间歇 EOF。发现 WebSocket 适配器直接传递读取 deadline 与 Go HTTP 的后台读中断语义不兼容：Gorilla 会永久缓存读取超时。改用有界后台读取和独立可恢复读取 deadline，新增“超时后重置并成功读取”回归；实际 peer 文件测试在 race 下重复 100 次通过。没有用自动重试掩盖问题。
- 全量 race 又复现既有 workspaceadmin 暂停/恢复持久化失败；定向 300 次捕获底层 `replace configuration (os code 5)`。Windows 原子替换现在只对 access denied/sharing/lock violation 最多重试 16 次（5ms 间隔），保持 CAS 写锁、同一临时文件和原子替换，不修改 ACL。定向 race 300 次及实际替换字节校验通过；永久拒绝/未知错误仍失败。存储诊断只保留数字错误码，不输出原始路径或 OS 错误内容。
- 增加撤销持久化失败用例：不把目录读取失败误报为撤销成功；当前运行时即使磁盘恢复可读，也维持拒绝该主体，明确报告未持久化，不能承诺重启后的撤权。
- 一轮 vet 因工作区 `tmp/go-build*` 正被清理而扫描失败；后续将进程级 TEMP/TMP 改到 Go 忽略的 `.tools/r12-tmp`，不改系统环境，也不删除用户临时目录。
- 最终源码全项目 `go test ./...`、`go test -race ./...`、`go vet ./...` 均 PASS（正常主机账号，Go 1.26.5，临时目录 `.tools/r12-tmp`）。默认跳过的 opt-in 网络隔离实验和公网/跨机门仍未执行，不能据此称全部外部验收通过。构建识别和补充证据见 [本轮证据](evidence/r12/2026-10-24-transport.md)。

### 开发制品

`bin/r12/` 含 Windows pair/agent/target/relay 四个独立 EXE；另有 `bin/r12/linux-amd64/local-probe-relay`。Linux 产物仅交叉编译，未在 Linux 主机运行。它们是本地开发构建，不是完整发布包，未覆盖现有 GUI。

### 当前后续顺序

继续 R12-05 的操作计划/本地明确审批、持久化作业、幂等、取消、命令/脚本/build/test 与受控文件写回；随后接多连接/在线规则与 GUI。原先末尾“未完成与下一步”保留为第一增量的历史状态，以本节和最新源码为准。

## 已实现的第一增量

### R12-00：基线与执行契约

- 实际核对现有只读 MCP、单一桌面 controller 与关闭的隔离执行门。
- 用户明确允许新增 `trusted_host`，使用当前用户实际权限；保留旧 `isolated_required` 策略。[决策记录](R12_EXECUTION_DECISION.zh-CN.md)。这项授权持续有效，不需要再次询问同一产品选择。
- 使用仓库已有 Go 1.26.5 工具链，独立工作区缓存/临时目录，未更改系统 Go 或全局 Git 配置。

### R12-01：加密兼容性样机

新增 `internal/peertransport`：

- Gorilla WebSocket v1.5.3 二进制消息流适配 `net.Conn`，限制单消息 64 KiB，关闭发送压缩，保留网络 deadline。
- Go 标准 TLS 1.3 双向认证，正常 CA/主机名验证后核对预配对目标公钥；目标端验证 Agent 授权回调，关闭服务端会话票据。
- 每个 HTTP 请求重新检查主体撤销，不从 header/JSON 接受身份；可信主体以私有 context key 传递。调用方仍须绑定实际 connection/profile，不能直接把这个中间件当成文件授权。
- 官方 MCP Go SDK 通过真实回环 WSS + 内层 mTLS 完成初始化和工具调用；超过 64 KiB 的结果跨消息传输无损。
- 错误目标 pin、撤销 Agent、明文/伪造 header、文本消息和超大消息拒绝；错误流重复读取不会触发 Gorilla 的失败读取 panic。
- 新依赖来自已有缓存，经 Go 模块校验取得固定 checksum；增加独立 BSD 许可证。后续发布制品仍须重新生成第三方依赖清单。

这里的证书由测试临时生成，未安装到系统信任库。此样机不等于设备配对、生产 Relay 或公网通道已经交付。

### R12-02：配置基础（部分）

- 增加 `local-probe.config.v2`，支持 `purpose=web_local|agent_remote` 与 `execution_policy=isolated_required|trusted_host`。
- 旧配置仍使用 v1，默认 Web 用途和隔离要求，不自动改授权。
- v1 不接受新字段；v2 拒绝未知用途/策略；持久化往返保留选择。旧二进制按未知 schema 拒绝 v2，避免静默忽略新语义。
- MCP 初始化增加原生合法访问优先说明；没有把客户端名称或路径相同当作可靠的同机识别。

### R12-05：受信任执行启动基础（部分）

- 新增本地 `probe.RunTrustedProcess`，要求已审计可执行文件描述、SHA-256、明确 argv/cwd/env、超时与输出限额。
- 复用 Windows 挂起启动、执行映像核对和 Job Object；固定探针仍限制一个进程，独立受信任入口允许最多 32 个进程。
- 进程退出后清理后代；超时和输出超限终止作业；不继承全部宿主环境。当前不承诺 CPU/内存限额已完成。
- 实际启动测试 helper 验证正常返回、超时及输出超限的具体错误类型。这不是用户实际 Node/Python/build/test 全流程验收。
- 该入口尚未接入 MCP/GUI。审批、审计、连接授权、在线撤销和持久化作业必须由后续上层完成后才能对外开放；现有 GUI 的执行状态继续关闭。
- 非 Windows 平台明确返回不支持，没有临时使用不受控的 Unix launcher。

## 验证结果

环境：Windows amd64，Go 1.26.5；全部临时输入为合成工作区/测试 helper。没有新建账号或修改 WFP/防火墙规则。

| 检查 | 结果 |
|---|---|
| `go test ./...` | 正常主机账号环境下 PASS |
| `go test -race ./...` | 首轮出现既有连接切换测试失败；完整复测 PASS，见下面限制 |
| `go vet ./...` | PASS |
| `go test -race ./internal/desktopbridge -run TestDesktopHistoryPersistsIndependentScopesAndSelection -count=25` | PASS；增强失败输出，未改原断言 |
| config / peertransport 定向测试 | PASS |
| Windows TrustedHost 实际进程测试 | PASS，含正常运行/超时/输出超限 |
| `git diff --check` | PASS；只存在 Git 的 LF/CRLF 提示 |

最初测试环境存在 GOROOT 与执行工具版本不同、共享缓存拒绝访问以及临时目录问题，已通过进程级环境设置解决，没有修改全局环境。沙箱账号的既有 `TestCurrentAccountRestrictedDefaultDACL` 失败；正常主机账号完整回归通过，未放宽测试断言。

**仍须保留的失败记录**：第一次完整 race 回归中，`TestDesktopHistoryPersistsIndependentScopesAndSelection` 报告 selection mixed scopes。定向 25 次及第二次完整 race 均通过，尚不能确定首轮原因；新增详细失败投影以便追踪。不能将“复测通过”写成“根因已修复”。

## 未完成与下一步

1. R12-02：持久化设备身份、DPAPI/权限保护、配对包、CSR 签发、授权 epoch 与完整撤销。
2. R12-03：可运行的 stdio Agent 桥接与生产 Tunnel 适配。
3. R12-04：自建 Relay 控制面、数据面、票据、配额、恢复与 Linux 部署制品。
4. R12-05：不可变操作计划、每连接明确授权与本地审批、审计、作业账本、幂等、取消、真实脚本/build/test 和受控文件写入。
5. R12-06：多连接生产宿主、在线规则和公平调度；继续追踪连接切换偶发失败。
6. R12-07/08：后端封板后 GUI 接线、真实跨机与服务器验收、发布候选。

当前不具备“完整远程操作可用版”的条件。后续执行按 [技术实现书](R12_DUAL_MODE_REMOTE_IMPLEMENTATION_PLAN.zh-CN.md) 继续，不重新请求已经明确的受信任执行产品授权。
