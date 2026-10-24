# GUI implementation handoff

Updated: 2026-10-24. Audience: GUI designer/frontend/desktop engineer. This is the default English handoff; [detailed Chinese version](GUI_HANDOFF.zh-CN.md) remains available. Start with the complete `feat/r12-dual-mode` branch as described in the [developer guide](R12_DEVELOPMENT.md).

## Objective and implementation boundary

Extend the existing approved Windows GUI to manage two connection purposes, pairing, explicit developer authorization, local approvals and jobs. Preserve the current working interface; do not replace it with a disconnected demonstration website.

The separate R12 backend supports real Windows operations, but **the backend is not frozen**. Trusted local management APIs, the full multi-connection host, online rules and selective authorization epochs remain incomplete. GUI code cannot fill these gaps with fake state or by removing permission gates. Coordinate these contracts with the backend owner, implement/test them in Go first, then integrate the interface.

The current GUI is real HTML/CSS/JavaScript embedded in WebView2 at `https://localassets/index.html`. The implementation under `internal/desktopweb/assets` is the visual and behavioral baseline. Historical `prototypes/typography-review` contains simulated data and is only a design reference.

Two purposes:

| Purpose | Intended experience |
| --- | --- |
| Web access to local files | Standard MCP through the existing Web-facing ingress |
| Agent remote operations | A native Agent bridge accesses another authorized environment through direct WSS/Tunnel or self-hosted Relay |

If the Agent already has lawful native access to the same target workspace, advise native tools. Client names, shared paths and IPs are not trusted host identity, and this advice must not bypass native permission boundaries. Same physical machine does not necessarily mean same environment.

## Preserve the accepted design

- Keep Overview, Connections, Access, Activity and Settings. Mix topology, trees, cards, timeline, prose and tables; avoid turning every page into a table.
- Use one collapsible sidebar with a top-right `< / >` control. Expanded items show icon and text in the same aligned button; collapsed items show labeled icons. Do not restore a separate LP rail or global inspector/details panel.
- Put active connection controls on Overview. Connections is primarily new connection configuration and reusable history; saving adds a history item. Viewing/selecting an entry must not implicitly stop other connections.
- Access uses a left tree and corresponding right list. Keep read-only, commands and advanced rule sections. Use collapsible branches rather than an expanding wall of cards. Pause/remove icon buttons require labels/tooltips and keyboard access.
- Activity combines workspace, connection, channel, device, category, result, time and keyword filters. Preserve the event's historical labels instead of retroactively rewriting them.
- Settings includes appearance, font size, About/update, program health and configuration backup. Diagnostic export belongs in health. Save uses a native location/base-name dialog with `.lpbackup` supplied by the app; restore uses native file selection, validation and preview.
- Remove global font specifications, “font loaded”, demo-data and preview badges. Keep meaningful offline, expired, unauthorized, pending approval and trust-change states.
- Preserve text selection/copy, native right-click, F5 and Ctrl+R. Polling must preserve drafts, selection, scrolling and focus.

### Typography, color and clarity

Current baseline: sidebar 232px, top bar 48px, body 14px. Body uses Geist / HarmonyOS Sans SC / system fallbacks; technical values use Geist Mono / Cascadia Code / Consolas. Chinese headings use locally installed STZhongsong with fallback. A CSS font name is not evidence that a distributable font asset is bundled. New fonts require licensing/offline fallback review; do not depend on a CDN.

Dark theme: background `#181818`, cards `#222222`, borders `#3c3c3c`, text `#f2f0ec`, warm-gray emphasis `#d6cfc2`. Light background `#f5f5f1`, white cards. Keep the neutral charcoal/warm-gray palette; **no blue/purple dark theme**. Font sizes 12/14/16/18 must consistently affect navigation, controls, headings and dialogs.

Use real table semantics and explicit column sizing; long names/IDs/paths cannot push action buttons under the wrong header. Test narrow windows and large fonts.

The host already uses Per-Monitor V2 and `WM_DPICHANGED`. Do not use CSS zoom, whole-page transforms or bitmap scaling to repair clarity; do not apply DPI scaling twice. Validate actual WebView2 windows at 100/125/150/200% and across mixed-DPI monitors. Report unavailable hardware as untested rather than claiming all computers are clear.

Synchronize page, native title bar/border and native context menu with light/dark mode. Preserve `window.ready`, acknowledged `window.theme`, retries and rapid-switch final-state consistency. CSS alone cannot theme native menus.

## Required workflows

### Connections and pairing

Choose purpose separately from transport. Existing official OpenAI Secure MCP Tunnel remains standard MCP; the new opaque byte-stream `/v1/peer` is not a generic use of that official tunnel. Direct WSS/Tunnel and self-hosted Relay are supported by the native Agent bridge.

Show target identity, connection/profile, transport readiness, endpoint authentication and MCP readiness as separate states. Saved configuration, a listening port and completed endpoint authentication are different facts. Do not show online solely because a record exists.

Pairing imports/exports only public bundle/CSR/certificate material, displays independently checked fingerprints and binds the intended connection. Private keys and CA signing authority remain in the trusted host, never in JS. Revocation errors and certificate expiry must be visible. Do not request real server secrets to build mock interactions.

### Explicit developer access and approvals

Display the execution contract: `trusted_host` uses current-user file/network permissions and **is not a sandbox**. Commands may reach files outside a scoped root through OS permissions. Read-only remains the default; saving a runner/template cannot grant access.

Require the backend's writable v2 profile, `agent_remote`, developer connection allowlist, tool permission, audited runner/hash and local approval. The old desktop `execution_available=false` projection is not the new engine. Do not relabel it as ready or delete `network_enforcement_required` to simulate integration.

