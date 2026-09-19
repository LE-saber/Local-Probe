---
last_mapped_commit: 9982a8f84a075436b16a2c7aafd61362ebe14146
last_mapped_at: 2026-09-19
---
# Codebase Structure

**Analysis Date:** 2026-09-19

## Directory Layout

```text
repo/
├── .github/workflows/              # Go verification and Windows release-preflight CI
├── .planning/codebase/             # GSD-generated architecture/structure maps
├── cmd/                             # Executable composition roots
│   ├── local-probe-mcp/             # Authenticated loopback MCP service
│   ├── local-probe-preview/         # Windows Preview/tray application
│   └── readcore-demo/               # Explicit local-file read demonstration
├── configs/                         # Non-secret example configuration files
├── docs/                            # Plan, contracts, threat model, status, and evidence
├── internal/                        # All Go implementation packages
├── manual-test-targets/             # Deterministic Unicode, pagination, and denial fixtures
├── scripts/                         # PowerShell startup, setup, build, and acceptance scripts
├── AGENTS.md                        # Repository execution and safety rules
├── README.md                        # Product scope, current status, and run commands
├── go.mod / go.sum                  # Go module and dependency locks
└── .gitignore / .gitattributes      # Repository and platform file rules
```

Local-only directories such as `.runtime/`, `.secrets/`, `bin/`, `tmp/`, and
`logs/` are ignored or may contain runtime/build material. Their contents are
not part of the committed source layout; secrets and credential files must not
be inspected or documented.

## Directory Purposes

**`cmd/`:**

- Purpose: Own process arguments, dependency composition, listeners, and OS
  signal/UI startup.
- Contains: `main.go` files and executable-specific adapters/tests.
- Key files: `cmd/local-probe-mcp/main.go`,
  `cmd/local-probe-preview/main.go`, `cmd/readcore-demo/main.go`.

**`internal/readcore/`, `internal/config/`, `internal/policy/`, and `internal/rootfs/`:**

- Purpose: Form the core authorization and safe local-file read boundary.
- Contains: Transport-free read contracts, immutable config/revision stores,
  bound scopes, Go `os.Root` adapters, file identity checks, and platform path
  implementations.
- Key files: `internal/readcore/engine.go`, `internal/config/config.go`,
  `internal/config/filestore.go`, `internal/policy/policy.go`,
  `internal/rootfs/source.go`.

**`internal/mcpserver/` and `internal/cfaccess/`:**

- Purpose: Expose the read/discovery core through the official MCP SDK and
  authenticate local-token or Cloudflare Access ingress.
- Contains: Tool schemas/handlers, strict argument decoding, auth middleware,
  public-host validation, structured result serialization, and JWT verifier.
- Key files: `internal/mcpserver/server.go`,
  `internal/cfaccess/verifier.go`.

**`internal/search/` and `internal/workspacesnapshot/`:**

- Purpose: Implement bounded directory listing, find, literal search, tree
  traversal, signed continuation cursors, and path-derived workspace summaries.
- Contains: Source ports, walkers, cursor payloads, coverage/budget models, and
  manifest/language/evidence derivation.
- Key files: `internal/search/types.go`, `internal/search/service.go`,
  `internal/search/walker.go`, `internal/search/cursor.go`,
  `internal/search/tree.go`, `internal/workspacesnapshot/engine.go`.

**`internal/environment/`:**

- Purpose: Provide path-free coarse environment facts and trusted-candidate
  discovery without PATH lookup or process execution.
- Contains: Environment types, fixed candidate inspection, and OS-specific path
  classification.
- Key files: `internal/environment/types.go`,
  `internal/environment/discover.go`.

**`internal/audit/`, `internal/auditreader/`, `internal/supportbundle/`, and `internal/releasecheck/`:**

- Purpose: Persist, read, sanitize, export, and evaluate local operational
  evidence.
- Contains: Bounded JSONL sink/rotation, allowlisted audit readers, sanitized
  support bundle models, and local release-evidence gates.
