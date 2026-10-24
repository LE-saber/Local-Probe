# R12 developer guide

Updated: 2026-10-24. This is the English entry point for the current development implementation. Read [repository rules](../AGENTS.md), the [master plan](MASTER_PLAN.zh-CN.md) and [actual status](IMPLEMENTATION_STATUS.md) before editing.

## Get the implementation

```powershell
git clone --branch feat/r12-dual-mode git@github.com:LE-saber/Local-Probe.git
Set-Location .\Local-Probe
git status --short --branch
git rev-parse HEAD
```

The R12 code and handoff documents are on this branch, including files that were previously untracked. Cloning the older main baseline is insufficient. The earlier developer ZIP is a dated snapshot; this branch is the continuing source of development. No release/tag is created by uploading this source.

If you already have local changes, fetch and compare in a separate checkout/branch. Do not overwrite a working tree, run reset/clean, or copy private runtime data into Git. Record the commit used for every test or build.

## Product scope and actual status

Two connection purposes are distinct from their transports:

- `web_local`: standard MCP access from a Web AI to authorized local files, through the existing supported ingress.
- `agent_remote`: a native Agent bridge connecting to another execution environment through direct WSS/Tunnel or a self-hosted Relay.

The Agent should use authorized native tools when it already has direct access to the same workspace. Client name, IP or matching paths do not establish locality and must not grant permission or bypass an Agent's own restrictions. WSL, containers and VMs may be different execution environments on the same computer.

Implemented R12 backend:

- Persistent device/Agent pairing, certificate-bound grants, revocation and active leases.
- Native stdio MCP bridge, scoped paired target, direct WSS and static role-separated Relay; endpoint TLS 1.3 mutual authentication keeps MCP payloads encrypted through the Relay.
- Explicit Windows `trusted_host` commands, scripts, actual version calls, build/test, bounded UTF-8 writes and exact patches.
- Private durable jobs, exact per-operation local approval, subject/connection isolation, idempotency, cancellation, output bounds and restart without automatic replay.
- Two connections each running one real job independently. This is not the full production multi-connection host.

Not complete: GUI/Web advanced-operation ingress, trusted local management interfaces, at least three independently managed production connections, online rules/selective epochs, automatic certificate/credential lifecycle, archival/retention and final cross-machine operation acceptance. Linux Agent builds exist; Linux target execution/writes are unsupported.

## Source map

| Path | Responsibility |
| --- | --- |
| `cmd/local-probe-agent` | Official SDK stdio bridge for the native Agent |
| `cmd/local-probe-target` | Paired MCP target; optional operation engine |
| `cmd/local-probe-pair` | Local identity creation, fingerprints, issue/install/revoke |
| `cmd/local-probe-operations` | Local inspect/approve/reject/cancel and before-image access |
| `cmd/local-probe-relay` | Fixed-route encrypted stream forwarding on loopback |
| `cmd/local-probe-relay-edge` | Narrow TLS reverse proxy for the Relay |
| `internal/deviceidentity` | Private identity storage, grants, revocation and leases |
| `internal/peertransport`, `internal/agentbridge`, `internal/relay` | TLS/WSS transport and MCP forwarding |
| `internal/operations` | Runner catalog, immutable plans, private journal and execution |
| `internal/mcpserver/operations.go` | Eight paired MCP operation tools |
| `internal/config`, `internal/policy`, `internal/rootfs` | Configuration, authenticated scope, real file boundaries and controlled writes |
| `internal/desktopweb/assets` | Existing approved GUI HTML/CSS/JS/SVG baseline |
| `internal/desktopbridge`, `internal/desktophost` | Local RPC and Windows/WebView2 host |
| `deploy/operations`, `deploy/relay` | Empty catalog and generic Relay deployment examples |

The official GUI is under `desktopweb/assets`; `prototypes/typography-review` is a historical mock with fake data. The old desktop bridge still owns one controller; its old `network_enforcement_required` gate is not the new operation engine. Do not remove a gate or fabricate state to simulate integration.

## Environment and first checks

Windows amd64 is the execution and GUI target. `go.mod` requires Go 1.25.0; CI and the recorded Windows verification use Go 1.26.5. Start with that toolchain to reproduce evidence. Initial dependency resolution requires network access or a trusted module cache. Keep `go.mod`, `go.sum`, the local WebView2 replace and third-party notices intact.

Use Git, PowerShell and Node for development; WebView2 Runtime is required for the GUI. Race testing needs a supported C toolchain. Actual Python/Node/PowerShell/cmd runtime tests require explicitly selected local installations; do not auto-install them or invent version results.

