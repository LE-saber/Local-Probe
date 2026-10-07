---
last_mapped_commit: 9982a8f84a075436b16a2c7aafd61362ebe14146
last_mapped_at: 2026-09-19
---
<!-- refreshed: 2026-09-19 -->

# Architecture

**Analysis Date:** 2026-09-19

## System Overview

```text
┌────────────────────────────────────────────────────────────────────────────┐
│ Composition roots                                                           │
│ `cmd/local-probe-mcp/main.go`     `cmd/local-probe-preview/main.go`         │
└───────────────┬───────────────────────────────┬────────────────────────────┘
                │                               │
                ▼                               ▼
┌───────────────────────────────┐  ┌────────────────────────────────────────┐
│ Authenticated MCP HTTP         │  │ Local Windows Preview                   │
│ `internal/mcpserver/server.go` │  │ `internal/previewapp/` + `previewui/` │
└───────────────┬───────────────┘  └──────────────┬─────────────────────────┘
                │                                  │
                ▼                                  ▼
┌────────────────────────────────────────────────────────────────────────────┐
│ Config snapshot + authorization                                             │
│ `internal/config/` → `internal/policy/` → `policy.BoundScope`              │
└───────────────┬───────────────────────────────────────┬────────────────────┘
                │                                       │
                ▼                                       ▼
┌───────────────────────────────┐  ┌────────────────────────────────────────┐
│ Bounded read/discovery domain  │  │ Local diagnostics/config projection     │
│ `readcore`, `search`,          │  │ `desktopadmin`, `auditreader`,         │
│ `workspacesnapshot`,           │  │ `workspaceadmin`, `supportbundle`      │
│ `environment`                  │  └────────────────────────────────────────┘
└───────────────┬───────────────┘
                ▼
┌────────────────────────────────────────────────────────────────────────────┐
│ Platform/resource adapters                                                  │
│ `internal/rootfs/` (Go `os.Root` handles), `internal/audit/` (JSONL sink)  │
└────────────────────────────────────────────────────────────────────────────┘
```

The repository is a Go 1.25 module. The deployed read path is intentionally
read-only and capability-bound: transport authentication supplies a connection
identity, `internal/policy/policy.go` binds that identity to one immutable
configuration revision, and the bounded domain packages operate only through
interfaces supplied by the composition root. The process/tunnel lifecycle
packages (`internal/supervisor/`, `internal/admission/`, and related adapters)
are local coordination cores with explicit `ProductionReady=false` gates; they
are not wired into the MCP request path.

## Component Responsibilities