- Key files: `internal/audit/sink.go`, `internal/audit/types.go`,
  `internal/auditreader/reader.go`, `internal/supportbundle/bundle.go`,
  `internal/releasecheck/releasecheck.go`.

**`internal/desktopadmin/`, `internal/previewapp/`, `internal/previewconnect/`, `internal/previewui/`, and `internal/workspaceadmin/`:**

- Purpose: Implement the local read-only Preview and its explicit workspace
  root configuration workflow.
- Contains: Sanitized status projections, model refresh, fixed MCP/tunnel child
  control, Win32/tray UI, and revision-aware root add/remove operations.
- Key files: `internal/desktopadmin/desktopadmin.go`,
  `internal/previewapp/previewapp.go`, `internal/previewconnect/connect.go`,
  `internal/previewui/model.go`, `internal/previewui/window_windows.go`,
  `internal/workspaceadmin/workspaceadmin.go`.

**`internal/admission/`, `internal/supervisor/`, `internal/lifecycleadapter/`, `internal/connectionmanager/`, `internal/runtimeowner/`, and `internal/readiness/`:**

- Purpose: Keep future per-connection lifecycle, runtime ownership, admission,
  and readiness concerns behind typed local contracts.
- Contains: Gate permits, lifecycle epochs/capabilities, state machine/retry
  logic, orchestration order, Windows Job ownership, and attestation replay
  evaluation.
- Key files: `internal/admission/admission.go`,
  `internal/supervisor/supervisor.go`, `internal/supervisor/spec.go`,
  `internal/lifecycleadapter/adapter.go`, `internal/connectionmanager/manager.go`,
  `internal/runtimeowner/runtimeowner.go`, `internal/readiness/readiness.go`.
- Boundary: These packages explicitly retain non-production status and are not
  the active MCP service lifecycle.

**`internal/commandprofile/`, `internal/commandexec/`, `internal/commandpath/`, `internal/confirmation/`, `internal/probe/`, `internal/gitprobe/`, and `internal/networkguard/`:**

- Purpose: Define fixed local command/probe and network-deny contracts without
  accepting arbitrary shell input.
- Contains: Immutable profiles/typed slots, one-time confirmation, path/image
  binding, fixed version probes, Git read planning/parsing, and fail-closed
  network guard interfaces/fakes.
- Key files: `internal/commandprofile/profile.go`,
  `internal/commandexec/executor.go`, `internal/commandexec/prepared.go`,
  `internal/commandpath/binding.go`, `internal/probe/probe.go`,
  `internal/gitprobe/gitprobe.go`, `internal/networkguard/guard.go`.

**`internal/catalog/`:**

- Purpose: Hold a local metadata-only candidate cache that requires live
  verification by the direct search/rootfs path.
- Key file: `internal/catalog/catalog.go`.
- Boundary: Catalog is not wired into MCP/search by default and reports
  `ProductionReady=false`.

**`configs/`:**

- Purpose: Store safe, non-secret examples for local configuration and tunnel
  setup.
- Key files: `configs/local-probe.example.json`,
  `configs/cloudflare-tunnel.example.json`,
  `configs/tunnel-client.example.yaml`.

**`docs/`:**

- Purpose: Record the project plan separately from implemented status and
  preserve contracts/evidence for future phases.
- Key files: `docs/MASTER_PLAN.zh-CN.md`,
  `docs/IMPLEMENTATION_STATUS.md`, `docs/TOOL_CONTRACTS.md`,
  `docs/THREAT_MODEL.md`, `docs/NEXT_PHASE_PLAN.zh-CN.md`,
  `docs/evidence/`.

**`manual-test-targets/`:**

- Purpose: Provide small deterministic fixtures for MCP discovery/read,
  Unicode, line endings, pagination, deep trees, and denied paths.
- Key files: `manual-test-targets/README.md`,
  `manual-test-targets/r2/README.md`, `manual-test-targets/r4/README.md`.