```powershell
go version
node --version
go mod verify
node scripts/Test-LocalProbeDesktopFrontend.mjs
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
```

Run Windows account/ACL tests as the normal development user; an automation sandbox may change the identity being tested. Report such failures rather than weakening assertions. Keep process-local TEMP/TMP in an ignored `.tools` subdirectory so `./...` does not enumerate transient build files. Do not modify global environment settings.

Build separate development binaries:

```powershell
go build -trimpath -o bin/dev/local-probe-target.exe ./cmd/local-probe-target
go build -trimpath -o bin/dev/local-probe-agent.exe ./cmd/local-probe-agent
go build -trimpath -o bin/dev/local-probe-pair.exe ./cmd/local-probe-pair
go build -trimpath -o bin/dev/local-probe-operations.exe ./cmd/local-probe-operations
```

Read [pairing and transport](R12_PAIRING_AND_TRANSPORT.md) before starting a target. Use synthetic directories and new test identities. Samples are not complete live configurations and grant no access by themselves.

For GUI builds, read `scripts/Build-LocalProbeDesktop.ps1` before selecting `-GoPath` and `-OutputPath`. It also writes `bin/local-probe-mcp.exe`; changing only the GUI output path does not protect an existing running MCP binary. Build and test in a separate directory. Preserve the existing user's processes, connections and authorizations.

Real-runtime opt-in tests use `LOCAL_PROBE_OPERATIONS_REAL_TOOLS=1` and absolute paths in `LOCAL_PROBE_OPERATIONS_PYTHON`, `LOCAL_PROBE_OPERATIONS_NODE`, `LOCAL_PROBE_OPERATIONS_POWERSHELL` and `LOCAL_PROBE_OPERATIONS_CMD`. Run `TestInstalledPythonNodePowerShellScriptsAndVersions` and `TestRealGoBuildAndTestProject` separately with these prerequisites. Their default skip is not a pass.

## Implementation sequence and ownership

1. **Reproduce the baseline.** Record the commit, tool versions and actual checks. Previous PASS records are not new results on your computer.
2. **Agree local management contracts.** Backend first: capability/readiness, pairing, catalog, exact local approvals, job projections and owner cancellation. Define versioned DTOs, errors, paging and asynchronous state before the GUI depends on them.
3. **Complete one real connection.** Pair → explicitly authorize → request → locally inspect → approve/reject → observe actual result/cancel, preserving the read-only path.
4. **Finish the production control plane.** Backend first: online configuration/rule changes, affected-connection revocation/selective epochs and at least three independent connection lifecycles; then wire the GUI.
5. **Perform desktop regressions.** Existing five pages, topology/tree/list mix, native dialogs, theme synchronization, selection/copy/right-click/refresh, drafts, long content and real mixed-DPI windows.
6. **Validate cross-machine Relay operations.** After development, obtain the owner's Linux access details and short-lived test credentials. Test Linux Agent → real Relay → Windows target using synthetic data. Do not scan the LAN or assume authorization to an unknown machine.

GUI ownership covers interaction, official assets and typed local adapters. Backend ownership covers authorization, identity, execution and runtime lifecycle. Prefer direct Go APIs or trusted typed local IPC. Remote MCP must never gain a local approval method; do not shell-concatenate a CLI wrapper or move security decisions into JavaScript.

## Evidence and release boundary

See the [upload verification](evidence/r12/2026-10-24-source-upload.md), [operation evidence](evidence/r12/2026-10-24-operations.md) and [transport evidence](evidence/r12/2026-10-24-transport.md). The prior public Relay test covered read-only behavior with both endpoint programs on the Windows computer; it does not establish Linux-to-Windows execution.

No live configuration, token, endpoint identity, private key, SSH data, job journal, backup, binary or raw local log belongs in the source upload. Generic deployment examples do not authorize deployment. No new accounts or global network-policy changes are approved; the earlier Relay TCP 8080 exception is specific to that test deployment. `trusted_host` was explicitly approved and uses current-user OS permissions, without sandbox guarantees.

Source upload is not authorization to merge main, publish a Release, install a service or change real connections. Return a file/commit list, interface changes, test and manual evidence, remaining gaps and independent development builds. Preserve MIT and all third-party licensing.

Detailed supplementary materials: [Chinese handoff](R12_DEVELOPER_START.zh-CN.md), [R12 plan](R12_DUAL_MODE_REMOTE_IMPLEMENTATION_PLAN.zh-CN.md), [progress](R12_PROGRESS.zh-CN.md).