| Component | Responsibility | File |
|-----------|----------------|------|
| MCP composition root | Loads validated configuration, opens roots, creates audit/auth services, binds a loopback HTTP listener, and owns shutdown | `cmd/local-probe-mcp/main.go` |
| MCP protocol boundary | Registers tools with the official MCP SDK, enforces request/response budgets, authenticates requests, filters tools, and maps domain results to structured content | `internal/mcpserver/server.go` |
| Cloudflare ingress verifier | Validates Access JWTs, issuer/audience/time claims, maps trusted principals to configured connection IDs, and never accepts a connection from request arguments | `internal/cfaccess/verifier.go` |
| Configuration store | Parses strict JSON, validates roots/profiles/connections, maintains immutable snapshots and monotonically changing revisions, and provides CAS/lease operations | `internal/config/config.go` |
| Policy manager | Converts an authenticated connection into a revision-bound `policy.BoundScope` containing allowed roots, tools, deny patterns, and discovery ignores | `internal/policy/policy.go` |
| Read kernel | Performs bounded byte/line/tail reads, deterministic batch allocation, UTF-8 checks, cancellation, and before/after metadata consistency checks without opening paths | `internal/readcore/engine.go`, `internal/readcore/types.go` |
| Root filesystem adapter | Opens configured roots using `os.Root`, rejects links/reparse objects and non-regular files, revalidates policy on handle operations, and implements both read and search source ports | `internal/rootfs/source.go` |
| Search service | Traverses authorized directory streams with bounded opens/entries, literal text search, tree/list/find operations, signed cursors, and generation checks | `internal/search/service.go`, `internal/search/walker.go`, `internal/search/cursor.go` |
| Workspace snapshot | Wraps the search tree traversal and derives bounded manifest candidates, language statistics, and evidence paths from returned metadata | `internal/workspacesnapshot/engine.go` |
| Environment query | Returns coarse OS/architecture facts and checks only trusted configured tool candidates without PATH lookup or process execution | `internal/environment/types.go`, `internal/environment/discover.go` |
| Audit sink | Validates and redacts typed events, queues normal events, synchronously writes security events, rotates bounded JSONL files, and retains a bounded archive | `internal/audit/sink.go`, `internal/audit/types.go` |
| Preview application model | Reads a local `FileStore` snapshot and sanitized audit projection into immutable GUI view models; lifecycle actions remain behind a production gate | `internal/previewapp/previewapp.go` |
| Preview process connector | Validates a fixed layout and controls only the configured MCP and cloudflared child processes with bounded polling and token-safe diagnostics | `internal/previewconnect/connect.go` |
| Native Preview UI | Renders the local read-only projection, workspace picker, diagnostics export, and tray/window lifecycle; Windows implementation is in the platform file | `internal/previewui/window_windows.go`, `internal/previewui/window_stub.go` |
| Workspace administrator | Adds/removes explicitly selected local roots for the fixed Preview connection using path safety checks and revision CAS | `internal/workspaceadmin/workspaceadmin.go` |
| Runtime coordination core | Models per-connection lifecycle states, admission permits, lifecycle epochs, readiness hooks, and cleanup/retry semantics through injected interfaces | `internal/supervisor/`, `internal/admission/`, `internal/lifecycleadapter/`, `internal/connectionmanager/` |
| Command/probe boundary | Defines immutable fixed command profiles, local confirmation, executable identity bindings, fixed version probes, and fail-closed network enforcement contracts; no arbitrary shell surface exists | `internal/commandprofile/`, `internal/commandexec/`, `internal/probe/`, `internal/confirmation/`, `internal/networkguard/` |

## Pattern Overview

**Overall:** Layered ports-and-adapters architecture with explicit composition
roots and revision-bound capabilities.

**Key Characteristics:**

- Keep listeners, authentication wiring, concrete filesystem handles, and
  process ownership in composition/adaptor packages; domain packages accept
  narrow interfaces. `internal/readcore/types.go` and `internal/search/types.go`
  define the principal source ports.
- Treat configuration and authorization as immutable snapshots. A
  `policy.BoundScope` is tied to a connection, profile, root set, tool set, and
  revision, and every lower-level operation revalidates it through
  `internal/policy/policy.go`.
- Make every remote operation bounded and typed. Read/search/snapshot results
  carry coverage, budgets, warnings, continuation state, and stable error
  categories rather than unbounded content or OS errors.
- Separate currently usable read-only MCP behavior from local-only scaffolding.
  `internal/catalog/catalog.go`, `internal/runtimeowner/`,
  `internal/readiness/`, `internal/supervisor/`, and command execution layers
  expose testable contracts but explicitly fail closed before production wiring.

## Layers

**Composition and Transport:**

- Purpose: Assemble trusted dependencies, bind listeners, select ingress mode,
  and translate process signals into shutdown.
- Location: `cmd/local-probe-mcp/main.go`,
  `cmd/local-probe-preview/main.go`.
- Contains: Flag parsing, config paths, listener setup, SDK construction, and
  UI/controller composition.
- Depends on: All concrete services needed by the selected executable.
- Used by: The operating system or the PowerShell launch scripts in
  `scripts/`.

**Protocol and Authentication:**

- Purpose: Provide the MCP Streamable HTTP endpoint, authenticate local bearer
  tokens or Cloudflare Access assertions, and expose only authorized tools.
