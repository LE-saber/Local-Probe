# R12 公网 Relay 测试部署与运维

日期：2026-10-24。此文记录已部署、实测的只读中转增量，不代表完整远程操作已经交付。实际测试域名、角色凭据及 SSH 主机记录只保存在忽略目录 `.tools/r12-public-deploy/`，本文使用占位符。

## 已部署结构

```text
目标程序（主动出站 WSS） ─┐
                         ├─ 公网 TCP 8080 / TLS 1.3
Agent 桥接（主动出站 WSS）┘       ↓
                         local-probe-relay-edge
                                  ↓ 固定回环 HTTP / WebSocket
                         local-probe-relay :8792
                                  ↓
                         仅转发端点之间的内层 mTLS 密文
```

运行环境为用户授权的 Ubuntu 24.04 x86_64 服务器。原有 443 服务保持运行。仅为本项目开放 TCP 8080；未新建账号、安装系统包或修改其他服务。

| 项目 | 实际配置 |
|---|---|
| Relay 单元 | `local-probe-relay-r12.service` |
| TLS 前端单元 | `local-probe-relay-edge-r12.service` |
| Relay 监听 | `127.0.0.1:8792` |
| TLS 前端监听 | 仅公网 NAT 对应网卡的 IPv4 地址，端口 8080；不监听 Docker 网桥 |
| 运行用户 | 既有 `nobody:nogroup`，不是 root |
| 二进制/脚本 | `/opt/local-probe-relay-r12/` |
| 私有配置/外层证书 | `/etc/local-probe-relay-r12/`，root 私有目录 |
| 凭据交付 | systemd `LoadCredential`；角色令牌从 root 私有环境文件读取 |
| 单元限制 | `NoNewPrivileges`、只读系统目录、私有临时目录、192 MiB 内存、64 tasks、1024 文件句柄 |
| 日志 | 不记录 Authorization 或数据帧；每单元 30 秒最多 20 条日志 |
| TCP 分段 | 当前前端仅对自身 listener 设置 `TCP_MAXSEG=1100`；未调整系统路由/MTU或全局防火墙策略 |

前端仅转发 GET `/healthz`、`/v1/target`、`/v1/agent`，拒绝 query 和请求体；固定上游必须为回环 HTTP，不提供任意地址代理。中转角色认证与目标端配对授权互相独立。

外层 TLS 在 Relay 前端终止，因此服务器能看到角色准入信息及连接元数据。工具调用、文件内容和结果仍由目标与 Agent 的内层 TLS 1.3 双向认证加密；Relay 不持有这些端点的签发私钥。不能将外层 CA 与端点配对 CA 混用。

## 部署脚本与客户端

[部署脚本](../deploy/relay/bootstrap-test-server.py)只管理自身标记的目录和单元，拒绝覆盖不属于本项目的配置；更新单元前保存备份。脚本不会配置防火墙。已有证书和凭据被保留，运行脚本不等于证书续期。

先交叉编译并校验 Linux amd64 二进制，将文件上传为候选文件，核对 SHA-256 后原子重命名。不要用 scp 直接覆盖正在执行的二进制。部署参数示意：

```text
python3 /opt/local-probe-relay-r12/bootstrap-test-server.py \
  --host <实际测试域名> --port 8080 \
  --bind-address <公网NAT对应网卡的本机IPv4> \
  --tcp-max-segment 1100 \
  --relay-sha256 <已验证的Relay哈希> \
  --edge-sha256 <已验证的前端哈希> --update-owned-units
```

替换二进制后，需要显式重启对应单元；脚本只有在单元内容改变时才自动重启。首次部署会 enable/start 两个单元，服务器重启后也会启动。此次主机上的确切 8080 iptables 放行规则为运行期规则，未安装全局持久化工具；云端规则由用户放行。重启后仍需复核公网入站，而不能只检查服务 active。应定期检查证书和磁盘。

目标端和 Agent 都需要明确配置可信公开 CA：

```text
local-probe-target -store <目标私有身份目录> -config <目标配置绝对路径> \
  -audit-dir <私有审计目录> \
  -relay-endpoint wss://<实际测试域名>:8080/v1/target \
  -outer-ca-file <公开外层CA的绝对路径>

local-probe-agent -store <Agent私有身份目录> -transport relay \
  -endpoint wss://<实际测试域名>:8080/v1/agent \
  -outer-ca-file <公开外层CA的绝对路径>
```

目标端读取 `LOCAL_PROBE_RELAY_TARGET_TOKEN`，Agent 读取 `LOCAL_PROBE_AGENT_ACCESS_TOKEN`。两者使用服务器路由中对应角色的不同凭据；不要放入命令行参数、截图或日志。配对与授权另按[配对说明](R12_PAIRING_AND_TRANSPORT.zh-CN.md)完成；仅有 Relay 凭据不能读取文件。

