# Local-Probe execution rules

Read `docs/MASTER_PLAN.zh-CN.md` and `docs/IMPLEMENTATION_STATUS.md` first. The plan is the main deliverable; keep implemented behavior separate from future design.

- Priority: effective file access, bounded environment probes, reliability, then optional writes. Do not turn this into an IDE, coding agent or ChatGPT DOM automation project.
- Work on a focused branch/PR. Do not change `LE-saber/deep-project-orchestrator`, merge, deploy, expand permissions, or provision secrets without authorization.
- `internal/readcore` is a transport-free algorithm kernel. A lexical path validator and fake Source are NOT filesystem isolation or account authentication.
- Complete plan P04 security gates before exposing a network MCP service. Use an officially supported, pinned Go toolchain and official MCP SDK; do not hand-roll protocol compatibility.
- Keep authenticated connection scope separate from model arguments. Apply authorization to tools/list, reads, search, metadata, caches and continuations.
- Never expose arbitrary shell, silently enable writes, copy credentials into logs, or weaken policy to get a test passing.
- Do not copy upstream code or prompts without license review. The repository's release license is not yet selected.
- Run `go test ./...`, `go test -race ./...`, `go vet ./...`, and relevant fuzz/OS integration tests. Commit only evidence for checks actually run; cross-compilation is not runtime verification.
- Maintain the plan task IDs in PRs. Report partial completion, failures, assumptions and blocked external tests. A local test does not demonstrate ChatGPT Pro, Windows path security or multi-account behavior.
- Current byte-range core counts returned UTF-8 bytes and logical ReaderAt bytes, not tokens, physical disk traffic or full MCP serialized size. A future adapter must add independent wire and global scheduling limits.
