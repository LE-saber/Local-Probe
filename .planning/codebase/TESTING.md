---
last_mapped_commit: 9982a8f84a075436b16a2c7aafd61362ebe14146
last_mapped_at: 2026-09-19
---
# Testing Patterns

**Analysis Date:** 2026-09-19

## Test Framework

**Runner:**

- Go's standard `testing` package is used across the 78 co-located test files; module/toolchain requirements are in `go.mod`.
- There is no `testify`, `gomock`, or other assertion/mocking dependency. Tests use `testing.T`, `testing.TB`, `testing.F`, and `testing.B` directly (`internal/readcore/engine_test.go`, `internal/mcpserver/server_test.go`).
- CI configuration is in `.github/workflows/readcore.yml`; Windows release/preflight checks are in `.github/workflows/release-preflight.yml`.

**Assertion Library:**

- Use `t.Fatal`/`t.Fatalf` for setup or invariant failures and `t.Error`/`t.Errorf` for independent assertion failures (`internal/config/config_test.go`, `internal/mcpserver/server_test.go`).
- Use `errors.Is`/`errors.As` for stable error contracts rather than matching incidental strings (`internal/policy/policy_test.go`, `internal/cfaccess/verifier_test.go`). Use direct field comparisons, `reflect.DeepEqual`, or JSON round trips for values (`internal/config/config_test.go`).

**Run Commands:**

~~~bash
go test ./...                                      # all package tests
go test -race ./...                                # race-enabled suite
go test -count=1 -cover ./...                      # CI run with coverage
go vet ./...                                       # static checks
go build ./...                                     # compile all commands/packages
gofmt -l internal cmd                              # must print nothing
go test -coverprofile=coverage.out ./...           # write a coverage profile
go tool cover -func=coverage.out                  # inspect statement coverage
go test ./internal/readcore -run='^$' -fuzz=FuzzValidPath -fuzztime=5s -parallel=2
go test ./internal/readcore -run='^$' -fuzz=FuzzUTF8Pagination -fuzztime=5s -parallel=2
go test ./internal/readcore -run='^$' -bench=BenchmarkLargeRange -benchmem -count=3
~~~

`README.md`, `AGENTS.md`, and `.github/workflows/readcore.yml` are the source of truth for normal verification commands. The current local mapping run passes `go test ./...`, `go test -race ./...`, `go vet ./...`, and `gofmt -l internal cmd`.

## Test File Organization

**Location:**

- Tests are co-located with their package under `internal/<package>/` or `cmd/<command>/`; there is no repository-wide `testdata/` convention (`internal/readcore/engine_test.go`, `cmd/readcore-demo/main_test.go`).
- Real temporary files/directories are created with `t.TempDir()` and removed by the test framework. Inline JSON/config strings and package-local fixture builders are preferred over checked-in fixtures (`internal/config/config_test.go`, `internal/mcpserver/server_test.go`).
- `internal/probe/testhelper/main.go` is a deliberately small helper executable built by `TestMain` in `internal/probe/probe_test.go` for process-boundary tests.

**Naming:**

- Unit/integration tests use `*_test.go` and names of the form `Test<Type><Behavior>`, with behavior words such as `Rejects`, `Preserves`, `FailsClosed`, `Uses`, or `Reports` (`internal/rootfs/source_test.go`, `internal/admission/admission_test.go`).
- Platform tests combine a build constraint with a suffix, for example `internal/rootfs/source_windows_security_test.go`, `internal/rootfs/source_unix_test.go`, and `internal/environment/environment_symlink_test.go`.
- Fuzz targets are `FuzzValidPath` and `FuzzUTF8Pagination` in `internal/readcore/engine_test.go`; benchmarks are `BenchmarkLargeRange` there and synthetic search benchmarks in `internal/search/benchmark_harness_test.go`.
- Synthetic search benchmark/evidence files intentionally use external package `search_test` (`internal/search/benchmark_harness_test.go`) to exercise the public API; ordinary package tests generally use the package under test.

**Structure:**

~~~text
internal/<package>/implementation.go
internal/<package>/implementation_test.go
internal/<package>/<platform>_windows.go
internal/<package>/<platform>_unix.go
internal/<package>/<platform>_windows_test.go
cmd/<command>/main.go
cmd/<command>/main_test.go
~~~

Keep a platform-specific test beside the implementation it validates and put the build expression at the top of both files (`internal/probe/execution_windows.go`, `internal/probe/execution_unsupported_test.go`).

## Test Structure

**Suite Organization:**

~~~go
func TestEnvironmentToolsValidationAndLegacyCompatibility(t *testing.T) {
    base, err := Parse([]byte(validJSON))
    if err != nil {
        t.Fatal(err)
    }
    cases := []struct {
        name string
        tools []EnvironmentTool
    }{{name: "duplicate logical id", tools: ...}}
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if _, err := NewWithEnvironmentTools(..., tc.tools); err == nil || !errors.Is(err, ErrInvalid) {
                t.Fatalf("accepted invalid environment tools: %v", err)
            }
        })
    }
}
~~~

