# Local-Probe

[English](README.md) · [简体中文](README.zh-CN.md) · [繁體中文](README.zh-TW.md) · [日本語](README.ja.md)

**MCP tools and Windows desktop management for authorized workspace access, with an R12 backend for paired remote Agent operations.**

> **R12 backend development branch.** Get the current implementation from `feat/r12-dual-mode`; `main` remains the R11.3 Desktop Preview baseline until reviewed and merged. This is source for development, not a new Release. The existing desktop Developer page still reports `execution_available=false`; the separate paired target can opt into locally approved `trusted_host` operations. `release_ready=false` remains in effect. Project-owned source is [MIT](LICENSE); retain separate third-party licenses.

## R12: start here

```powershell
git clone --branch feat/r12-dual-mode git@github.com:LE-saber/Local-Probe.git
Set-Location .\Local-Probe
```

Read the [R12 developer guide](docs/R12_DEVELOPMENT.md), [GUI handoff](docs/GUI_HANDOFF.md), and [current status](docs/IMPLEMENTATION_STATUS.md). English is the default documentation language; detailed Chinese design and historical evidence remain available through the [documentation index](docs/README.md).

| Capability | Current implementation | Remaining work |
| --- | --- | --- |
| Web access to local files | Existing scoped MCP, Cloudflare Access and official OpenAI Tunnel integration | R12 advanced operations are not wired into the Web/desktop ingress |
| Agent access to another environment | Native stdio bridge, persistent pairing/revocation, direct WSS or static self-hosted Relay, inner TLS 1.3 mutual authentication | Certificate automation and production lifecycle management |
| Explicit developer operations | Windows command/script/version/build/test, text writes and exact patches; local per-operation approval, durable jobs, cancellation and bounded output | Trusted GUI management and approval interfaces |
| Concurrency and rules | Two independent connections can each run a job; scoped reads/writes and offline rule management | Full host for at least three independent connections, online rules and selective authorization epochs |

`trusted_host` runs with the current OS user's actual file and network permissions. It is **not a sandbox**. Existing read-only profiles are not upgraded; operations require an explicit writable v2 profile, `agent_remote`, developer/tool permission, a local runner catalog and exact local approval. See [operation setup and limits](docs/R12_OPERATIONS.md).

Local Windows operation tests and a public read-only Relay path have been exercised. The new operations have **not** been validated from the additional Linux computer through the real Relay, and Linux target execution/writes are unsupported. If an Agent already has authorized native access to the target workspace, use its native tools rather than this bridge.

## Existing desktop Preview

The sections below describe the existing R11.3 desktop behavior. Its saved connection controls and Developer page do not automatically use the new R12 target/Agent backend.

## What you can do

- List directories, find files, search literal text and page through directory trees.
- Read bounded byte/line ranges, tails and batches; obtain scoped workspace snapshots and limited environment information.
- Manage authorized roots and preview connection/folder deny/ignore rules, with revision checks and offline configuration saves.
- View connection health, filtered activity, redacted diagnostics and native `.lpbackup` backup/restore.
- Use the WebView2 desktop interface, tray controls, DPI scaling, themes, languages and font sizes.
- Start/stop the selected Cloudflare or OpenAI tunnel connection after the required external setup.

The GUI manages **one selected connection**. Stored connection history is not simultaneous multi-connection operation. The Developer page manages configuration and command templates; it does not enable commands, scripts, builds or file writes.

## Build and open the desktop GUI

Requires Windows x64, WebView2 Runtime and PowerShell 7. Source builds require Go 1.25+ and Git; initial module resolution needs network access or a populated cache. Prepare the official `cloudflared` or `tunnel-client` separately for your chosen connection; neither is bundled.

```powershell
git clone --branch feat/r12-dual-mode git@github.com:LE-saber/Local-Probe.git
Set-Location .\Local-Probe
pwsh -NoLogo -NoProfile -File .\scripts\Build-LocalProbeDesktop.ps1
pwsh -NoLogo -NoProfile -File .\scripts\Start-LocalProbeDesktop.ps1
```

The GUI can open without a configuration. Opening it does not authorize a disk or create credentials. Follow the [desktop guide](docs/DESKTOP_PREVIEW.zh-CN.md) and [deployment guide](docs/DEPLOYMENT_PREVIEW.zh-CN.md) for first configuration. Exit an older GUI from its tray before switching versions; closing its window alone is insufficient.

## Choose a connection

| Connection | Local origin | Authentication | GUI management |
| --- | --- | --- | --- |
| Cloudflare Preview | `127.0.0.1:8788` | Cloudflare Access JWT | Supported after setup |
| OpenAI Secure MCP Tunnel | `127.0.0.1:8787` | Local hop token plus Platform Tunnel | Requires separate credential initialization first |

The two paths are independent. Do not let separate startup methods own the same origin or port. A locally ready service does not prove successful ChatGPT Workspace registration or a real remote tool call.

