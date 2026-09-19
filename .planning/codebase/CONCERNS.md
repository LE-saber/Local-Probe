---
last_mapped_commit: 9982a8f84a075436b16a2c7aafd61362ebe14146
last_mapped_at: 2026-09-19
---
# Codebase Concerns

**Analysis Date:** 2026-09-19

## Tech Debt

**Production lifecycle is contract-only:**

- Issue: `internal/admission/`, `internal/supervisor/`, `internal/lifecycleadapter/`, `internal/connectionmanager/`, `internal/runtimeowner/`, and `internal/readiness/` expose injected state-machine contracts while their production gates remain false. They do not own a real MCP child, tunnel child, broker, or OS readiness proof.
- Files: `internal/supervisor/supervisor.go`, `internal/runtimeowner/runtimeowner.go`, `internal/readiness/readiness.go`, `internal/connectionmanager/manager.go`.
- Impact: A green fake-lifecycle test cannot establish process ownership, tunnel health, cleanup, or multi-connection isolation for a release.
- Fix approach: Wire one trusted launcher/broker, real local-MCP ping and remote health evidence, OS ownership/network enforcement, and long-running dual-connection tests before enabling any production gate.

**Configuration persistence is not a process boundary:**

- Issue: `FileStore` uses process-local locks and path-based `Lstat`/open/replace operations; it explicitly lacks an OS inter-process lock, ACL enforcement, and complete reparse/hardlink/TOCTOU protection.
- Files: `internal/config/filestore.go`, `internal/config/filestore_replace_windows.go`, `internal/workspaceadmin/workspaceadmin.go`.
- Impact: Two processes can lose updates or race configuration/root replacement; a successful local CAS is not an authorization-store guarantee.
- Fix approach: Add a protected directory/ACL policy, OS-level lock, handle-based identity checks, and a production capability separate from the convenience `FileStore`.

**Audit health is not wired to request admission:**

- Issue: Normal audit events are queued and may return `ErrQueueFull`; write/rotation failures mark the sink degraded, while the MCP path ignores `audit.Emit` errors and the admission health gate is only an injectable contract.
- Files: `internal/audit/sink.go`, `internal/mcpserver/server.go`, `internal/admission/admission.go`.
- Impact: Calls can continue after audit records are dropped or persistence fails, weakening incident reconstruction and command-admission guarantees.
- Fix approach: Connect sink health to `SetAuditAvailable(false)`, define recovery semantics, and make the production ingress reject new work until durable audit health is restored.

**Release evidence and source tree are mixed:**

- Issue: The repository tracks manual fixtures and evidence documents alongside product code, while local build/config output exists under ignored `.runtime/`, `bin/`, and `tmp/` directories. `docs/evidence/K0-local-verification.json` records Go 1.23 and source blobs from an earlier baseline, not the current module/revision.
- Files: `manual-test-targets/`, `docs/evidence/K0-local-verification.json`, `docs/evidence/P04-platform-verification.json`, `.gitignore`.
- Impact: Source archives can ship test-only content or stale machine evidence; careless workspace archiving can capture ignored runtime configuration and build artifacts.
- Fix approach: Define an explicit source/package manifest, keep evidence tied to an immutable release commit, and package only built artifacts plus reviewed runtime examples.

## Known Bugs

**No reproducible application failure detected in the bounded check:**

- Symptoms: `go test ./...` passes for the packages under `cmd/` and `internal/`.
- Files: `go.mod`, `internal/*_test.go`, `cmd/*/*_test.go`.
- Trigger: Not detected by this scan; passing tests do not cover live external services, release packaging, or hostile OS races.
- Workaround: Treat the test result as local evidence only and apply the security, performance, and release gates below.

## Security Considerations

**Root validation has an explicit TOCTOU and unsupported-platform gap:**

- Risk: `rootfs.New` validates a path before `os.OpenRoot`; the non-Windows fallback reports no file identity/link capability, so hardlink/reparse guarantees are not equivalent on every supported build target.
- Files: `internal/rootfs/source.go`, `internal/rootfs/identity_other.go`, `internal/rootfs/identity.go`.
- Current mitigation: Go `os.Root`, repeated component checks, and fail-closed identity checks on Windows and supported Unix implementations.
- Recommendations: Open and validate a stable root handle atomically, reject platforms without identity support instead of returning an `m0` token, and retain explicit local-administrator limits.