- Location: `internal/mcpserver/server.go`, `internal/cfaccess/verifier.go`.
- Contains: SDK server construction, strict argument decoding, middleware,
  tool handlers, response serialization, and public-host checks.
- Depends on: `internal/policy/`, `internal/readcore/`, `internal/search/`,
  `internal/workspacesnapshot/`, `internal/environment/`, and `internal/audit/`.
- Used by: `cmd/local-probe-mcp/main.go` and the fixed Preview connector.

**Configuration and Authorization:**

- Purpose: Define the trusted root/profile/connection model and convert a
  transport identity into an authorization decision.
- Location: `internal/config/`, `internal/policy/`.
- Contains: Strict JSON parsing, revisioned stores, credential references,
  tool/root allowlists, deny/ignore matching, and scope validation.
- Depends on: `internal/readcore/` for the transport-free scope carrier and
  `internal/commandprofile/` for optional local developer-mode configuration.
- Used by: MCP, rootfs, Preview, workspace administration, and local command
  coordination.

**Bounded Read and Discovery Domain:**

- Purpose: Implement file reads, directory discovery, literal searches, tree
  pages, and path-derived workspace summaries without owning OS paths.
- Location: `internal/readcore/`, `internal/search/`,
  `internal/workspacesnapshot/`, `internal/environment/`.
- Contains: In-memory request/result types, bounded algorithms, cursors,
  generation checks, and coverage accounting.
- Depends on: `policy.BoundScope` and source/binder interfaces supplied by
  `internal/rootfs/`.
- Used by: `internal/mcpserver/server.go` and integration tests.

**Filesystem and Storage Adapters:**

- Purpose: Enforce actual OS containment and provide durable local records.
- Location: `internal/rootfs/`, `internal/config/filestore.go`,
  `internal/audit/`.
- Contains: `os.Root` ownership, regular-file/identity checks, platform-specific
  path handling, atomic config persistence, audit JSONL rotation, and bounded
  file readers.
- Depends on: Go OS APIs and the policy/domain ports.
- Used by: Read/search domain services, MCP composition, Preview, and local
  diagnostics.

**Local Preview and Administration:**

- Purpose: Offer a native, local-only observation and workspace configuration
  surface without exposing a management HTTP endpoint to MCP callers.
- Location: `internal/desktopadmin/`, `internal/previewapp/`,
  `internal/previewconnect/`, `internal/previewui/`,
  `internal/workspaceadmin/`, `internal/auditreader/`,
  `internal/supportbundle/`.
- Contains: Sanitized projections, bounded audit reads, fixed child-process
  connection control, revision-aware root mutations, and native UI adapters.
- Depends on: `internal/config/`, `internal/supervisor/` snapshots (when
  supplied), `internal/audit/`, and local filesystem APIs.
- Used by: `cmd/local-probe-preview/main.go`.

**Lifecycle and Command Scaffolding:**

- Purpose: Isolate future process/tunnel lifecycle, command admission, network
  deny, and readiness work behind typed local contracts.
- Location: `internal/admission/`, `internal/supervisor/`,
  `internal/lifecycleadapter/`, `internal/connectionmanager/`,
  `internal/runtimeowner/`, `internal/readiness/`, `internal/commandexec/`,
  `internal/networkguard/`.
- Contains: Injected factories/checkers, per-connection state machines,
  opaque capabilities, Windows Job ownership primitives, attestation/replay
  checks, and fixed-action execution boundaries.
- Depends on: Local configuration and audit contracts, but not on MCP request
  arguments or arbitrary OS command strings.
- Used by: Unit/integration tests and sanitized Preview status interfaces; no
  current MCP tool registers these capabilities.

## Data Flow

### Primary Request Path

1. The executable loads strict JSON and creates a `config.Store`, policy manager,
   revision-bound `rootfs.Source`, audit sink, and MCP server in
   `cmd/local-probe-mcp/main.go`.
