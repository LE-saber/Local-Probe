---
last_mapped_commit: 9982a8f84a075436b16a2c7aafd61362ebe14146
last_mapped_at: 2026-09-19
---
# Technology Stack

**Analysis Date:** 2026-09-19

## Languages

**Primary:**

- Go 1.25 language/runtime baseline - all application, library, test, and command code under `internal/` and `cmd/`; the module declares `go 1.25.0` in `go.mod`.

**Secondary:**

- PowerShell - Windows initialization, preflight, build, launch, packaging, and acceptance automation in `scripts/*.ps1`.
- JSON and YAML - strict runtime/configuration representations in `configs/local-probe.example.json`, `configs/cloudflare-tunnel.example.json`, and `configs/tunnel-client.example.yaml`; JSON is also the MCP and audit wire format.
- Windows resource data - native Preview icon/resource inputs in `cmd/local-probe-preview/preview_icon.rc`, `cmd/local-probe-preview/preview_icon_windows_amd64.syso`, and `internal/previewui/assets/`.

## Runtime

**Environment:**

- Go 1.25.0 is the module floor; the checked-in CI workflows use Go 1.26.5 and the current Windows development environment reports Go 1.26.0 (`go.mod`, `.github/workflows/readcore.yml`, `.github/workflows/release-preflight.yml`).
- The core is a local process. `cmd/local-probe-mcp` serves an HTTP MCP origin on an explicit loopback address, while `cmd/local-probe-preview` is a Windows desktop/tray process.

**Package Manager:**

- Go Modules (`go.mod`).
- Lockfile: `go.sum` present with checksums for direct and transitive modules.
- No npm, Cargo, Python, Java, or container dependency manifests are present in the repository root.

## Frameworks

**Core:**

- Go standard library - HTTP server, filesystem, JSON, cryptography, concurrency, process control, and platform build-tag implementations (`cmd/local-probe-mcp/main.go`, `internal/readcore/`, `internal/rootfs/`, `internal/config/`).
- Official Model Context Protocol Go SDK v1.7.0 - Streamable HTTP server, bearer middleware, MCP tool registration, protocol negotiation, and typed tool results (`internal/mcpserver/server.go`, `go.mod`).
- `github.com/coreos/go-oidc/v3/oidc` v3.21.0 - Cloudflare Access JWT verification against remote JWKS (`internal/cfaccess/verifier.go`, `go.mod`).

**Testing:**

- Go's `testing` package, `httptest`, race detector, coverage, and native fuzzing - package tests and fuzz targets are co-located under `internal/**` and `cmd/**` (`internal/readcore/engine_test.go`, `internal/readcore/engine_test.go`, `.github/workflows/readcore.yml`).
- GitHub Actions runs the Go matrix on Ubuntu, Windows, and macOS, plus Linux race/fuzz checks (`.github/workflows/readcore.yml`).

**Build/Dev:**

- `go build`, `go run`, `go test`, `go vet`, and `gofmt` are the supported local commands (`README.md`, `.github/workflows/readcore.yml`).
- PowerShell 5.1/7 automation builds the Windows Preview and MCP binaries and runs bounded preflight/acceptance checks (`scripts/Build-LocalProbePreview.ps1`, `scripts/acceptance.ps1`).
- Windows-native UI and process/file primitives use `golang.org/x/sys/windows` v0.41.0 plus `syscall`/Win32 APIs (`internal/previewui/window_windows.go`, `internal/probe/process_windows.go`, `internal/rootfs/source.go`, `go.mod`).

## Key Dependencies

**Critical:**

- `github.com/modelcontextprotocol/go-sdk` v1.7.0 - authoritative MCP protocol and Streamable HTTP implementation; keep tool registration and transport behavior in `internal/mcpserver/server.go`.
- `github.com/coreos/go-oidc/v3` v3.21.0 - RS256 OIDC/JWKS validation for the Cloudflare Access ingress (`internal/cfaccess/verifier.go`).
- `golang.org/x/sys` v0.41.0 - Windows handles/jobs/Win32 UI and Unix identity seams (`internal/previewui/`, `internal/probe/`, `internal/rootfs/`, `internal/runtimeowner/`, `go.mod`).
- `github.com/google/jsonschema-go` v0.4.3 and the SDK JSON packages - schema and JSON encoding support pulled into MCP result/tool handling (`go.mod`, `internal/mcpserver/server.go`).

**Infrastructure:**

