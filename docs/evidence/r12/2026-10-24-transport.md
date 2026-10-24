# R12 配对/传输后端开发证据

日期：2026-10-24；Windows amd64；Go 1.26.5；分支 `feat/r12-dual-mode`，未提交工作树，基准 HEAD `dd0a6b73981d8006403913e400d698c3b8fee291`。

## 本地证据

| 检查 | 结果与边界 |
|---|---|
| 身份/配对/撤销相关包 race | PASS；含替换与篡改、并发、损坏、重启和撤销存储失败 |
| `TestPairedAgentUsesExistingFileScopeAndRejectsMixedAuthentication` race ×100 | PASS；真实 rootfs 授权范围、独立目标设备 ID，非公网测试 |
| `TestWindowsRealCLIReadsFileThroughRelay` | PASS；启动真实目标/Agent EXE，由官方 SDK stdio 调用，经本机 HTTPS Relay 读取实际合成文件 |
| `TestReadDeadlineCanBeResetWithoutPoisoningWebSocket` | PASS；HTTP 后台读中断后可恢复，不依赖重试工具调用 |
| `TestUpdatePauseAndResumeAreProfileScoped` race ×300 | PASS；在 Windows 原子替换有限重试修复后运行 |
| `FuzzPublicPairingDecoder`，请求 5s | PASS；237,185 次执行；不是长期 fuzz 或安全审计结论 |
| Windows 四程序 build | PASS；本地开发构建，未打完整发布包 |
| Linux amd64 Relay build | PASS；只证明交叉编译，不是 Linux 运行证据 |

全项目 test/race/vet 的最终结果记录在 [R12 进展](../../R12_PROGRESS.zh-CN.md)。保留修复前的失败，不能用定向结果覆盖全项目失败。

## 最终开发产物 SHA-256

以下文件位于忽略目录 `bin/r12/`，没有进入 Git 源码或替换现有 GUI：

```text
9236B11B17221F928F3F384E97990D58F5D4C75C2F859AF8E491DCDFE0F66A1F  local-probe-agent.exe
ECEC83504312BE87ECCBF2F56DBD006CE054F176A98B75D7B2894596BC727C3A  local-probe-pair.exe
957350AE90474DA03BEBB2A91A21B177940DF28C27909DB68A5DA65C1956EF85  local-probe-relay.exe
533A3CCBD7C4819C96C79CF90F544D59F7ED29DFB75BD0BF5F0E5A9CEA7F3F26  local-probe-target.exe
F95F52C0917E832A159FCB96692CA1DFFD4F382644693E6339EF829EC77D9AC3  linux-amd64/local-probe-relay
```

哈希用于识别本次本地构建，不代表签名、可重现构建或正式发布批准。二进制由未提交源码构建，重新构建后应重新计算，不能沿用旧哈希。

## 未进行的外部操作

没有创建外部/Windows 账号，没有修改 WFP 或防火墙规则，没有连接实际公网 Relay、修改现有 Tunnel 或发布制品。全部身份、路由凭据、端口和文件输入来自临时测试夹具。
