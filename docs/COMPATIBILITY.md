# 兼容性与真实账户预检

核查日期：2026-09-07。**目前没有真实 ChatGPT 账号/模型/Tunnel 组合被本项目端到端验证。** 不要把开发者文档、App 可添加或 Go 单元测试等同于真实工具调用成功。

## 官方资料口径差异

- Developer Mode 指南当前列出 Plus、Pro、Business、Enterprise、Education，并描述完整 MCP 工具能力：`https://developers.openai.com/api/docs/guides/developer-mode`。
- Help Center 当前将 Full MCP 与 Business/Enterprise/Edu 关联，另描述 Pro 的 read/fetch 范围：`https://help.openai.com/en/articles/12584461-developer-mode-and-mcp-apps-in-chatgpt-beta`。
- Tunnel 指南将 Platform 的 Read/Use/Manage 权限与 ChatGPT developer-mode 权限分开，并允许多个组织/workspace 的 association：`https://developers.openai.com/api/docs/guides/secure-mcp-tunnels`。

因此不沿用对话中“Plus 一定不能用/Pro 一定只有某种工具”的绝对结论。账户套餐、Workspace 管理策略、模型能力、App 工具发现和 Tunnel 权限分别测试；服务不能靠套餐字符串决定授权。

## 待填写的真实矩阵

| 匿名 connection | OS | workspace 类型/套餐 | 网页实际模型标签 | Developer Mode/App | Tunnel Use/association | 发现工具 | 成功读取 | 第二连接隔离 |
|---|---|---|---|---|---|---|---|---|
| A | 待确认 | 待确认 | 待确认 | 未测试 | 未测试 | 未测试 | 未测试 | 未测试 |
| B | 待确认 | 待确认 | 待确认 | 未测试 | 未测试 | 未测试 | 未测试 | 未测试 |

不要在此表填账号邮箱、Cookie、完整 org/workspace ID 或 API key；必要的 ID 只留脱敏标签。

## 操作步骤

1. 在目标账户确认原生 App/Developer Mode 入口及创建权限；记录网页模型实际标签，不凭订阅名称推断。
2. 单独确认 Platform Tunnel Read/Use 权限及 ChatGPT workspace association；由所有者在本机录入 runtime key，不把 key 发给聊天、Issue 或 PR。
3. 用官方 MCP SDK 的最小无敏感信息 echo 工具验证 initialize、tools/list 和 tools/call；按官方客户端当前 help 配置，不猜旧参数。
4. 先验证本机 SDK client，再经官方 Tunnel 在目标 Pro 对话中调用 echo；记录准确错误层次。
5. 完成安全 root adapter 后，再调用 read_file/batch_read，验证返回路径、范围及版本与本机相符。
6. 第二账户重复；使用两个不同 profile 做正向和越权测试，再独立停用其中一个。
7. 根据实际证据更新矩阵，标明测试日期、客户端/协议/模型版本和脱敏证据位置。

一个 shared connection 不提供可凭空相信的终端成员身份；首版授权边界是 connection/profile。要逐成员隔离，需要可信 OAuth principal 映射或独立 connector/ingress。

## 当前本地可验证内容

Go 1.23.2/Linux amd64 已运行内核及本机 demo 测试。Windows amd64 和 macOS arm64 已交叉编译，但没有本地 OS 运行与安全路径测试。GitHub Actions 配置使用 Go 1.26.5 进行三平台测试，配置存在不表示 run 已通过，实际状态见 workflow/PR。

当前没有 MCP SDK 依赖和 Tunnel 二进制安装，也没有调用真实账号。不能给出“所有套餐可用”的产品承诺。