**`scripts/`:**

- Purpose: Automate local startup/build/setup and bounded Windows acceptance
  checks without embedding those concerns in domain packages.
- Key files: `scripts/Start-LocalProbeMcp.ps1`,
  `scripts/Start-LocalProbePreview.ps1`,
  `scripts/Start-CloudflareTunnel.ps1`, `scripts/acceptance.ps1`.

**`.github/workflows/`:**

- Purpose: Run matrix Go tests/build/vet/format checks and Windows release
  preflight.
- Key files: `.github/workflows/readcore.yml`,
  `.github/workflows/release-preflight.yml`.

## Key File Locations

**Entry Points:**

- `cmd/local-probe-mcp/main.go`: Authenticated loopback MCP server composition.
- `cmd/readcore-demo/main.go`: Explicit operator-selected local read demo.
- `cmd/local-probe-preview/main.go`: Windows Preview/tray composition.

**Configuration:**

- `internal/config/config.go`: Strict in-memory config model and revisioned
  `Store`.
- `internal/config/filestore.go`: Local JSON persistence, atomic replace, and
  revision-CAS helpers.
- `configs/*.example.*`: Non-secret examples only.
- `.runtime/` and `.secrets/`: Local runtime/credential locations; do not read
  or commit their contents.

**Core Logic:**

- `internal/readcore/engine.go`: Bounded read algorithm.
- `internal/policy/policy.go`: Authenticated scope binding and deny/ignore
  decisions.
- `internal/rootfs/source.go`: Actual configured-root/file/directory adapter.
- `internal/search/service.go`: Bounded discovery and text search.
- `internal/mcpserver/server.go`: MCP tool registration, auth, and handlers.

**Testing:**

- `internal/**/*_test.go`: Co-located package unit, integration, platform,
  race-seam, and benchmark tests.
- `cmd/**/*_test.go`: Executable composition and adapter tests.
- `manual-test-targets/`: Manual MCP/UI fixtures.
- `.github/workflows/readcore.yml`: CI commands for matrix tests, race/fuzz,
  vet, build, and formatting.
- `scripts/acceptance.ps1`: Windows local release-evidence scaffolding.

## Naming Conventions

**Files:**

- Use lowercase Go filenames matching the package concern, for example
  `internal/readcore/engine.go` and `internal/search/service.go`.
- Put tests beside implementation with `_test.go`, for example
  `internal/rootfs/source_test.go`.
- Use build/platform suffixes such as `_windows.go`, `_unix.go`, `_linux.go`,
  and `_other.go`, for example `internal/previewui/window_windows.go` and
  `internal/rootfs/identity_windows.go`.
- Use descriptive PascalCase Markdown names for durable docs, for example
  `docs/IMPLEMENTATION_STATUS.md` and `.planning/codebase/ARCHITECTURE.md`.

**Directories:**

- Use lowercase, compact Go package names without underscores, for example
  `internal/workspacesnapshot/` and `internal/connectionmanager/`.
- Group tests and platform variants inside the package they exercise rather
  than creating parallel test trees.
- Keep executable directories under `cmd/<binary-name>/` and local automation
  under `scripts/`.

**Symbols and APIs:**

- Export constructors as `New...`, stable sentinel errors as `Err...`, and
  typed result/status values with explicit `SchemaVersion` fields; examples are
  `internal/readcore/types.go`, `internal/search/types.go`, and
  `internal/desktopadmin/desktopadmin.go`.
- Keep sensitive capability fields private and reject JSON serialization for
  local-only values, as in `internal/admission/admission.go` and
  `internal/readiness/readiness.go`.

## Where to Add New Code

**New MCP read-only tool:**

- Add the domain request/result and bounded algorithm under the closest package
  in `internal/readcore/`, `internal/search/`, `internal/workspacesnapshot/`,
  or a new transport-free `internal/<domain>/` package.