## 实际故障、修复与证据

1. 最初 TCP 8080/8443 均不可达。用户授权且放行 TCP 8080 后继续测试；没有占用或替换原有 443 服务。
2. 前端通配地址监听导致已有 Docker 容器的明文 HTTP/2 流量误连 TLS 端口，并持续产生错误日志。将本服务限制为公网 NAT 网卡地址，另加自身日志限流；最终最近两分钟 TLS 错误为 0。未修改该容器。
3. 公网 TCP 能连接但 TLS 超时；抓包只记录 TCP 元数据，观察到服务端握手数据重传。调整本服务 socket MSS 后，保持默认 TLS 密钥交换、域名和 CA 校验的公网测试在约 1.6–1.8 秒内完成。此证据支持路径分段兼容问题；没有确认是哪一跳设备丢包，也未通过降低 TLS 版本或跳过证书检查解决。
4. 首轮公网连续三次测试结果为 PASS/FAIL/FAIL。定位到等待配对的目标 WebSocket 没有读循环，断开后旧槽要等 30 秒才释放。新增从注册到转发始终唯一的目标读循环与有界消息队列，断开即清理槽；不自动重放 Agent 操作。新增断开等待目标的回归，race 重复 30 次通过。
5. 修复后的 Relay 已原子部署并重启；临时 SSH 本地转发已关闭。真实公网 race 测试连续三次 PASS，耗时分别为 1.67、1.63、1.62 秒。外层角色混用/错误凭据拒绝、内层 mTLS、真实合成文件读取、跨 root 拒绝、未登记证书拒绝均通过。
6. Linux 主机实际执行前端测试二进制通过：listener MSS 的内核读取值、WebSocket 双向转发、角色认证及固定上游约束。不是仅凭交叉编译判断 Linux 可运行。
7. 等待槽修复后的最终源码全项目 `go test ./...`、`go test -race ./...`、`go vet ./...` 均 PASS（正常主机账号、Go 1.26.5）。cmd/internal/deploy/docs 的 Gitleaks 脱敏扫描未发现命中，`git diff --check` 与相关文档链接检查通过。未执行默认跳过的隔离实验，不据此扩张执行能力。

公网用例是 `internal/relay/public_test.go` 的 `TestPublicRelayReadOnlyEndToEnd`。仅在显式提供端点、私有测试凭据路径和公开 CA 路径时运行。可选的 SSH 诊断目的地址会明确标记“不是直接公网证据”；最终三次测试未使用该选项。

本次 Agent 和目标测试端点都在同一 Windows 电脑，Relay 在真实公网 Linux 服务器；没有访问用户工作目录。该结果证明真实跨机中转与加密只读链路，不等于两台不同电脑上的 Agent/目标完整操作验收。

部署二进制 SHA-256：

```text
local-probe-relay       12ec97f38b0a2c9fad366a0ec643289b676a921e708fa0db14d86d3b2cc7a3af
local-probe-relay-edge  bc7742f43df048897853c26084b948e5ea6d055ec0bdc5322cdc0abbdf548b03
```

## 运维、有效期与停止

测试证书没有安装到操作系统信任库，客户端必须显式提供公开 CA，并保持主机名验证。当前叶证书到期为 **2026-11-07 04:23:44 UTC（北京时间 12:23:44）**，测试 CA 到期为 **2026-11-23 04:23:44 UTC**。当前没有自动续期；到期前需重新签发或接入正式证书流程。不能将当前测试部署作为无人维护的长期服务。

检查状态（不会输出凭据）：

```sh
systemctl is-active local-probe-relay-r12.service local-probe-relay-edge-r12.service
openssl x509 -in /etc/local-probe-relay-r12/edge-cert.pem -noout -dates
```

停止并取消开机启动本项目服务（按需由维护者执行，本轮没有停止）：

```sh
systemctl disable --now local-probe-relay-edge-r12.service local-probe-relay-r12.service
```

单元和旧二进制有备份，但回滚时需同时核对 CLI 参数，旧前端不支持 `-tcp-max-segment`。不要简单恢复旧二进制而保留新参数。不要重置整台服务器防火墙；若撤销本项目规则，仅处理带 `local-probe-r12-relay` 注释的确切 TCP 8080 规则，云安全组由用户按实际归属维护。

最终检查磁盘约剩 2.8 GiB（使用率 93%）。本项目部署目录为数十 MiB；此前错误曾增加 journal，已有容器日志也占用空间。未清空或截断其他服务日志。正式持续运行前需另行安排日志留存和容量治理。

## 仍未交付

此部署不新增命令/脚本/build/test/文件写回能力。操作审批与作业账本、多连接管理、GUI 接入、凭据动态轮换、证书自动续期、监控与正式发布仍按 [R12 计划](R12_DUAL_MODE_REMOTE_IMPLEMENTATION_PLAN.zh-CN.md)继续。