**Preview child binaries are checked by metadata then launched by path:**

- Risk: The connector checks `os.Stat`/regular-file status and later calls `exec.Command`; it does not bind the executable to a final handle/hash or a Job Object. A replacement can occur between validation and launch, and descendants are not guaranteed to die with the direct child.
- Files: `internal/previewconnect/connect.go`, `internal/previewconnect/process_windows.go`.
- Current mitigation: Fixed argument lists, loopback origin checks, token-file separation, bounded output sanitization, and a Preview-only production gate.
- Recommendations: Use a trusted broker with final identity binding, suspended-child verification, Job ownership, descendant cleanup, and signed/pinned sidecar artifacts.

**MCP listener safety depends on composition callers:**

- Risk: `Server.Handler` cannot constrain the supplied `net.Listener`; accidental non-loopback binding would expose a powerful local filesystem reader beyond the intended boundary.
- Files: `internal/mcpserver/server.go`, `cmd/local-probe-mcp/main.go`, `scripts/Start-LocalProbeMcp.ps1`.
- Current mitigation: The command entry point selects loopback and the handler requires local bearer or Cloudflare Access authentication with host checks.
- Recommendations: Make listener construction part of the trusted server composition, assert loopback at startup, and add a negative test for non-loopback configuration.

## Performance Bottlenecks

**Paged search replays directory prefixes:**

- Problem: Cursor continuation reopens every frame and replays directory offsets before returning the next page.
- Files: `internal/search/walker.go`, `internal/search/service.go`, `internal/search/cursor.go`.
- Cause: The direct filesystem walker has no persistent offset/index and must preserve native directory order for cursor correctness.
- Improvement path: Measure replay cost on representative large trees, cap replay work separately from returned entries, and introduce an opt-in freshness-aware index only with direct-scan fallback.

**The catalog has no demonstrated end-to-end speedup:**

- Problem: Candidate queries can be faster in isolation, but every candidate requires live rootfs verification; the checked-in evidence reports end-to-end catalog work slower than direct scans.
- Files: `internal/catalog/catalog.go`, `internal/search/catalog_benchmark_test.go`, `docs/R7_INDEX_EVIDENCE.zh-CN.md`.
- Cause: In-memory candidates are not authoritative and the catalog has no persistent, freshness-proven reconciliation or watcher path.
- Improvement path: Keep catalog disabled by default, rerun 1m and real-disk comparisons, and enable only when candidate verification plus freshness costs beat direct traversal.

**Per-request budgets multiply without a global scheduler:**

- Problem: Each MCP call creates a new read/search engine with its own workers, scan budget, timeout, and response budget; concurrent authenticated calls have no global byte, open-file, or fairness quota.
- Files: `internal/mcpserver/server.go`, `internal/readcore/engine.go`, `internal/search/service.go`.
- Cause: Limits are transport/request-local and the lifecycle/admission gate is not connected to MCP.
- Improvement path: Add global and per-connection admission, wire-byte accounting after serialization, cancellation-aware queueing, and fair scheduling before exposing the service to untrusted concurrency.

## Fragile Areas

**Native Windows UI has a very large untested surface:**

- Files: `internal/previewui/window_windows.go`, `internal/previewui/workspace_picker_windows.go`, `internal/previewui/workspace_path_dialog_windows.go`.
- Why fragile: The main window procedure is roughly 2,300 lines of Win32 state/event code; tests cover model and adapters but do not exercise the native window/dialog lifecycle in CI.
- Safe modification: Keep UI state transitions in tested model/adapter code, make small platform changes, and run an interactive Windows smoke test for window, tray, DPI, picker, refresh, and shutdown behavior.
- Test coverage: No dedicated `*_test.go` accompanies the three large native window/picker implementations.

**Workspace root mutation crosses multiple trust boundaries:**