This table-driven and subtest style is used in `internal/config/config_test.go`, `internal/cfaccess/verifier_test.go`, and `internal/probe/probe_test.go`.

**Patterns:**

- Call `t.Helper()` in fixture/build/assertion helpers, often with `testing.TB` so the same helper works for tests and benchmarks (`internal/readcore/engine_test.go`, `internal/search/benchmark_harness_test.go`).
- Fail setup immediately; use `t.Cleanup` or `defer` for sinks, servers, source handles, and generated resources (`internal/audit/audit_test.go`, `internal/cfaccess/verifier_test.go`, `internal/rootfs/source_test.go`).
- Exercise public behavior through the real package boundary and assert side effects as well as returned values: coverage counters, continuation state, open/close counts, redaction, and revision invalidation are checked (`internal/readcore/engine_test.go`, `internal/search/search_test.go`, `internal/rootfs/search_integration_test.go`).
- Prefer deterministic fixtures with explicit limits and stable keys/tokens. Tests that need time use bounded contexts or injected clocks/timers rather than unbounded sleeps (`internal/admission/admission_test.go`, `internal/supervisor/supervisor_test.go`).
- Only two tests opt into `t.Parallel()` (`cmd/local-probe-mcp/main_test.go`, `internal/mcpserver/server_test.go`); do not add parallelism when tests share process, filesystem, environment, or network state unless the fixture is isolated.
- Security tests assert non-disclosure explicitly: malformed input must not echo paths/tokens, and audit/MCP responses must not contain raw request details (`internal/audit/command_test.go`, `internal/mcpserver/server_test.go`).

## Mocking

**Framework:**

- No mocking framework is installed. Use small manually implemented fakes, closures, deterministic sources, and dependency injection at package boundaries (`internal/networkguard/fake.go`, `internal/connectionmanager/manager_test.go`).

**Patterns:**

~~~go
executor.admit = func(commandprofile.Profile, commandprofile.DeveloperMode, string, commandprofile.EnforcementCapability) error {
    return nil
}
executor.run = func(context.Context, commandprofile.Profile, commandprofile.Variant) (Result, probe.ProcessOutcome, error) {
    return Result{CommandID: "codex_version", VariantID: "short", Version: "1.2.3"}, successfulOutcome(), nil
}
~~~

The closure seams in `internal/commandexec/executor_test.go` isolate admission and process execution while preserving executor sequencing and audit checks. The fake supervisor in `internal/connectionmanager/manager_test.go` uses a mutex, call counters, injected errors, and blocking channels to verify operation ordering and cancellation.

**What to Mock:**

- Mock process launchers, timers/clocks, audit recorders, policy/source binders, lifecycle runtimes, and failure-prone external seams (`internal/probe/probe_test.go`, `internal/supervisor/supervisor_test.go`, `internal/commandexec/executor_test.go`).
- Use `httptest.NewServer` and a custom `http.RoundTripper` for protocol/auth tests, not a fake HTTP client: `internal/mcpserver/server_test.go` exercises the actual MCP SDK client and `internal/cfaccess/verifier_test.go` provides an in-process OIDC provider.
- Use deterministic fake sources for read/search algorithm tests and real temporary directories for rootfs policy/integration tests (`internal/readcore/engine_test.go`, `internal/rootfs/search_integration_test.go`).

**What NOT to Mock:**

- Do not replace the rootfs, policy, or MCP integration boundary when validating authorization, path denial, continuation invalidation, or wire schemas; those behaviors are covered by real composition in `internal/rootfs/search_integration_test.go` and `internal/mcpserver/server_test.go`.
- Do not treat `internal/networkguard/fake.go` or injected supervisor fakes as OS/network proof; those tests validate local sequencing only. Keep real Windows/Unix behavior in the platform suites under `internal/rootfs/`, `internal/probe/`, and `internal/runtimeowner/`.

## Fixtures and Factories

**Test Data:**

~~~go
func newFixture(t *testing.T) *testFixture {
    t.Helper()
    root := t.TempDir()
    if err := os.WriteFile(root+"/hello.txt", []byte("hello from Local-Probe\n"), 0o600); err != nil {
        t.Fatal(err)
    }
    return &testFixture{root: root}
}
~~~

The equivalent real MCP setup continues through config, policy, rootfs, and server construction in `newTestServer` within `internal/mcpserver/server_test.go`. Reuse package-local factories such as `scopeFor`, `engineFor`, `newTestSource`, and `newFixture` rather than duplicating setup (`internal/readcore/engine_test.go`, `internal/rootfs/source_test.go`).

**Location:**

- No shared checked-in test fixture directory is detected. Use inline literals for small contracts, `t.TempDir()` for filesystem fixtures, and `internal/probe/testhelper/main.go` for deterministic child-process behavior.
- Manual acceptance inputs live under `manual-test-targets/`; they are not substitutes for package tests and should be exercised through `scripts/acceptance.ps1` and the commands documented in `README.md`.