2. `internal/mcpserver/server.go:271` builds the SDK Streamable HTTP handler.
   The local-token adapter or Cloudflare header adapter feeds the SDK bearer
   verifier; the HTTP server itself is bound to an explicit loopback address.
3. `internal/mcpserver/server.go:600` runs authorization middleware. It gets
   the connection ID from verified authentication, calls
   `policy.Manager.BindAuthenticated` in `internal/policy/policy.go:54`, and
   filters `tools/list` plus `tools/call` against that bound scope.
4. For `read_file`/`batch_read`, `internal/mcpserver/server.go:762` binds the
   same scope to a `readcore.Scope` and a rootfs-backed source, then the
   handlers at `internal/mcpserver/server.go:1179` and `internal/mcpserver/server.go:1209`
   call `readcore.Engine.ReadBatch`.
5. `internal/rootfs/source.go:119` validates the revision and deny policy,
   checks each relative path, rejects symlink/reparse components, opens the
   configured root handle, and returns a regular-file handle that continues to
   revalidate authorization.
6. `internal/readcore/engine.go:38` allocates deterministic quotas, executes
   bounded concurrent reads, checks UTF-8 and cancellation, compares metadata
   before and after the read, and returns per-item typed errors or continuation
   offsets.
7. Search tools follow the same authenticated scope through
   `internal/search/service.go`; `internal/rootfs/source.go` supplies bounded
   directory/file adapters, and `internal/search/cursor.go` signs cursor state
   with connection/profile/revision and directory generations.
8. `internal/mcpserver/server.go:1328` serializes a single structured result,
   checks the serialized MCP response budget, and returns a short compatibility
   text instead of duplicating large file content. MCP call/list/result audit
   events are recorded through `internal/audit/sink.go`.

### Workspace Discovery and Snapshot Flow

1. `internal/mcpserver/server.go:1244` validates the workspace snapshot request
   and checks the tool in the current `policy.BoundScope`.
2. `internal/workspacesnapshot/engine.go:66` delegates one bounded page to
   `search.Service.TreeDirectory`, preserving the search continuation and live
   generation checks.
3. The snapshot engine derives manifest candidates, language statistics, and
   evidence paths only from returned relative metadata; it does not execute
   project files or claim a strong multi-file snapshot.
4. The result carries coverage, warnings, budget, and continuation through the
   same MCP structured-result path.

### Preview and Workspace Configuration Flow

1. `cmd/local-probe-preview/main.go` creates a `previewapp.App`, fixed
   `previewconnect.Controller`, and `workspaceadmin.Manager`, then passes
   adapters to `previewui.Run`.
2. `internal/previewapp/previewapp.go:309` opens the selected local
   `config.FileStore`, loads a snapshot on refresh, and asks
   `internal/desktopadmin/desktopadmin.go` for bounded overview, connection,
   developer-rule, and diagnostic projections.
3. `internal/auditreader/reader.go` reads only allowlisted audit JSONL files;
   `internal/previewapp/previewapp.go` converts the sanitized records into the
   GUI model. Missing/invalid diagnostics stay explicit as unavailable states.
4. The Windows UI in `internal/previewui/window_windows.go` calls the fixed
   connector for Preview-only connect/reconnect behavior. The connector starts
   only the configured MCP/cloudflared children and sanitizes output.
5. Workspace add/remove operations in `internal/workspaceadmin/workspaceadmin.go`
   validate selected local directories, acquire a mutation lock, use
   `FileStore.SaveIfRevision`, and reconnect the fixed Preview connection via
   `cmd/local-probe-preview/workspace.go`.

## State Management

- `internal/config/config.go` keeps cloned immutable configuration snapshots and
  revision strings. Replacing a snapshot invalidates older policy scopes and
  cursors; `internal/config/filestore.go` adds on-disk atomic/CAS persistence
  for local administration.
- `internal/policy/policy.go` keeps the authorized connection/profile/root/tool
  decision inside `policy.BoundScope`; `Scope()` is an operation input, not a
  portable remote capability. `internal/rootfs/source.go` retains the source
  revision and bound scope on each handle.