- `github.com/go-jose/go-jose/v4` v4.1.4 and `github.com/golang-jwt/jwt/v5` v5.3.1 - transitive JWT/JWS support used by the OIDC/MCP dependency graph (`go.sum`).
- `golang.org/x/oauth2` v0.36.0 - transitive OIDC/MCP authentication support (`go.sum`).
- `golang.org/x/sync` v0.20.0 and `golang.org/x/time` v0.15.0 - transitive synchronization/rate/time support (`go.sum`).
- `github.com/segmentio/encoding` v0.5.4, `github.com/segmentio/asm` v1.1.3, and `github.com/yosida95/uritemplate/v3` v3.0.2 - transitive SDK encoding/template support (`go.sum`).

## Configuration

**Environment:**

- The primary configuration is strict, bounded JSON schema `local-probe.config.v1`, parsed and validated by `internal/config/config.go`; it contains explicit roots, read-only profiles, connections, credential references, environment-tool candidates, and optional command-profile metadata.
- `configs/local-probe.example.json` is the checked-in redacted shape for roots, deny/ignore patterns, read-only tools, logical connection IDs, credential references, and non-executing tool candidates.
- OpenAI tunnel-client settings use the checked-in shape in `configs/tunnel-client.example.yaml`; the runtime copy is `.runtime/tunnel-client.yaml` and references protected files rather than embedding key material (`scripts/Initialize-TunnelClient.ps1`, `.gitignore`).
- Cloudflare settings use `.runtime/cloudflare-tunnel.json` and `.runtime/cloudflare-access.json`, initialized by `scripts/Initialize-CloudflareTunnel.ps1`; only the non-secret examples are tracked (`configs/cloudflare-tunnel.example.json`, `internal/cfaccess/verifier.go`).
- Local MCP startup requires an explicit config path and accepts a local token from exactly one caller-selected environment variable or protected file (`cmd/local-probe-mcp/main.go`, `internal/mcpserver/server.go`).
- Audit defaults to the user configuration directory (`%APPDATA%/Local-Probe/audit` on Windows) and can be overridden by `-audit-dir` (`cmd/local-probe-mcp/main.go`, `cmd/local-probe-preview/main.go`).
- `.runtime/`, `.secrets/`, local JSON, credential, key, and log paths are ignored or externalized by `.gitignore`; a deny-list fixture named `manual-test-targets/r2/denied/.env` exists, but its contents are not part of runtime configuration (`.gitignore`, `manual-test-targets/r2/README.md`).

**Build:**

- `go.mod`/`go.sum` define dependency resolution; there is no Dockerfile, Makefile, package-lock, or deployment manifest.
- `.github/workflows/readcore.yml` defines cross-platform test/build/vet/fmt/race/fuzz checks; `.github/workflows/release-preflight.yml` defines the Windows acceptance gate.
- `scripts/Build-LocalProbePreview.ps1` emits `bin/local-probe-preview.exe` and `bin/local-probe-mcp.exe`; `bin/` is ignored and is not a source artifact.

## Platform Requirements

**Development:**

- Go 1.25 or newer is required for the `os.Root`-based filesystem boundary in `internal/rootfs/source.go`; Go 1.26.5 is pinned by CI workflows.
- Windows is required for the native Preview/tray, Win32 folder picker, Windows process/job guards, and Cloudflare/OpenAI PowerShell launch flows (`internal/previewui/window_windows.go`, `internal/previewconnect/process_windows.go`, `scripts/*.ps1`).
- The core packages have Unix build-tag implementations and CI coverage for Ubuntu/macOS, but Windows-specific Preview and process features are unavailable off Windows (`internal/environment/path_unix.go`, `internal/probe/execution_unsupported.go`, `.github/workflows/readcore.yml`).

**Production:**

- The supported deployment shape is a local Go executable with a loopback MCP origin; external OpenAI or Cloudflare tunnel processes are optional ingress layers (`cmd/local-probe-mcp/main.go`, `configs/tunnel-client.example.yaml`, `configs/cloudflare-tunnel.example.json`).
- No hosted service, database server, container runtime, installer, or cloud deployment target is defined in the repository (`README.md`, `.github/workflows/release-preflight.yml`).
- The Windows Preview is a native local observer/connector and does not provide a general web management server (`cmd/local-probe-preview/main.go`, `internal/previewui/window_windows.go`).

---

*Stack analysis: 2026-09-19*
