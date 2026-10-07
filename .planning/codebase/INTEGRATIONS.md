---
last_mapped_commit: 9982a8f84a075436b16a2c7aafd61362ebe14146
last_mapped_at: 2026-09-19
---
# External Integrations

**Analysis Date:** 2026-09-19

## APIs & External Services

**Model Context Protocol / ChatGPT:**

- Local-Probe exposes an authenticated read-only MCP Streamable HTTP endpoint at `/mcp`; the official Go SDK handles `initialize`, `tools/list`, `tools/call`, legacy sessions/SSE, and modern stateless protocol negotiation (`cmd/local-probe-mcp/main.go`, `internal/mcpserver/server.go`).
- Registered tools are `server_info`, `ping`, `read_file`, `batch_read`, `list_directory`, `find_files`, `search_text`, `tree_directory`, `get_environment`, `discover_tools`, and `workspace_snapshot`; all are marked read-only/idempotent in `internal/mcpserver/server.go`.
- ChatGPT Web Developer Mode/custom Apps are the external client described in `README.md` and `docs/COMPATIBILITY.md`; the repository does not implement a ChatGPT client or OAuth server.

**OpenAI Secure MCP Tunnel:**

- The optional `openai_runtime` transport uses the externally installed official `tunnel-client.exe`, not a Go SDK, to connect a loopback MCP origin to the OpenAI tunnel control plane (`configs/tunnel-client.example.yaml`, `scripts/Start-TunnelClient.ps1`).
  - SDK/Client: external `tunnel-client` Windows release, with companion `cloudflared.exe`; the expected layout and release checksum are documented in `docs/TUNNEL_SETUP.zh-CN.md` and validated by `scripts/Test-TunnelClientPrerequisites.ps1`.
  - Auth: a Platform Runtime API key with tunnel read/use scope is referenced by `file:` from `.runtime/tunnel-client.yaml`; a separate local hop bearer is referenced by `X-Local-Probe-Token` for both normal and discovery requests (`configs/tunnel-client.example.yaml`, `scripts/Initialize-TunnelClient.ps1`).
  - Endpoint: the profile names `https://api.openai.com` as the control plane and forwards to `http://127.0.0.1:8787/mcp`; the wrapper passes the selected opaque tunnel ID as a non-secret argument (`configs/tunnel-client.example.yaml`, `scripts/Start-TunnelClient.ps1`).

**Cloudflare Named Tunnel and Access:**

- The optional `cloudflare_named` transport runs external `cloudflared` against a remotely managed Named Tunnel and forwards a public hostname to the loopback origin on `127.0.0.1:8788` (`configs/cloudflare-tunnel.example.json`, `scripts/Start-CloudflareTunnel.ps1`, `internal/previewconnect/connect.go`).
  - SDK/Client: external `cloudflared` binary; the Preview controller starts only the fixed binary and fixed `tunnel run --token-file` argument shape (`internal/previewconnect/connect.go`).
  - Auth: Cloudflare Access Managed OAuth runs at the edge; the origin accepts exactly one `Cf-Access-Jwt-Assertion`, verifies RS256 issuer/audience/signature/`exp`/`nbf` against remote JWKS, and maps JWT `sub` to an enabled Local-Probe connection (`internal/cfaccess/verifier.go`, `internal/mcpserver/server.go`).
  - Config: non-secret `public_host`, loopback `origin_url`, loopback `metrics_addr`, protocol (`auto`, `http2`, or `quic`), Access issuer/JWKS/audience, public-host allowlist, and principal-to-connection mapping live in `.runtime/*.json` (`scripts/Initialize-CloudflareTunnel.ps1`, `configs/cloudflare-tunnel.example.json`).
  - Health: the Preview/preflight code polls loopback `/ready` and `/metrics`, requiring HTTP 200 plus a positive `cloudflared_tunnel_ha_connections` metric before reporting tunnel readiness (`internal/previewconnect/connect.go`, `scripts/Test-CloudflareTunnelPrerequisites.ps1`).

**Local tool/process integrations:**

- Configured Git/Node/Codex candidates are discovered by exact path or one non-recursive candidate directory without PATH search or process execution (`internal/environment/discover.go`, `configs/local-probe.example.json`).
- Fixed local version probes support Git, Python, Node, and trusted generic version profiles with bounded output and Windows process/job controls, but the MCP server does not register `run_probe` (`internal/probe/probe.go`, `internal/mcpserver/server.go`).
- Fixed Git status/diff planning and parsing exists, but `internal/gitprobe/gitprobe.go` marks every plan non-executable because repository filters and root binding are not closed; `git_status` and `git_diff` are not MCP tools.

## Data Storage

**Databases:**

- None detected. Configuration and authorization metadata are local JSON values validated by `internal/config/config.go`; no SQL/NoSQL driver or remote database is in `go.mod`.

**File Storage:**

- Authorized content is read from explicitly configured local filesystem roots through Go `os.Root`, with policy-bound regular-file/directory handles (`internal/rootfs/source.go`, `internal/policy/policy.go`).
- Runtime configuration can be persisted to one explicit JSON path with a sibling `.bak` backup and revision/CAS checks by `internal/config/filestore.go`; the helper is explicitly marked not production-ready for cross-process/OS authorization.
- Windows Preview diagnostics/support bundles are local, bounded, schema-checked JSON exports and are not uploaded (`internal/previewui/export.go`, `internal/supportbundle/bundle.go`).

**Caching:**

- No remote cache or cache service is present. `internal/catalog/catalog.go` provides an in-memory metadata candidate catalog, but it is connection/revision scoped, `ProductionReady=false`, not wired to MCP, and disabled by default (`internal/catalog/catalog.go`, `docs/IMPLEMENTATION_STATUS.md`).

