# R12 pairing and transport

Updated: 2026-10-24. The native Agent bridge supports direct WSS/Tunnel and a static self-hosted Relay. Start with scoped read-only access; [operations](R12_OPERATIONS.md) are a separate explicit opt-in. [Detailed Chinese reference](R12_PAIRING_AND_TRANSPORT.zh-CN.md).

## Programs and trust boundaries

| Program | Role |
| --- | --- |
| `local-probe-pair` | Local offline identities, fingerprints, issue/install/revoke |
| `local-probe-target` | Paired MCP target; loopback WSS origin or outbound Relay channels |
| `local-probe-agent` | Official SDK stdio MCP bridge launched by a native Agent |
| `local-probe-relay` | Fixed-route forwarding on loopback behind HTTPS |
| `local-probe-relay-edge` | Narrow TLS reverse proxy with a fixed loopback upstream |
| `local-probe-operations` | Local owner inspection/approval/rejection/cancellation |

Outer HTTPS/WSS authenticates transport admission and protects the Internet hop. Inner TLS 1.3 mutual authentication independently verifies paired endpoints and encrypts MCP payloads through the Relay. The Relay sees route/admission metadata, not operation/file plaintext. A Relay token is not target authorization; a relayed connection to a wrong device still fails endpoint verification.

The official OpenAI Secure MCP Tunnel continues through the existing standard MCP integration. It is not a generic WSS byte-stream tunnel for `/v1/peer`. The new native bridge is needed for endpoint-held keys; a generic Web MCP client cannot automatically speak this inner transport.

## Pairing sequence

All placeholders must be replaced locally. Use UTF-8 JSON for public exchange files. Private keys and identity directories never leave their endpoints or enter source control. These steps require the owner to stop any runtime using the store; `-offline` is an explicit declaration, not an automatic process terminator.

1. On the target: `local-probe-pair -action target-init -store <new-absolute-identity-directory> -approve -offline`. Parent directory must exist. Existing identities are not overwritten. Save the public bundle output.
2. Use `local-probe-pair -action fingerprint-bundle` with that JSON on stdin. Independently verify/transmit the fingerprint to the Agent owner; trusting a bundle and fingerprint delivered only by the Relay does not establish identity.
3. On the Agent machine: `local-probe-pair -action agent-init -store <new-absolute-identity-directory> -approve -offline`. Save its public CSR JSON.
4. Use `local-probe-pair -action fingerprint-request` with the CSR on stdin; deliver the request and independently verify its fingerprint with the target owner.
5. Configure an enabled v2 `purpose=agent_remote` connection bound to an appropriate profile. On the target, run `local-probe-pair -action issue -store <target-store> -config <absolute-config> -connection-id <id> -fingerprint <verified-csr-fingerprint> -approve -offline` with the CSR on stdin. Save the public pairing output, including the target bundle and 30-day Agent certificate.
6. On the Agent, run `local-probe-pair -action install -store <agent-store> -fingerprint <independently-verified-target-fingerprint> -approve -offline` with pairing JSON on stdin. Wrong device, mismatched Agent key and tampered grant metadata are rejected.

`-action request -store <agent-store>` can produce a new CSR if needed; its fingerprint changes and must be reverified. `-action show` exports public target material. Do not reissue the same key to recover lost output or change its scope; retain the original public output or create a new Agent identity.

## Direct WSS/Tunnel

Target:

```text
local-probe-target -store <target-store> -config <absolute-config> -audit-dir <private-audit-directory> -listen 127.0.0.1:8791
```

Set `LOCAL_PROBE_TARGET_ACCESS_TOKEN` in the local process environment to a fresh high-entropy secret. Forward the controlled Cloudflare Named Tunnel WSS `/v1/peer` path to this loopback origin. Do not change an existing user's tunnel to test the source checkout.

Native Agent MCP command:

```text
local-probe-agent -store <agent-store> -transport tunnel -endpoint wss://<controlled-host>/v1/peer
```

