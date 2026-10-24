# R12 source-upload verification

Date: 2026-10-24. Branch: `feat/r12-dual-mode`. R12 started from `dd0a6b73981d8006403913e400d698c3b8fee291`; the branch was fast-forwarded to main's documentation-only update `823e7513327d538ec2aadb139998e2fd85232648` before preparing this upload. No main merge, Release, live deployment or permission expansion is part of the source handoff.

## Fresh checks

Windows amd64, Go 1.26.5, normal Windows account. Private process-local temp/cache settings were used, without changing global environment settings. Tests used synthetic data; public-network and actual installed-runtime opt-in tests were not rerun for this upload.

| Command/check | Result |
| --- | --- |
| `go test -count=1 ./...` | PASS, 50.28 seconds |
| `go test -race -count=1 ./...` | PASS, 70.06 seconds |
| `go vet ./...` | PASS, 3.58 seconds |
| `node scripts/Test-LocalProbeDesktopFrontend.mjs` | PASS, 24 VM scenarios |
| `gofmt -l internal cmd` | PASS, no unformatted files |
| Pairing public-decoder fuzz, five-second target/two workers | PASS, 74,554 executions (9.16-second total test including startup/shutdown) |
| `go build -mod=readonly ./cmd/...` | PASS, Windows CLI/GUI entry points |
| Updated documentation relative links | PASS, 14 entry/handoff/status documents |
| `git diff --cached --check` | PASS |
| Staged source export, Gitleaks 8.30.1 redacted scan | PASS, no findings |
| Staged path exclusions | PASS, 96 changed files; no private runtime/build paths |

The complete index was exported to an ignored directory for scanning; credentials and private identities were not exported. The upload adds the Gorilla WebSocket BSD license alongside its pinned module checksum. Raw local logs, private configuration and development ZIPs are intentionally excluded from Git. These checks are local verification, not a claim that GitHub Actions or external acceptance has passed.

## Previous evidence remains separate

Actual installed runtimes, real synthetic Go build/test, local owner CLI approval, WSS/mTLS file operations, cancellation/process limits and conflicts were verified in the [earlier operation round](2026-10-24-operations.md). Public read-only Relay validation is in the [deployment record](../../R12_RELAY_DEPLOYMENT.zh-CN.md). These do not establish Linux Agent → real Relay → Windows execution, GUI operation wiring or production multi-connection lifecycle.

The English README and developer/GUI/operation/pairing guides distinguish implemented behavior from planned interfaces. Chinese design and historical evidence remain available. No credentials, private keys, live configuration, SSH data, job journal, backup, toolchain/cache or developer ZIP is intended for this source commit.
