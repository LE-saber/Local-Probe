# Local-Probe

[English](README.md) · [简体中文](README.zh-CN.md) · [繁體中文](README.zh-TW.md) · [日本語](README.ja.md)

**Windows desktop management and MCP tools for discovering, searching and reading explicitly authorized local workspaces.**

> **R11.3 Desktop Preview.** Source is available under [MIT](LICENSE), with separate third-party licenses. There is no published GitHub Release at this snapshot. This is an unsigned preview, not a production supervisor or installer; `execution_available=false` and `release_ready=false` remain in effect.

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
git clone --branch main https://github.com/LE-saber/Local-Probe.git
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

Available files and tools depend on profile roots, tools, deny/ignore rules and read-only policy. The preview does not expose arbitrary shell, arbitrary commands, writes, credential display or model-edited configuration. Backend modules and configuration intent are not proof that execution is enabled.

Production supervision/service, a real WFP/broker enforcement path, signing, an installer, automatic updates, Linux/macOS GUI, default large-scale indexing, million-file end-to-end evidence and independent security review are not claimed as delivered. Local checks and historical validation records do not establish those guarantees. The [R11.3 release note](docs/RELEASE_NOTES_R11.3.zh-CN.md) is a draft, not a published release.

## Guides and evidence

Most detailed implementation guides remain in Simplified Chinese; these links preserve the existing documentation.

| Guide | Contents |
| --- | --- |
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