Use `LOCAL_PROBE_AGENT_ACCESS_TOKEN` for the matching outer admission secret. Optional Cloudflare Access service credentials use both `LOCAL_PROBE_CF_CLIENT_ID` and `LOCAL_PROBE_CF_CLIENT_SECRET`. Keep command and args structured; never put secrets in argv, URLs, logs or commits.

## Self-hosted Relay

See the [generic route configuration](../deploy/relay/relay.example.json). Configure distinct random target/Agent role tokens through the named local environment references. Roles/routes cannot share credentials. The Relay does not possess endpoint private keys or the endpoint signing CA.

Server:

```text
local-probe-relay -config <absolute-relay-config> -listen 127.0.0.1:8792
```

Expose WSS `/v1/target` and `/v1/agent` through the owner's HTTPS reverse proxy; it must support WebSocket Upgrade without logging Authorization/data frames. The Relay itself refuses public listening addresses. The narrow `local-probe-relay-edge` can terminate TLS using explicitly supplied cert/key files; review its flags and deployment example before use.

Target (outbound only):

```text
local-probe-target -store <target-store> -config <absolute-config> -audit-dir <private-audit-directory> -relay-endpoint wss://<relay-host>/v1/target
```

Use `LOCAL_PROBE_RELAY_TARGET_TOKEN`; no inbound target port is created. The target maintains four outbound channels, reconnecting with jittered backoff.

Agent:

```text
local-probe-agent -store <agent-store> -transport relay -endpoint wss://<relay-host>/v1/agent
```

Use the Relay Agent role secret in `LOCAL_PROBE_AGENT_ACCESS_TOKEN`. No arbitrary destination host/port/query routing is accepted. Once paired by the static route, endpoints perform inner mutual TLS.

For a private outer CA, the Agent and Relay-mode target can use `-outer-ca-file <absolute-public-ca-pem>`. Only CA certificates are accepted; hostname verification stays enabled. Without this flag, normal system trust is used. Never use a skip-verification workaround or confuse the outer CA with the endpoint pairing CA.

### Current implementation limits

The first Relay uses authenticated, role-bound static waiting channels instead of the plan's dynamic control connection/one-time tickets. This removes dynamic remote registration from the initial implementation; it does not add arbitrary forwarding or weaken inner authentication.

Per route: up to 16 target channels; global 128. Unmatched wait is bounded to 30 seconds. Binary messages are limited to 64 KiB, heartbeat every 20 seconds, read deadline 60 seconds. Errors close both sides. Admission rate limits use the actual peer address, ignoring forwarded headers; clients behind one proxy currently share that source budget.

The [deployment record](R12_RELAY_DEPLOYMENT.zh-CN.md) describes the authorized Linux test instance and its read-only public validation. It is not a production deployment recipe or current access credential. Its short-lived test leaf certificate expires on 2026-11-07; verify/renew appropriately before later acceptance. Automatic renewal, dynamic credential/route control and full operations telemetry remain incomplete.

## Revocation and private storage

Offline revoke:

```text
local-probe-pair -action revoke -store <target-store> -key-pin <issued-public-key-pin> -approve -offline
```

Revocation persists across restart and the same key cannot be reissued into a different scope. Use a new key for a new pairing. Runtime revocation APIs can cancel active leases, but GUI/trusted management wiring remains incomplete; the offline CLI is not a live management interface.

Windows uses current-user DPAPI and protected current-user/SYSTEM DACLs. Unix uses private directories/files (0700/0600); it does not claim encrypted-at-rest storage against the user/root. Damaged/unavailable records fail closed instead of silently generating identities.

The target runtime loads its leaf key and public records without needing the CA signing key. This separates duties but does not isolate against the same OS user: trusted-host scripts may access that user's identity storage. Identity backup/recovery is not implemented; copying an old store could lose revocations, so re-pair new identities rather than restoring stale authority.

## Validation boundary

Local WSS/mTLS and Relay integration, native EXE forwarding, scoped reads/refusals, revocation, role confusion and transport recovery have tests. Public read-only Relay checks passed with Agent and target on the same Windows machine. New commands/writes through a Linux Agent and the real Relay remain untested. See [status](IMPLEMENTATION_STATUS.md) and [evidence index](README.md).