- `internal/search/cursor.go` stores signed, expiring process-local cursor
  payloads. Cursors bind operation parameters, scope identity, revision, open
  frames, and directory generations; a process restart invalidates the default
  random signing key.
- `internal/readcore/engine.go` owns no global state. Per-request workers,
  quota counters, and result slices are local; `internal/mcpserver/server.go`
  uses atomics only for request/audit correlation IDs.
- `internal/audit/sink.go` owns a bounded queue and worker, while security
  events use synchronous delivery. `internal/auditreader/reader.go` reads a
  sanitized projection rather than sharing sink internals.
- `internal/previewapp/previewapp.go` serializes refreshes and publishes deep
  copies of its model. `internal/previewconnect/connect.go` serializes owned
  child state and never manages unrelated processes.
- `internal/supervisor/supervisor.go` maintains independent per-connection
  entries, generation numbers, retry timers, and cleanup state. The gate in
  `internal/admission/admission.go` owns global/per-connection permits and
  lifecycle epochs; these states are local coordination primitives and are not
  serialized as remote authorization.

## Key Abstractions

**Revision-bound authorization:**

- Purpose: Prevent model-supplied IDs from selecting a different profile/root
  and make configuration replacement revoke existing access.
- Examples: `internal/policy/policy.go`, `internal/mcpserver/server.go`.
- Pattern: Authenticate first, bind from trusted store state, then revalidate
  the immutable bound value at each adapter operation.

**Transport-neutral read ports:**

- Purpose: Keep the read algorithm independent of HTTP, MCP, and filesystem
  path opening.
- Examples: `internal/readcore/types.go` (`Source`, `Handle`),
  `internal/search/types.go` (`Source`, `Directory`, `Binder`).
- Pattern: Domain engines accept interfaces; `internal/rootfs/source.go`
  implements binders for the production filesystem boundary.

**Bounded structured result:**

- Purpose: Make partial coverage and budget exhaustion explicit to callers.
- Examples: `internal/readcore/types.go` (`BatchResult`),
  `internal/search/types.go` (`Coverage`, `Budget`),
  `internal/workspacesnapshot/types.go` (`Result`).
- Pattern: Return stable schema/version fields, typed warnings/errors, and a
  signed continuation when progress can safely continue.

**Local-only opaque capability:**

- Purpose: Stop tokens, permits, runtime identities, confirmations, and
  readiness contexts from becoming JSON or MCP authorization values.
- Examples: `internal/admission/admission.go`,
  `internal/confirmation/confirmation.go`, `internal/readiness/readiness.go`,
  `internal/runtimeowner/runtimeowner.go`.
- Pattern: Keep ownership fields private, implement serialization rejection,
  and invalidate on revision/generation/stop transitions.

**Sanitized local projection:**

- Purpose: Let Preview show status/diagnostics without exposing paths, tokens,
  raw child output, or implementation errors to remote callers.
- Examples: `internal/desktopadmin/desktopadmin.go`,
  `internal/auditreader/reader.go`, `internal/supportbundle/bundle.go`.
- Pattern: Read through narrow interfaces, apply allowlists and bounds again at
  the presentation boundary, and publish stable enums/codes.

## Entry Points

**Authenticated MCP service:**

- Location: `cmd/local-probe-mcp/main.go`.
- Triggers: Process invocation with an explicit JSON config and ingress flags.
- Responsibilities: Validate loopback address and ingress mode, load config and
  optional Cloudflare settings, construct policy/rootfs/audit/MCP services,
  serve `/mcp`, and perform bounded shutdown on OS signals.

**Read-core demonstration:**

- Location: `cmd/readcore-demo/main.go`.
- Triggers: `go run ./cmd/readcore-demo` with an operator-selected local file.
- Responsibilities: Apply a narrow local-demo scope, pre-open one regular file,
  invoke `readcore.Engine`, and emit structured JSON. It is not a remote source,
  MCP server, or filesystem sandbox.

