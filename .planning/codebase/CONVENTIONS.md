---
last_mapped_commit: 9982a8f84a075436b16a2c7aafd61362ebe14146
last_mapped_at: 2026-09-19
---
# Coding Conventions

**Analysis Date:** 2026-09-19

## Naming Patterns

**Files:**

- Use lowercase Go filenames with descriptive words separated by underscores, for example `internal/readcore/engine.go`, `internal/config/filestore.go`, and `internal/commandprofile/profile.go`.
- Co-locate tests beside the implementation as `*_test.go`; use platform suffixes such as `internal/rootfs/source_windows_test.go`, `internal/rootfs/source_unix_test.go`, and `internal/runtimeowner/runtimeowner_other_test.go`.
- Put a matching `//go:build` constraint at the top of platform-specific files, as in `internal/environment/path_linux.go` and `internal/previewui/icon_stub.go`.

**Functions:**

- Exported functions and methods use PascalCase; unexported helpers use lowerCamelCase. Constructors consistently use `New`/`NewX` (`internal/config/config.go`, `internal/policy/policy.go`, `internal/readcore/engine.go`).
- Prefer the standard Go context-first signature and return an error last: `func (e *Engine) ReadBatch(ctx context.Context, ...) (BatchResult, error)` in `internal/readcore/engine.go`.
- Use descriptive behavior-oriented test names such as `TestSourceEnforcesDenyAndRevocationOnEveryHandleOperation` in `internal/rootfs/source_test.go`; fuzzers use `Fuzz...` and benchmarks use `Benchmark...` in `internal/readcore/engine_test.go`.
- Preserve Go initialisms (`ID`, `URL`, `HTTP`, `JSON`, `MCP`, `SHA256`) in exported names and use the same initialisms in JSON field names where the wire contract requires them, as in `internal/mcpserver/server.go`.

**Variables:**

- Use lowerCamelCase locals and short conventional names for narrow scopes (`ctx`, `err`, `req`, `cfg`, `i`); use descriptive names for security or budget state (`outputQuota`, `scanQuota`, `profileRevision`) in `internal/readcore/engine.go`.
- Name sentinel errors `Err...`, typed error codes `Code...`, and schema/constants with explicit domains, as in `internal/admission/admission.go` and `internal/audit/types.go`.
- Keep mutable backing slices/maps private and expose copies or immutable accessors. `internal/config/config.go` uses private fields plus `Clone`/accessor methods; `internal/policy/policy.go` binds a revision-scoped value.

**Types:**

- Use PascalCase for exported structs/interfaces and typed string enums (`type ErrorCode string` in `internal/admission/admission.go` and `type Component string` in `internal/audit/types.go`).
- Define small interfaces at the consuming boundary (`SourceBinder` in `internal/mcpserver/server.go`, `AuditRecorder` in `internal/commandexec/executor.go`) so tests and local adapters inject only the required behavior.
- Prefer immutable value objects with unexported fields and validated constructors for security-sensitive state (`internal/config/config.go`, `internal/commandprofile/profile.go`, `internal/confirmation/confirmation.go`).

## Code Style

**Formatting:**

- Run `gofmt` on `internal/` and `cmd/`; the repository workflow enforces `test -z "$(gofmt -l internal cmd)"` in `.github/workflows/readcore.yml`.
- Use standard Go tabs/brace placement and let `gofmt` own import grouping. `gofmt -l internal cmd` is clean for the current tree.
- Keep package comments and exported API comments adjacent to declarations, as shown in `internal/config/config.go`, `internal/policy/policy.go`, and `internal/mcpserver/server.go`.

**Linting:**

- `go vet ./...` is the repository-wide static check in `AGENTS.md`, `README.md`, and `.github/workflows/readcore.yml`.
- No `.golangci.yml`, `staticcheck.conf`, `Makefile`, or other project-specific lint configuration is present; do not assume a formatter or linter beyond `gofmt`/`go vet`.
- Build and test all packages with `go build ./...` and `go test ./...` before changing cross-package contracts; CI runs both from `.github/workflows/readcore.yml`.

## Import Organization

**Order:**

1. Standard library imports (`context`, `errors`, `fmt`, `io`, `sync`, etc.).
2. A blank line.
3. Module and third-party imports, grouped under `github.com/LE-saber/Local-Probe/...` and external SDK paths.

`internal/readcore/engine.go`, `internal/mcpserver/server.go`, and `internal/config/config_test.go` show this exact gofmt-managed grouping.

**Path Aliases:**

- No path aliases are configured; import local packages by the module path from `go.mod`, for example `github.com/LE-saber/Local-Probe/internal/policy`.
- Keep imports package-oriented and one-directional: command entry points in `cmd/` depend on `internal/`, while core packages do not import command packages (`cmd/local-probe-mcp/main.go`, `internal/mcpserver/server.go`).

## Error Handling

**Patterns:**