## Authentication & Identity

**Auth Provider:**

- Local ingress uses a caller-selected bearer token from an environment variable or protected file; `internal/mcpserver/server.go` compares tokens in constant time and maps each token to a configured connection ID.
- Cloudflare ingress uses Cloudflare Access as the edge identity provider and `internal/cfaccess/verifier.go` as the origin verifier. The origin does not accept the client’s opaque `Authorization` bearer as a fallback and does not implement OAuth/DCR.
- OpenAI tunnel ingress separates the control-plane Runtime API key from the local MCP hop token; Local-Probe stores only credential references in the JSON config (`internal/config/config.go`, `scripts/Initialize-TunnelClient.ps1`).
- Authorization after authentication is local and revision-bound: `internal/policy/policy.go` binds connection/profile/root/tool scope, and `internal/mcpserver/server.go` filters tools and rechecks scope on each request.

## Monitoring & Observability

**Error Tracking:**

- No external error-tracking service is configured. Stable error categories are surfaced by the MCP envelope and Preview connection status (`internal/mcpserver/server.go`, `internal/previewconnect/connect.go`).

**Logs:**

- `internal/audit/sink.go` writes bounded `local-probe.audit.v2` JSONL files locally, with a bounded queue, synchronous security events, rotation, retention, and directory/file permissions; default location is the user config directory (`cmd/local-probe-mcp/main.go`, `internal/audit/types.go`).
- `internal/auditreader/reader.go` and `internal/previewapp/previewapp.go` read an allowlisted, sanitized subset for the Windows Preview; raw paths, tokens, credentials, command lines, and file contents are excluded (`internal/auditreader/reader.go`, `internal/previewui/export.go`).
- Cloudflare tunnel readiness is observed through local metrics polling rather than an external telemetry backend (`internal/previewconnect/connect.go`).

## CI/CD & Deployment

**Hosting:**

- The application is local-first: `cmd/local-probe-mcp` binds loopback and is optionally reached through an OpenAI Secure MCP Tunnel or Cloudflare Named Tunnel. No hosted Local-Probe service or deployment manifest exists (`cmd/local-probe-mcp/main.go`, `README.md`).

**CI Pipeline:**

- GitHub Actions is configured for cross-platform Go test/build/vet/fmt/race/fuzz checks (`.github/workflows/readcore.yml`).
- A Windows release-preflight workflow runs the bounded PowerShell acceptance script and deliberately keeps `release_ready=false` until external/release gates are satisfied (`.github/workflows/release-preflight.yml`, `scripts/acceptance.ps1`).

## Environment Configuration

**Required env vars:**

- No fixed application environment variable is mandatory when protected token files are used; `cmd/local-probe-mcp/main.go` accepts an arbitrary variable name through `-token-env` or a file through `-token-file`.
- `TUNNEL_TRANSPORT_PROTOCOL` is an optional per-process override used by the Cloudflare launcher for `http2`/`quic`; `scripts/Start-CloudflareTunnel.ps1` removes inherited values when config selects `auto`.
- `APPDATA`/the OS user configuration directory determines the default audit location through `os.UserConfigDir` (`cmd/local-probe-mcp/main.go`, `cmd/local-probe-preview/main.go`).
- `LOCAL_PROBE_SEARCH_HARNESS_SIZE`, `LOCAL_PROBE_SEARCH_HARNESS_ALLOW_LARGE`, and `LOCAL_PROBE_SEARCH_HARNESS_EVIDENCE` are test/benchmark controls only (`internal/search/benchmark_harness_test.go`, `docs/R7_INDEX_EVIDENCE.zh-CN.md`).

**Secrets location:**

- OpenAI tunnel runtime key, tunnel ID, and generated local hop token are kept in external `.secrets/` files beside the repository; the runtime YAML in `.runtime/tunnel-client.yaml` contains only `file:` references (`scripts/Initialize-TunnelClient.ps1`, `docs/TUNNEL_SETUP.zh-CN.md`).
- Cloudflare Tunnel token is kept in external `.secrets/cloudflared-tunnel-token.txt`; non-secret tunnel/Access settings are in ignored `.runtime/cloudflare-tunnel.json` and `.runtime/cloudflare-access.json` (`scripts/Initialize-CloudflareTunnel.ps1`, `docs/CLOUDFLARE_TUNNEL_SETUP.zh-CN.md`).
- `.runtime/`, `.secrets/`, `*.key`, `*.pem`, credential directories, and `.env` patterns are excluded by `.gitignore`; the repository contains a deny-list `.env` fixture at `manual-test-targets/r2/denied/.env` whose contents are not read (`.gitignore`).

## Webhooks & Callbacks

**Incoming:**

- The only application HTTP entry point is the MCP endpoint `/mcp`; non-MCP paths return 404, including OAuth metadata candidates (`cmd/local-probe-mcp/main.go`, `internal/mcpserver/server.go`).
- Cloudflare forwards the Access assertion header to the loopback origin, but there are no webhook controllers or generic callback routes (`internal/mcpserver/server.go`, `internal/cfaccess/verifier.go`).

**Outgoing:**

- `coreos/go-oidc` fetches and refreshes the configured Cloudflare Access JWKS URL over HTTP(S) during token verification (`internal/cfaccess/verifier.go`).
- The external OpenAI tunnel client connects to `api.openai.com` and the external `cloudflared` process connects to Cloudflare edge; these network calls are outside the Go process (`configs/tunnel-client.example.yaml`, `scripts/Start-TunnelClient.ps1`, `scripts/Start-CloudflareTunnel.ps1`).
- The Preview polls only local loopback `/ready` and `/metrics` endpoints for tunnel readiness (`internal/previewconnect/connect.go`).

---

*Integration audit: 2026-09-19*