**Windows Preview application:**

- Location: `cmd/local-probe-preview/main.go`.
- Triggers: Direct executable invocation or `scripts/Start-LocalProbePreview.ps1`.
- Responsibilities: Resolve local config/audit defaults, create Preview model,
  fixed connector, workspace manager, and native UI; close only Preview-owned
  resources on exit.

## Architectural Constraints

- **Threading:** MCP handlers are concurrent and must honor request context;
  `readcore` uses bounded worker goroutines, `audit.Sink` uses one bounded
  writer worker, and Preview serializes refreshes while dispatching UI work
  asynchronously. `internal/supervisor` uses one worker/lifecycle generation
  per connection.
- **Global state:** No process-wide mutable authorization state is used.
  Process-local cursor keys and MCP/audit request counters live in
  `internal/search/cursor.go` and `internal/mcpserver/server.go`; configuration,
  root handles, audit queues, and supervisor entries are instance-owned.
- **Circular imports:** No circular internal package dependency is observed in
  the Go package graph. The intended direction is composition/MCP → policy and
  domain → source ports → rootfs; `internal/rootfs/` may implement
  `internal/search/` ports but domain packages do not import the MCP server.
- **Filesystem boundary:** Remote inputs contain logical root IDs and validated
  relative paths only. Root OS paths come from `internal/config/` and are opened
  by `internal/rootfs/`; string-prefix checks alone are not an authorization
  boundary.
- **Transport boundary:** `internal/mcpserver/` owns an HTTP handler but not a
  listener. `cmd/local-probe-mcp/main.go` must bind loopback explicitly. The
  Cloudflare ingress is an authentication/edge integration, not a root path or
  credential source.
- **Execution boundary:** Raw shell, arbitrary command strings, caller-selected
  argv/environment/cwd, and MCP `run_probe` are not exposed. Fixed command
  contracts in `internal/commandprofile/` and `internal/commandexec/` remain
  local and fail closed before a production launcher/network guard exists.
- **Consistency boundary:** `readcore` metadata versions and directory
  generations are weak/live evidence. `workspacesnapshot` is bounded live
  traversal, not a transactionally consistent repository snapshot.
- **Production readiness:** `ProductionReady=false` in the lifecycle,
  readiness, runtime ownership, catalog, Preview administration, and release
  evaluator packages. Do not infer production process/tunnel control from their
  unit tests or fake backends.

## Anti-Patterns

### Opening a model path outside the rootfs adapter

**What happens:** A new handler calls `os.Open`, walks an absolute path, or
constructs a filesystem path from MCP arguments instead of using the configured
root handle.

**Why it's wrong:** It bypasses revision-bound deny patterns, symlink/reparse
  checks, regular-file checks, and the same-handle revalidation in
  `internal/rootfs/source.go`.

**Do this instead:** Bind the authenticated `policy.BoundScope`, obtain a
`readcore.Source`/`search.Source` from `internal/rootfs/source.go`, and let
`internal/readcore/` or `internal/search/` perform bounded work.

### Constructing authorization from request fields

**What happens:** A handler trusts `profile_id`, `connection_id`, or a
`root_id` supplied by the model as the authority.

**Why it's wrong:** The model can select a broader profile, reuse a stale root
ID, or cross a connection boundary; a lexical ID is not an authenticated
identity.

**Do this instead:** Read the verified connection identity from SDK auth context
and call `policy.Manager.BindAuthenticated` in `internal/mcpserver/server.go`.

### Treating cursors or metadata as durable snapshots

**What happens:** A caller assumes a cursor, metadata token, or workspace
snapshot proves the whole repository stayed unchanged.

**Why it's wrong:** Search cursors cover live directory generations and
`readcore` versions are metadata-strength evidence; neither is a strong
multi-file snapshot.