- Declare stable package errors with `errors.New` and test them with `errors.Is`, as in `internal/policy/policy.go`, `internal/config/config.go`, and `internal/commandprofile/profile.go`.
- Wrap errors with `%w` only when callers need the sentinel identity; add stable, path-free context (`fmt.Errorf("%w: invalid limits", ErrInvalidOptions)` in `internal/mcpserver/server.go`).
- Use typed errors when callers need a stable code without exposing request data. `internal/admission/admission.go` implements `Error.Is`, while `internal/environment/errors.go` and `internal/gitprobe/gitprobe.go` carry constrained codes.
- Validate at constructors and parse boundaries. `internal/config/config.go` rejects unknown JSON fields, duplicate keys, malformed trailing values, invalid UTF-8, and dangling references before returning a `Config`.
- Fail closed at authorization, lifecycle, audit, and execution boundaries. `internal/policy/policy.go`, `internal/admission/admission.go`, and `internal/commandexec/executor.go` return stable denials rather than partial capabilities.
- Never echo raw paths, tokens, executable details, or underlying sensitive errors. `internal/audit/command.go`, `internal/mcpserver/server.go`, `internal/probe/probe.go`, and `internal/previewui/connection.go` sanitize errors and use generic stable codes.
- Honor `context.Context` cancellation/deadlines and classify them into stable errors; `internal/readcore/engine.go` maps cancellation/deadlines and clears content/continuations on failure.
- Return zero/cleared result state when an operation fails. `internal/readcore/engine.go` uses `clearContent`, and `internal/commandexec/executor.go` returns an empty `Result` on rejected or failed execution.

## Logging

**Framework:**

- There is no centralized structured application logger. CLI entry points use short `fmt.Fprintln`/`fmt.Fprintf` messages to stderr/stdout (`cmd/local-probe-mcp/main.go`, `cmd/readcore-demo/main.go`).
- Structured operational evidence is emitted through the audit package as bounded JSONL (`internal/audit/sink.go`, `internal/audit/types.go`), not through arbitrary log lines.

**Patterns:**

- Emit allowlisted identifiers, counters, stable error codes, and durations; do not include request bodies, paths, argv, environment, or credential material. `internal/audit/command.go` and `internal/auditreader/reader.go` enforce this boundary.
- Convert unknown/internal failures to stable categories before exposing them to MCP or UI callers (`internal/mcpserver/server.go`, `internal/desktopadmin/desktopadmin.go`).
- Tests should assert both the safe event and absence of sensitive sentinels, following `internal/audit/command_test.go` and `internal/mcpserver/server_test.go`.

## Comments

**When to Comment:**

- Comment exported APIs and package boundaries, especially when a type is local-only, immutable, or intentionally fail-closed (`internal/admission/admission.go`, `internal/catalog/catalog.go`).
- Explain security invariants and why an apparently simpler operation is rejected, for example the revision lease in `internal/config/config.go` and the no-shell execution boundary in `internal/commandexec/executor.go`.
- Document current limitations and evidence scope instead of promising future behavior; `internal/lifecycleadapter/adapter.go` and `internal/networkguard/fake.go` explicitly mark non-production primitives.

**JSDoc/TSDoc:**

- Not applicable; this is a Go codebase. Use Go doc comments for exported declarations, and keep comments in full sentences where possible (`internal/readcore/types.go`, `internal/mcpserver/server.go`).

## Function Design

**Size:**

- Keep operations focused around one boundary or invariant. Large domain files are split into helpers and platform files (`internal/readcore/engine.go`, `internal/config/config.go`, `internal/desktopadmin/desktopadmin.go`); extract a helper when it isolates validation, serialization, or a side effect.

**Parameters:**

- Pass `context.Context` first for cancelable work and use an options/config struct when a constructor has multiple policy knobs (`internal/mcpserver/server.go`, `internal/previewapp/previewapp.go`).
- Accept interfaces for external effects and inject clocks, timers, sources, runners, or recorders at the boundary (`internal/supervisor/supervisor.go`, `internal/networkguard/guard.go`, `internal/commandexec/executor.go`).
- Copy caller-owned slices/maps at entry and before returning them; use `Clone`/defensive accessors instead of exposing mutable internal state (`internal/config/config.go`, `internal/commandprofile/profile.go`).

**Return Values:**

- Return typed domain values plus `error`; callers must inspect errors before using values (`internal/readcore/engine.go`, `internal/search/service.go`).
- Keep result fields explicit and bounded: coverage, warnings, budget, continuation, and stable error codes are preferred over implicit partial success (`internal/search/types.go`, `internal/workspacesnapshot/types.go`).
- Do not serialize local-only capabilities or opaque handles. `internal/confirmation/confirmation.go`, `internal/catalog/catalog.go`, and `internal/commandexec/prepared.go` explicitly reject JSON marshal/unmarshal for those values.

## Module Design

**Exports:**

- Expose only constructors, immutable accessors, narrow interfaces, and stable error/code contracts. Keep implementation fields and wire/raw structs private (`internal/config/config.go`, `internal/audit/types.go`).
- Keep platform implementations behind same-package files with build constraints (`internal/rootfs/identity_windows.go`, `internal/rootfs/identity_unix.go`, `internal/probe/execution_windows.go`, `internal/probe/execution_unsupported.go`).

**Barrel Files:**

- Not used. Go packages expose declarations directly; place a new implementation beside related package files rather than creating an index/barrel file (`internal/search/`, `internal/previewui/`).
- Keep test-only helpers in the relevant `*_test.go` file or a clearly named test helper command (`internal/probe/probe_test.go`, `internal/probe/testhelper/main.go`).

---

*Convention analysis: 2026-09-19*
