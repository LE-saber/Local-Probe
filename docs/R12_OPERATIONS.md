# R12 locally approved operations

Updated: 2026-10-24. The implementation is in the independent paired target/CLI backend. The existing desktop and Web advanced-operation ingress are not connected. See [detailed Chinese notes](R12_OPERATIONS_BACKEND.zh-CN.md).

## Execution contract

Windows `trusted_host` runs commands with the current user's actual OS file/network permissions. It **does not provide sandbox isolation**, including confinement of a runtime's own file access to the configured root. Job limits and clean environment handling do not change this contract. The owner explicitly approved this mode; existing `isolated_required` gates remain intact and old configurations are not upgraded.

All conditions are required:

1. A `local-probe.config.v2` explicitly writable profile (`read_only=false`); v1/default profiles remain read-only.
2. An enabled paired connection with `purpose=agent_remote` and `execution_policy=trusted_host`.
3. Developer mode enabled with the connection in `allowed_connections`, and the relevant tools allowed by its profile.
4. A local reviewed runner catalog and private journal outside all authorized roots, supplied explicitly at target startup.
5. The local owner approves the immutable plan's exact ID/digest before execution. Subject, config revision, runner identity/hash and file version are rechecked.

The legacy `network_default=deny` configuration does not establish network denial for `trusted_host`. GUI text must say so. The remote caller cannot approve itself; a remote `confirmed=true` is rejected.

Relevant profile tools are `probe_tools`, `prepare_operation`, `submit_operation`, `operation_status`, `operation_output`, `cancel_operation`, `prepare_file_change` and `commit_file_change`, alongside selected read tools. Authenticated subject/connection scope is bound by the server, not model arguments. See the [Chinese configuration fragment](R12_OPERATIONS_BACKEND.zh-CN.md) and config validation code; fragments are not full deployable configurations.

## Local setup

[The empty catalog](../deploy/operations/catalog.example.json) grants no runner by default. Each entry requires a reviewed physical executable path and actual lowercase SHA-256; optional `script_type` is python/node/powershell/cmd. Explicitly list necessary environment variables. An executable update changes its hash and requires review.

```json
{
  "version": "local-probe.operations.v1",
  "tools": [{
    "id": "python",
    "executable": "C:\\TrustedTools\\python.exe",
    "sha256": "<actual-64-character-lowercase-sha256>",
    "script_type": "python",
    "environment": ["SystemRoot=C:\\Windows"]
  }]
}
```

The host's complete environment/PATH/tokens are not inherited. Build/test dependencies such as PATH/GOROOT must be explicitly configured. TEMP/TMP use private per-job directories. cmd scripts currently reject extra arguments and journal paths with spaces/cmd metacharacters; use approved script content and do not work around this by shell string concatenation.

```text
local-probe-operations -action init -journal <new-absolute-private-directory>
```

Add **both** `-operations-journal <directory>` and `-operations-catalog <absolute-catalog-json>` to the [target startup](R12_PAIRING_AND_TRANSPORT.md). Without them the target stays read-only. These are suffix flags, not a complete command by themselves.

On Windows, private records use current-user DPAPI and a protected current-user/SYSTEM DACL. Shared approval directories are rejected. The engine holds an exclusive storage lock; the local control CLI can publish separate approval records concurrently. The journal cannot overlap authorized roots. Corrupt/unavailable storage closes admission and cancels active work.

## Remote request and local decision

Prepare a plan through the paired MCP tool:

```json
{
  "nonce": "unique-intent-1",
  "kind": "script",
  "tool_id": "python",
  "root_id": "workspace",
  "script": "print('hello')\n",
  "args": [],
  "timeout_seconds": 60
}
```

Kinds: command, script, version, build, test, file_write and file_patch. Preparation validates/persists the plan; it does not run it. `probe_tools` lists reviewed runner identities; an actual version requires an approved `kind=version` process and real output.

Submit the returned ID/digest with `submit_operation`; it waits for local approval. File tools `prepare_file_change`/`commit_file_change` use the same local-approval boundary; “commit” does not bypass it.

The owner uses a local terminal:

```text
local-probe-operations -action list -journal <directory>
local-probe-operations -action inspect -journal <directory> -id <id>
local-probe-operations -action approve -journal <directory> -id <id> -digest <reviewed-digest> -ack-trusted-host
local-probe-operations -action reject -journal <directory> -id <id> -digest <digest>
local-probe-operations -action cancel -journal <directory> -id <id> -digest <digest>
```

Inspect shows subject/connection, workspace, arguments/script and file before/after content. Approval binds that complete plan and expires five minutes after preparation. Changed plan/configuration/identity cannot reuse it. Local cancel confirms a durable request, not completed process cleanup; query until terminal. Remote `cancel_operation` waits for cleanup.

## Jobs, retry and output

Poll `operation_status` at least once per 60 seconds while running to maintain the Agent lease. A GUI local projection must not renew that lease on behalf of the Agent. Default timeout is 60 seconds; maximum 30 minutes. Output is available after completion, paged by stdout/stderr byte offset as base64 raw bytes. No interactive terminal or live output stream is implemented.

Same subject/connection/nonce and same input returns the original job, even after side effects. Changed content with that nonce is rejected. After a network failure query the original ID; do not create a new nonce to replay writes/builds automatically.

Restart marks unstarted jobs interrupted and previously recorded-running jobs unknown; it does not automatically rerun them. Storage publication uses complete encrypted temporary records and no-overwrite atomic publication. The journal is bounded to 128 historical jobs/about 64 MiB and rejects admission when full; automatic archival is not implemented.

Windows limits: 32 processes, 1 GiB aggregate committed memory, cumulative user CPU budget plus wall timeout, 64 KiB each stdout/stderr. Natural parent exit and termination both require actual process-tree cleanup. Unconfirmed cleanup after the bounded wait yields unknown and disables admission. These limits are not network/file isolation or CPU percentage quotas.

## File changes and recovery

UTF-8 text is limited to 128 KiB. New files use exclusive creation; existing files bind content digest and file identity. Exact patches support at most 32 `{old,new}` edits with unique old context, without fuzzy merging, full unified diff or multi-file transactions.

```json
{
  "nonce": "patch-1",
  "kind": "file_patch",
  "root_id": "workspace",
  "path": "main.py",
  "edits": [{"old": "answer = 1", "new": "answer = 2"}]
}
```

Commit rechecks writable scope, rules, directory handles, reparse points, hard links, identity and version. Existing files are updated through a locked handle after a private before image is saved. This is **not atomic file replacement**: crash/disk failure may partially write, returning unknown and retaining manual recovery material. No automatic restoration or replay is performed.

Local `-action before -journal <directory> -id <id> -digest <digest>` returns the before image as base64; it does not restore it. Inspect the current file before manual recovery. Linux target operations are unsupported and do not fall back to an uncontrolled Unix launcher.

## Validation and remaining integration

The [operation evidence](evidence/r12/2026-10-24-operations.md) records actual Windows runtimes, build/test, local CLI approval, WSS/mTLS operations, conflicts/revocation, two-connection concurrency, cleanup and restart behavior. The [source-upload evidence](evidence/r12/2026-10-24-source-upload.md) records the latest repository handoff checks separately.

Remaining: trusted GUI management, advanced Web ingress, production multi-connection host, online rules/selective authorization epochs, retention and real Linux Agent/public Relay operation tests. A global config revision change currently invalidates old approvals; selective hot reload is not complete.