- Add the MCP schema, handler, tool registration, authorization check, stable
  error mapping, and structured result wiring in `internal/mcpserver/server.go`.
- Reuse `internal/policy/BoundScope` and `internal/rootfs/` source binders;
  never add an OS path opener to `internal/mcpserver/`.
- Add co-located tests under the affected package and, when the wire contract
  changes, update `docs/TOOL_CONTRACTS.md` and the MCP tests.

**New filesystem/search capability:**

- Put traversal/scan logic in `internal/search/` and read logic in
  `internal/readcore/`; extend the narrow source interfaces in
  `internal/search/types.go` or `internal/readcore/types.go` only when the
  operation needs a new primitive.
- Implement actual root/handle behavior in `internal/rootfs/source.go` and its
  platform helpers, including deny/reparse/generation checks.

**New configuration field or profile:**

- Extend the value types, constructors, cloning, JSON schema, validation, and
  revision semantics in `internal/config/config.go` or the focused companion
  file such as `internal/config/environment.go` or
  `internal/config/commandprofiles.go`.
- Update `configs/` examples and add `internal/config/*_test.go` coverage for
  malformed, duplicate, dangling, and revision-conflict inputs.

**New Preview screen/projection:**

- Add bounded local data types and refresh behavior in
  `internal/previewapp/previewapp.go` or `internal/desktopadmin/desktopadmin.go`.
- Add UI model/adapters in `internal/previewui/model.go` and render/event code
  in `internal/previewui/window_windows.go`; keep non-Windows behavior in
  `internal/previewui/window_stub.go`.
- Route fixed child/tunnel actions through `internal/previewconnect/`, and
  route root mutations through `internal/workspaceadmin/`; do not invent a
  general command or management socket.

**New lifecycle or command execution capability:**

- Keep typed contracts in the corresponding package group under
  `internal/admission/`, `internal/supervisor/`, `internal/runtimeowner/`,
  `internal/readiness/`, `internal/commandprofile/`, `internal/commandexec/`,
  `internal/probe/`, or `internal/networkguard/`.
- Supply concrete OS/process/network adapters only after their production gate
  and evidence requirements are closed; do not register a remote MCP tool as a
  shortcut.

**Utilities:**

- Prefer a small helper beside the owning domain package. Share only stable,
  security-relevant primitives through an existing package such as
  `internal/readcore/` or `internal/config/`; avoid a catch-all utility package
  that can bypass policy or source interfaces.

## Special Directories

**`.planning/codebase/`:**

- Purpose: Generated GSD mapping documents consumed by planning/execution.
- Generated: Yes.
- Committed: The mapping workflow may commit these documents; keep them free of
  secrets and use exact analysis dates.

**`.runtime/`:**

- Purpose: Local runtime configuration and generated operational state used by
  scripts/Preview.
- Generated: Usually local/generated.
- Committed: No; ignored by `/.runtime/` in `.gitignore`.

**`.secrets/`:**

- Purpose: External credential material such as tunnel tokens referenced by
  local startup tooling.
- Generated: Local/operator-managed.
- Committed: No; ignored by `/.secrets/`. Never read or quote its contents.

**`bin/`, `tmp/`, `logs/`, and `coverage/`:**

- Purpose: Built executables, scratch files, logs, and coverage output.
- Generated: Yes.
- Committed: No according to `.gitignore`; source changes belong under `cmd/`,
  `internal/`, `scripts/`, or `docs/`.

**`manual-test-targets/`:**

- Purpose: Small intentional fixtures for manual and integration verification.
- Generated: No, except helper-prepared line-ending variants under the fixture
  tree.
- Committed: Yes; keep fixtures deterministic and free of real credentials.

**`docs/evidence/`:**

- Purpose: Machine-readable verification records and bounded evidence metadata.
- Generated: By verification workflows or manual evidence capture.
- Committed: Yes when reviewed; do not include tokens, private paths, or raw
  credentials.

---

*Structure analysis: 2026-09-19*