## Coverage

**Requirements:**

- No numeric coverage threshold is enforced by `go.mod` or workflow configuration. CI records coverage with `go test -count=1 -cover ./...` in `.github/workflows/readcore.yml`.
- `docs/IMPLEMENTATION_STATUS.md` records historical package-specific evidence (including `internal/readcore` and `cmd/readcore-demo`); treat those values as evidence for that run, not a gate for every future change.

**View Coverage:**

~~~bash
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
go tool cover -html=coverage.out
~~~

Interpret coverage together with the OS, race, fuzz, and integration suites; line coverage alone does not prove path isolation, lifecycle sequencing, or secret redaction (`docs/THREAT_MODEL.md`, `docs/IMPLEMENTATION_STATUS.md`).

## Test Types

**Unit Tests:**

- Pure validation, parsing, allocation, error classification, immutable-value, and state-machine tests are concentrated in `internal/readcore/`, `internal/config/`, `internal/policy/`, `internal/admission/`, `internal/commandprofile/`, and `internal/audit/`.
- Keep unit tests deterministic and assert stable error identity/codes, bounded outputs, and no caller mutation (`internal/config/config_test.go`, `internal/admission/admission_test.go`).

**Integration Tests:**

- Filesystem composition is tested through config → policy → rootfs → search/readcore in `internal/rootfs/search_integration_test.go` and `internal/rootfs/source_test.go`.
- MCP integration uses `httptest.NewServer`, the official SDK client, auth transport, tool listing, calls, structured JSON, and audit assertions in `internal/mcpserver/server_test.go` and `internal/mcpserver/cloudflare_test.go`.
- Process integration compiles deterministic helper binaries in `internal/probe/probe_test.go`, then checks exact arguments/environment, output limits, timeout process-tree cleanup, and identity changes.

**E2E Tests:**

- No browser or deployed-service E2E framework is present. The closest end-to-end local path is the MCP HTTP client/server test in `internal/mcpserver/server_test.go`; Windows acceptance orchestration is provided by `scripts/acceptance.ps1`.
- Cloudflare/OIDC tests use local `httptest` providers in `internal/cfaccess/verifier_test.go` and `internal/mcpserver/cloudflare_test.go`, not a live external account.

**Platform and Adversarial Tests:**

- The CI matrix runs core tests on Ubuntu, Windows, and macOS and runs race/fuzz on Ubuntu (`.github/workflows/readcore.yml`). Platform-specific source/identity/process tests are selected by build tags in `internal/rootfs/`, `internal/probe/`, `internal/environment/`, and `internal/runtimeowner/`.
- Link, hardlink, reparse/junction, long-path, special-file, and swap-race cases belong in platform suites (`internal/rootfs/source_windows_security_test.go`, `internal/rootfs/source_unix_test.go`). Tests may `t.Skipf` only when the OS/filesystem cannot provide the required capability, as in `internal/rootfs/source_test.go`.

## Common Patterns

**Async Testing:**

~~~go
ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
defer cancel()
done := make(chan error, 1)
go func() { done <- operation(ctx) }()
if err := <-done; !errors.Is(err, ErrCancelled) { ... }
~~~

Use bounded contexts, channels, `sync.WaitGroup`, and injected barriers/timers for concurrent behavior. `internal/admission/admission_test.go`, `internal/lifecycleadapter/integration_test.go`, and `internal/supervisor/supervisor_test.go` demonstrate this style; always wait for goroutines and close/release barriers in cleanup.

**Error Testing:**

- Assert `errors.Is`/`errors.As` against package sentinels or typed codes, then assert the result is cleared or side effects were not performed (`internal/readcore/engine_test.go`, `internal/commandexec/executor_test.go`).
- For security failures, also check that serialized output and error text omit raw paths, credentials, argv, and untrusted selectors (`internal/auditreader/reader_test.go`, `internal/mcpserver/server_test.go`).

**Fuzz and Benchmark Testing:**

- Seed fuzzers with valid, traversal, drive-letter, Unicode, and separator inputs in `internal/readcore/engine_test.go`; skip inputs outside the contract and assert termination, progress, valid UTF-8, and content reconstruction.
- Run large synthetic search benchmarks only with explicit opt-in environment variables from `internal/search/benchmark_harness_test.go`: `LOCAL_PROBE_SEARCH_HARNESS_SIZE`, `LOCAL_PROBE_SEARCH_HARNESS_ALLOW_LARGE=1`, and `LOCAL_PROBE_SEARCH_HARNESS_EVIDENCE=1`.
- The benchmark harness creates fixtures under `b.TempDir()`, reports allocations, separates cold/warm runs, and records coverage; do not commit generated fixture data (`internal/search/benchmark_harness_test.go`).

---

*Testing analysis: 2026-09-19*