- Files: `internal/workspaceadmin/workspaceadmin.go`, `internal/workspaceadmin/path_safety_windows.go`, `internal/workspaceadmin/path_safety_other.go`.
- Why fragile: UI path checks, revision CAS, config persistence, root opening, and reconnect are separate operations; path metadata can change after validation.
- Safe modification: Preserve revision checks, revalidate the selected root through the same production root-handle adapter used for reads, and keep mutation serialized per config path.
- Test coverage: Unit tests cover path classifications and CAS, but no production multi-process mutation or root-swap test exists.

## Scaling Limits

**Memory and request fan-out:**

- Current capacity: Default read batches allow 32 items, 4 workers, 128 KiB output, and up to 8 MiB read/scan budgets; search/snapshot calls have independent limits.
- Limit: Concurrent MCP callers can multiply those limits and each search cursor can retain/replay directory frames; audit queues also have finite capacity and drop normal events when full.
- Scaling path: Use a global admission budget, per-connection fairness, explicit queue-depth telemetry, and a load test covering many simultaneous roots before increasing per-call limits.

## Dependencies at Risk

**Go toolchain policy is split across module, workstation, and CI:**

- Risk: `go.mod` declares `go 1.25.0`, local evidence records Go 1.23, the workstation is Go 1.26.0, and CI selects Go 1.26.5; there is no `toolchain` directive or release toolchain manifest.
- Impact: Reproducibility and platform behavior can differ, especially for `os.Root` and Windows APIs.
- Migration plan: Pin one supported Go release in a checked-in build/release manifest, regenerate evidence with that toolchain, and make CI/release scripts consume the same value.

## Missing Critical Features

**Supply-chain and distribution gates are absent:**

- Problem: There is no root `LICENSE`, `NOTICE`, third-party attribution file, SBOM, CVE report, artifact signature, provenance record, portable package, or install/update/uninstall rollback matrix.
- Blocks: A distributable release cannot be legally, reproducibly, or operationally verified.
- Files: `go.mod`, `go.sum`, `docs/UPSTREAM_REVIEW.md`, `scripts/acceptance.ps1`, `.github/workflows/release-preflight.yml`.

**The release workflow is a preflight, not a release authorization:**

- Problem: The workflow requires `release_ready` to be `false`, and `scripts/acceptance.ps1` hard-codes the same result while checking only local evidence.
- Blocks: A green GitHub Actions job cannot be interpreted as a releasable build.
- Files: `.github/workflows/release-preflight.yml`, `scripts/acceptance.ps1`, `internal/releasecheck/releasecheck.go`, `docs/R9_RELEASE_CHECKLIST.zh-CN.md`.

## Test Coverage Gaps

**Live external and deployed E2E:**

- What's not tested: Real Cloudflare/Tunnel health, multiple real principals/connections, long-running reconnect/cleanup, ChatGPT account compatibility, and browser-side MCP behavior.
- Files: `internal/cfaccess/verifier_test.go`, `internal/mcpserver/cloudflare_test.go`, `internal/previewconnect/live_test.go`, `.github/workflows/release-preflight.yml`.
- Risk: Local OIDC/HTTP fakes can pass while edge identity, tunnel, or account policy fails in deployment.
- Priority: High.

**OS enforcement and native process ownership:**

- What's not tested: A trusted launcher/broker, WFP enforcement, direct/leaf Job membership, cross-process config locking, and hostile root replacement on every supported platform.
- Files: `internal/networkguard/wfp/disabled.go`, `internal/runtimeowner/`, `internal/config/filestore.go`, `internal/rootfs/source_windows_security_test.go`.
- Risk: Contract/fake tests can be mistaken for security proof.
- Priority: High.

**Release/package hygiene:**

- What's not tested: A clean source archive and clean Windows install that exclude `manual-test-targets/`, stale evidence, ignored `.runtime/`, `bin/`, and `tmp/` content while retaining all required runtime assets.
- Files: `manual-test-targets/`, `docs/evidence/`, `scripts/Build-LocalProbePreview.ps1`, `.gitignore`.
- Risk: Test fixtures, stale claims, local configuration, or build outputs can ship accidentally.
- Priority: Medium.

---

*Concerns audit: 2026-09-19*