Approval details show operation ID/digest, authenticated Agent/connection, workspace, exact arguments/script, executable identity/hash, timeout, file before/after content, expected file version and expiry. Approve/reject are local-owner actions bound to that exact plan; remote MCP has no approval method. Changed/expired plans must be prepared and approved again. Do not accept a remote `confirmed=true`.

### Jobs and output

Use backend states and distinguish queued/pending approval/running/terminal failure/interrupted/unknown. Display rejection and cleanup failures accurately. Reconnection queries the original job, rather than generating a new nonce and repeating side effects.

Output is currently available after completion, bounded and paged by stdout/stderr byte offset, with base64 raw bytes. Do not invent a live terminal or assume cmd output is UTF-8.

Local viewing must not accidentally renew the Agent's 60-second status-poll lease. Remote cancellation waits for cleanup; local owner cancellation persists a request. Display “cancelling” until a real terminal state is observed. Unconfirmed process-tree cleanup remains unknown and disables admission.

### Rules, health and backup

Deny takes precedence; ignore affects discovery and is not secrecy. Show preview, affected scope and revision/CAS conflicts while retaining edits. Current rules RPC is offline; do not claim immediate global revocation until the backend implements it for active targets.

Health distinguishes configuration, process, transport, identity, authorization, runner and certificate expiry. An absent update source means “not configured”, not “latest”. Restore validates `local-probe.backup.v1`, previews changes and preserves removal of developer grants; do not include keys/journals in ordinary config backups or resurrect revoked grants.

## Existing interfaces versus proposed work

Actual message envelope:

```json
{"id":"gui-request-1","method":"snapshot","params":{}}
```

Response: `{id,ok,data,error}`; current data is a snapshot containing desktop, connection, workspace, view and optional rules/backup candidate. It is not JSON-RPC 2.0 and must not be exposed as a remote approval API. Keep the fixed origin/CSP; no arbitrary fetch, inline scripts or remote fonts/icons.

| Existing method | Behavior |
| --- | --- |
| `snapshot` | Current desktop projection |
| `connections.save/select/remove` | History; old DTO lacks purpose/Relay fields |
| `connection.connect/reconnect/cancel/disconnect/setTransport` | One selected controller |
| `workspace.pick/add/update/remove` | Native directory selection and roots |
| `rules.read/preview/apply` | Offline rule administration |
| `developer.configure/saveProfile/removeProfile` | Old configuration, not the new operation engine |
| `settings.backup/pickBackup/restore` | Native save/select/validate/preview/restore |
| `logs.export`, `diagnostics.export` | Controlled redacted exports |
| `window.theme` | Separately handled host theme application/acknowledgement |

**Proposed local contracts below do not yet exist as stable RPC methods:**

| Contract | Backend responsibility |
| --- | --- |
| Purpose, target identity, capabilities and independent connection lifecycle | Versioned connection DTOs and full runtime host |
| Pair/import/issue/revoke and fingerprints | Trusted local deviceidentity management |
| Catalog and explicit execution policy | Native selection, hashing and config revision |
| Local approval queue/detail/decision | Owner context and exact journal ID/digest |
| Job projection/output/owner cancel | Read-only local status without Agent lease renewal |
| Online rules and health/certificate state | Affected connections, immediate revocation and truthful diagnostics |

Prefer direct Go APIs or typed local IPC. Do not concatenate shell commands, accept arbitrary management executables or transfer host authority into remote JSON fields. Agree required/nullable fields, errors, paging, bounds and cancellation behavior with the backend owner before integrating.

## Current limits that the interface must show

- Windows execution target only; Linux Agent needs real-machine acceptance.
- Job limits: global two / one per connection, 32 processes, 1 GiB aggregate committed memory, cumulative user CPU and wall-time bounds, 64 KiB per output stream. These are not sandbox guarantees.
- Default timeout 60 seconds, maximum 30 minutes; approval window five minutes; Agent lease 60 seconds.
- UTF-8 files up to 128 KiB; at most 32 unique-context exact text edits. Existing-file updates are not atomic replacement; before images support manual recovery after unknown/partial results.
- cmd scripts reject extra arguments and journal paths with spaces/cmd metacharacters.
- Journal: 128 historical jobs and about 64 MiB; no automatic archive. Show admission/maintenance failure.
- Global config revision currently invalidates old plans; selective connection epochs and online reload are incomplete.
- Two concurrent jobs are not three independent production connection lifecycles.
- Prior public Relay checks were read-only, not the new cross-machine execution acceptance.

## Delivery and acceptance

Deliver the official assets and necessary typed host adapters, contract changes, independent builds, screenshots and actual evidence. New interactions may first use a separate operable HTML mock for owner review; the existing approved visual baseline does not need a wholesale redesign.

Run frontend VM tests and Go test/race/vet. Add meaningful tests for changed behavior. Manually test long content, collapsed sidebar, filters, scope tree/list coordination, keyboard labels/focus, drafts, copy/right-click/refresh, native backup dialogs, fast theme switching, mixed DPI and disconnect/recovery. VM tests do not establish pixel/native-menu correctness.

Verify default read-only, refusal without exact local approval, changed-plan expiry, cross-subject isolation, revocation, idempotent retry, owner cancellation and unknown cleanup. New runtime features must be tested in the backend first.

Do not overwrite running binaries, alter real authorization, connect to the user's Linux computer or deploy on the real Relay as part of design work. Real Relay/Linux operation acceptance follows backend and GUI development with explicit test details from the owner. Uploading this branch is not merge/Release authorization.

Next: [developer guide](R12_DEVELOPMENT.md), [operations](R12_OPERATIONS.md), [pairing](R12_PAIRING_AND_TRANSPORT.md), [latest status](IMPLEMENTATION_STATUS.md).
