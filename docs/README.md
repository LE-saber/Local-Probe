# Documentation

English is the default language for the repository and current R12 developer handoff. Existing `.zh-CN.md` documents retain the detailed designs, historical evidence and Chinese translation. A design describes intended work; the latest status and actual code determine what is implemented.

## R12 developer entry points

| Document | Read it for |
| --- | --- |
| [Developer guide](R12_DEVELOPMENT.md) | Getting the complete branch, source map, checks and implementation sequence |
| [GUI handoff](GUI_HANDOFF.md) | Accepted visual baseline, workflows, actual desktop RPC and missing interfaces |
| [Operations](R12_OPERATIONS.md) | Trusted-host execution, local approval, jobs and file changes |
| [Pairing and transport](R12_PAIRING_AND_TRANSPORT.md) | Pairing, native Agent, WSS/Relay startup and identity handling |
| [Implementation status](IMPLEMENTATION_STATUS.md) | Current English summary followed by dated historical records |
| [Upload verification](evidence/r12/2026-10-24-source-upload.md) | Checks performed for the repository handoff |

## Detailed design and evidence (Simplified Chinese)

- [R12 implementation plan](R12_DUAL_MODE_REMOTE_IMPLEMENTATION_PLAN.zh-CN.md), [trusted-host decision](R12_EXECUTION_DECISION.zh-CN.md), [progress](R12_PROGRESS.zh-CN.md).
- [GUI requirements](GUI_HANDOFF.zh-CN.md), [developer handoff](R12_DEVELOPER_START.zh-CN.md), [operation details](R12_OPERATIONS_BACKEND.zh-CN.md).
- [Pairing/transport details](R12_PAIRING_AND_TRANSPORT.zh-CN.md), [public Relay deployment evidence](R12_RELAY_DEPLOYMENT.zh-CN.md).
- [Local operation evidence](evidence/r12/2026-10-24-operations.md), [transport evidence](evidence/r12/2026-10-24-transport.md).

## Existing Preview and architecture

- [Desktop guide](DESKTOP_PREVIEW.zh-CN.md), [deployment](DEPLOYMENT_PREVIEW.zh-CN.md), [development](DEVELOPMENT.zh-CN.md).
- [Architecture](ARCHITECTURE.zh-CN.md), [tool contracts](TOOL_CONTRACTS.md), [threat model](THREAT_MODEL.md).
- [OpenAI Tunnel](TUNNEL_SETUP.zh-CN.md), [Cloudflare Tunnel/Access](CLOUDFLARE_TUNNEL_SETUP.zh-CN.md).
- [Master plan](MASTER_PLAN.zh-CN.md), [R11 backend plan](R11_BACKEND_FIRST_PLAN.zh-CN.md), [release checklist](R9_RELEASE_CHECKLIST.zh-CN.md).

R11 documents describe the older desktop ingress and execution gates. The explicitly approved R12 `trusted_host` path is a separate execution contract; it does not implement strict network isolation or silently enable the old desktop execution path.