**Do this instead:** Honor `coverage`, warnings, revision/generation failures,
and continuation semantics from `internal/search/` and
`internal/workspacesnapshot/`; use a separately designed snapshot if strong
consistency is required.

### Turning Preview or fake lifecycle contracts into a control plane

**What happens:** A UI or future caller treats `desktopadmin` actions,
`internal/supervisor` state, or a fake `networkguard` backend as proof that
real MCP/tunnel/process ownership is production-ready.

**Why it's wrong:** Those packages explicitly return production-gate/unavailable
states or use injected fakes; process ownership, OS network enforcement, and
real health evidence are separate boundaries.

**Do this instead:** Keep Preview actions behind the existing gate in
`internal/previewapp/previewapp.go`, and add a reviewed adapter that supplies
the interfaces and evidence required by `internal/admission/` and
`internal/supervisor/` before exposing control.

## Error Handling

**Strategy:** Fail closed at trust boundaries, classify errors into stable
path-free codes, and preserve partial progress only when a continuation is
valid. OS paths, secrets, raw child output, and wrapped implementation details
do not cross MCP or sanitized Preview boundaries.

**Patterns:**

- `internal/config/config.go` rejects unknown fields, duplicate JSON keys,
  invalid references, oversize data, and revision conflicts before a snapshot is
  stored.
- `internal/policy/policy.go` maps missing/disabled/reassigned connections and
  stale revisions to denial/revocation rather than guessing a fallback scope.
- `internal/readcore/engine.go` returns per-item `ItemError` values such as
  `denied`, `not_found`, `unsupported_encoding`, `stale_version`, and
  `budget_exhausted` while preserving other batch items.
- `internal/search/service.go` maps traversal failures into typed errors and
  coverage warnings, and only emits a continuation after checking generations.
- `internal/mcpserver/server.go` converts domain failures to a stable MCP error
  envelope through `errorResult` and bounds the serialized response.
- `internal/previewconnect/connect.go`, `internal/desktopadmin/desktopadmin.go`,
  and `internal/workspaceadmin/workspaceadmin.go` expose fixed diagnostic or
  remediation codes instead of OS/process/path text.
- `internal/audit/sink.go` treats queue/storage/rotation degradation as typed
  sink errors and writes security events synchronously; callers must not treat a
  dropped normal event as a successful audit.

## Cross-Cutting Concerns

**Logging:** `internal/audit/` is the structured event channel. MCP calls,
authentication decisions, policy and search activity, command events, and
tunnel/service state use allowlisted event types and redacted fields. Ordinary
logs should not add ad hoc path/token/command output; `internal/previewconnect/`
and `internal/auditreader/` sanitize child/audit data again.

**Validation:** Strict JSON decoders, bounded lengths/counts, UTF-8 checks,
relative-path validation, platform-specific reparse/drive checks, typed enum
errors, and revision/generation comparisons appear at every layer. Domain
validators live beside their types; MCP schemas in
`internal/mcpserver/server.go` are an additional boundary, not a replacement
for lower-layer validation.

**Authentication:** `internal/mcpserver/` delegates bearer verification to the
official MCP SDK. Local tokens are mapped to configured connections; Cloudflare
Access assertions are verified by `internal/cfaccess/` and mapped through an
explicit principal table. `internal/policy/` is the authorization boundary
after authentication; connection/profile/root/tool arguments never elevate
access.

**Budgeting:** `internal/readcore/`, `internal/search/`, and
`internal/workspacesnapshot/` bound I/O, scan work, output, open handles,
cursor size, and time independently. `internal/mcpserver/` separately bounds
request bodies and serialized wire responses.

**Platform variation:** OS-specific behavior uses Go build suffixes such as
`*_windows.go`, `*_unix.go`, `*_linux.go`, and `*_other.go` throughout
`internal/rootfs/`, `internal/probe/`, `internal/runtimeowner/`,
`internal/previewui/`, and related packages. The Windows UI/runtime path must
not be assumed to exist on Unix builds.

---

*Architecture analysis: 2026-09-19*