### Initialize an authorized folder

Replace the example path with a folder you intend to authorize:

```powershell
$authorizedRoot = (Resolve-Path 'C:\replace-with-folder-you-authorize').Path
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Setup-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path `
  -AuthorizedRoot $authorizedRoot
```

Initialization uses ignored local `.runtime/` configuration and `.tools\cloudflared.exe`. The Cloudflare token file belongs in `.secrets\cloudflared-tunnel-token.txt` **outside the repository, under its parent directory**. Do not commit tokens, keys, JWTs, cookies or runtime configuration.

For Cloudflare, route the public hostname to `http://127.0.0.1:8788` and configure issuer, JWKS, audience, JWT subject mappings and matching `public_hosts` in `.runtime\cloudflare-access.json`. After the connection is ready, the client endpoint is `https://<public-host>/mcp`; verify `initialize`, `tools/list` and `ping` before file access. The GUI manages only child processes it started.

The older native Preview remains available through [Build-LocalProbePreview.ps1](scripts/Build-LocalProbePreview.ps1) and [Start-LocalProbePreview.ps1](scripts/Start-LocalProbePreview.ps1). New desktop users should follow the Desktop instructions above.

## Access and release boundaries

Available files and tools depend on profile roots, tools, deny/ignore rules and read-only policy. The existing desktop/Web Preview does not expose developer commands or writes. The separate R12 paired target supports explicitly configured and locally approved operations; merely saving a desktop command template does not enable them. Neither path exposes credential display or model-edited configuration.

Production supervision/service, a real WFP/broker enforcement path, signing, an installer, automatic updates, Linux/macOS GUI, default large-scale indexing, million-file end-to-end evidence and independent security review are not claimed as delivered. Local checks and historical validation records do not establish those guarantees. The [R11.3 release note](docs/RELEASE_NOTES_R11.3.zh-CN.md) is a draft, not a published release.

## Guides and evidence

Start with the English R12 guides. Detailed historical implementation plans remain in Simplified Chinese.

| Guide | Contents |
| --- | --- |
| [R12 development](docs/R12_DEVELOPMENT.md) | Source checkout, component map, verification and remaining backend work |
| [GUI handoff](docs/GUI_HANDOFF.md) | Approved design, existing/proposed interfaces and acceptance requirements |
| [Operations](docs/R12_OPERATIONS.md) | Explicit trusted-host setup, approvals, jobs and file semantics |
| [Pairing and transport](docs/R12_PAIRING_AND_TRANSPORT.md) | Endpoint pairing, Tunnel/Relay startup and trust boundaries |
| [Documentation index](docs/README.md) | English entry points and Chinese design/evidence references |
| [Deployment](docs/DEPLOYMENT_PREVIEW.zh-CN.md) | Dependencies, configuration, workspaces and troubleshooting |
| [Desktop GUI](docs/DESKTOP_PREVIEW.zh-CN.md) | Interface, backups, file rules and verification boundaries |
| [Native Preview](docs/PREVIEW.zh-CN.md) | Older GUI, tray, workspaces and diagnostics |
| [Cloudflare setup](docs/CLOUDFLARE_TUNNEL_SETUP.zh-CN.md) | Routing, Access JWT and metrics checks |
| [OpenAI Tunnel setup](docs/TUNNEL_SETUP.zh-CN.md) | Independent credential setup and port 8787 path |
| [Development](docs/DEVELOPMENT.zh-CN.md) / [Architecture](docs/ARCHITECTURE.zh-CN.md) | Source map and component responsibilities |
| [Tool contracts](docs/TOOL_CONTRACTS.md) / [Threat model](docs/THREAT_MODEL.md) | Protocol and security boundaries |
| [Implementation status](docs/IMPLEMENTATION_STATUS.md) | Actual work and historical verification records |
| [R11 backend progress](docs/R11_BACKEND_PROGRESS.zh-CN.md) | Remaining execution gates |
| [Release checklist](docs/R9_RELEASE_CHECKLIST.zh-CN.md) | Release prerequisites |
| [Master plan](docs/MASTER_PLAN.zh-CN.md) / [Code map](.planning/codebase/STRUCTURE.md) | Maintenance plan and repository structure |

## Development and packaging

Existing checks, to run in an appropriate configured environment:

```powershell
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
```

Use [Test-LocalProbeEnvironment.ps1](scripts/Test-LocalProbeEnvironment.ps1) for environment checks. The legacy native Preview ZIP can be prepared with:

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Package-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path -Version R9-preview -Build
```

That command creates an unsigned manual-test preview, not a stable release. Packaging, non-Quick acceptance, clean-directory rebuilding and actual Windows/Tunnel/ChatGPT checks remain separate gates. This documentation update does not claim to have rerun them. Project-owned code is MIT; preserve third-party notices and review them before distributing a package.
