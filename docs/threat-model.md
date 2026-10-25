---
description: Threat model for scoped scale-to-zero runtimes and future organization isolation.
last_verified: 2026-10-24
---
# Threat model

This model covers the local personal deployment and the contracts that must remain
valid when organization contexts are added. It does not claim hostile multi-tenant
isolation or replace deployment-specific review.

## Trust boundaries and data flow

```text
verified channel
      |
      v
communication hub -- authenticated job --> Hermes runtime
                                              |
                                      authenticated runtime
                                              |
                                              v
                                     Go ToolHub gateway
                                      | policy + owner check
                                      v
                                    internal vMCP
                                              |
                                              v
                                      ToolHive Runtime
                                              |
                                              v
                                    connector / provider
```

Only the communication hub derives a principal from a verified channel identity.
Only the Go ToolHub gateway authorizes external tool execution. Hermes output, tool
arguments, connector responses, web content, vMCP membership and workload metadata are
untrusted authorization inputs. Communication access to a provider never grants access
to that provider's data.

The current owner trusts their personal Hermes runtime with terminal-visible personal
credentials. Managed-only credentials have a stronger boundary: they are resolved by
Go after authorization and injected only into the selected connector. A future
organization policy intersects with, and can only narrow, personal policy. It never
changes the meaning or ownership of stable identifiers.

In the scale-to-zero target, the communication hub sends the authenticated job to a
separate host-side Go supervisor, which alone controls Docker and resolves exact
context mounts. The communication and Hermes containers never receive the Docker
socket or a parent directory containing other users' homes. A runtime generation is
leased only for current work and is not an authorization source.

## Assets and invariants

- Hermes homes, sessions, memory, workspace and browser/session state belong to one
  `principal_id` and `context_id`.
- A `runtime_id` is bound server-side to that owner and context.
- A provider secret belongs to one current `connection_id`; `credential_ref` is never
  a tool argument or manifest value.
- `tool_binding_id` and `policy_version` must be current at execution time. Visibility
  in `tools/list` is not permission to call.
- Organization material is read-only unless a separately authorized action permits a
  bounded mutation.

## Threats, controls and test ownership

| Threat / abuse case | Enforcement and fail-closed behavior | Audit evidence | Regression owner |
|---|---|---|---|
| Forged principal, context, runtime, connection or credential fields in a prompt/tool call | Communication hub supplies authenticated identity; Go ignores model-supplied authority fields and rejects mismatches before execution | Principal/context/runtime, binding and denial reason; no secrets | #21, #47 |
| Confused deputy or cross-owner credential selection | Go resolves connection and credential from the authenticated runtime plus current binding; missing, ambiguous or wrong-owner records deny | Connection/binding identifiers and outcome | #47, #48, #51 |
| Stale binding, policy, cached tool list or replay after disable/remove | Go rereads binding and policy on every call; stale calls deny before vMCP/backend; projection change forces MCP reconnect | Policy/projection revisions and reconnect outcome | #21, #24, #26 |
| Prompt injection or malicious connector/web response | Content never grants provider mutations, identity, mounts or policy; provider writes still require concrete user authority | Requested capability and authorization source | #20, #24, provider issue |
| Managed secret exposed through Hermes, vMCP, args, logs, transcript, files, backup or process inspection | Secret values bypass Hermes/vMCP, are injected only into the owning workload, redacted, encrypted at rest and cleaned on stop/failure | Secret reference and lifecycle event only | #48, #51 |
| Shared workload leaks user state or static credentials | Shared is limited to stateless per-request authorization; state, env/file credentials and browser sessions require per-user/per-job workloads | Workload class, owner and runtime | #22, #25 |
| Cross-user filesystem mount, symlink or stateful-volume reuse | Go validates resolved mounts inside the selected scope; ambiguous paths and owner mismatch deny; explicit deletion only | Resolved scope/workload IDs, never file content | #22, #25 |
| SSRF or unauthorized egress | Manifest allowlist plus ToolHive egress policy deny destinations by default; management networks are excluded | Destination class and deny/allow outcome | #20, #25 |
| Mutable or compromised workload/sidecar supply chain | ToolHive CLI and every workload, ingress, egress and DNS image are pinned; unknown digest blocks rollout | Version/digest and provenance | #20, #25 |
| CPU, memory, PID or restart-loop exhaustion | Go/Docker controller applies finite budgets, timeouts, bounded retries and circuit behavior | Budget, usage, termination/recovery reason | #25, #46 |
| Forged scope starts a container with another user's mounts or environment | Host supervisor derives names, mounts and environment from the validated binding; job fields cannot supply paths; mismatch denies before Docker | Principal/context/runtime, generation and denial reason | #14, #18 |
| Idle reaper races a new job, approval or active stream | Per-context lifecycle lock and leases make start/stop atomic; unknown state preserves compute and queues work | Lease, activity and lifecycle transition without content | #14-#18 |
| Reconciliation turns all registered users into resident runtimes | Only runnable jobs, due routines, approvals or explicit pins create desired runtime state | Desired-state reason and active-runtime count | #18, #46 |
| Duplicate or spoofed scheduled occurrence targets another audience | Hub owns schedule identity and audience; occurrence key is durable and execution rechecks current owner/context/policy | Schedule/occurrence/job/delivery IDs | #28, #33 |
| Browser profile, Telegram session or personal terminal bypass | Treat profiles/sessions as credentials; managed mode stays outside Hermes; personal-terminal exposure is explicit, reversible and owner-scoped | Exposure mode and lifecycle, no values | #51 and connector issue |
| Group/reply audience disclosure or replayed delivery | Telegram groups require an explicit dedicated group home, member allowlist and mention. Other group adapters remain denied. Delivery, approval and replay bind the verified actor to the original audience; personal homes never become a group implicitly | Group scope, conversation/audience IDs and delivery state; bounded private transcript | #207; communication group scope, audience, revocation and queued-approval tests; supervisor group binding/policy tests |
| Model forges owner, locator, backend or policy on the control MCP | Control operations derive identity from the authenticated request and reject authority arguments before any store write | Principal and denial reason; no locators as model input | #81, #101 |
| Control-plane socket reaches foreign containers | The ToolHub/workload-controller socket is mounted only when the space renders workload features or diagnostics; every inspect/exec/logs/lifecycle call is scope-checked against the own compose project label, the `hermes-hub.scope` spawn label or the managed agent network, and foreign objects deny before the socket sees them | `docker-scope: denied` lines in service logs | #221 |
| Dev stack attaches prod credential material | The broker state volume is named per environment (`hermes-credential-broker-<env>`); crossing environments needs an explicit `HUB_BROKER_STATE_VOLUME` override | Rendered external volume name per env | #225 |
| Operator needs to block a principal from importing GitHub MCP | `prepare_source` allows self-install by default; a disabled or revoked `self-install` grant denies that principal; catalog definitions still require catalog-default or definition assignment | Grant kind and outcome | #81, #101 |
| Alice lists or calls Bob's binding | Projection and control status are exact-owner; foreign onboarding/binding ids are not found | Principal/binding ids | #101, #86 |
| Secret entered as Telegram `KEY=value` or chat becomes an MCP credential | Production MCP credentials use the loopback form or URL elicitation into ciphertext; control tools reject credential-named arguments | Key names only | #102 |
| Expired confirmation or replayed nonce | Single-use nonce with TTL; replay and expiry fail closed | Onboarding id and deny reason | #102 |
| Replayed, forged or cross-operation human-action link | `?t=` tokens resolve only the stored nonce of that onboarding and operation; replay/expiry answer 410 and token-less POSTs deny | Onboarding id, link path and deny/410 outcome | #283 |
| Public listener exposes control or gateway routes | The opt-in public surface registers only the human-action paths; all other routes 404, and the gateway accepts https `form_url` only for the exact configured origin host | Route registration and 404 outcome | #283 |
| Action link lands on the wrong channel or reaches the model | The notice routes to the initiating job's channel; link tokens, nonces, OAuth state and device codes are swept from every control-op result | Channel id and delivery outcome; no tokens in results | #283 |
| Credential persisted before the server accepts it | The read-only `tools/list` probe precedes any ciphertext, connection or Broker write; a failed probe re-renders the form with no residue | Probe result and persistence outcome | #283 |
| Device-flow session or pasted redirect stolen mid-flow | Device codes live server-side keyed to the onboarding; pasted redirects re-validate `state` and the registered origin through the same PKCE callback | Onboarding id and flow outcome | #283 |
| Rotating refresh token double-spent by concurrent callers | One durable lease per `(connection, provider)` under a separate lease lock; a second in-flight caller joins or fails; revoked/missing/degraded connections never acquire | Lease holder, expiry and acquire outcome | #287 |
| Revoke raced by an in-flight OAuth refresh | Refresh lease admission reads a lock-free status mirror resynced on mutation, `Load` and `Reload`; holder-matched release cannot drop a successor's lease | Connection status and lease outcome | #287 |
| Credential-bearing recipe drift keeps serving stale tokens | Connections pin `hermes-hub.credential-recipe`; the reconciler compares the pin to the resolved digest and `MarkReauth` revokes the reference, stops workloads and denies calls until reauthorization | Recipe digests, connection status and notify outcome | #287 |
| `proxy` delivery leaks the secret into env, argv, inspect or logs | The workload environment carries only the `hub-managed` stub; the egress-proxy plan holds the credential reference and allowlisted target host; proxy targets validate against the reviewed egress list | Proxy plan fields and stub-only env | #287 |
| Migration strands credentials on the legacy store or loses them on rollback | `hubctl credentials migrate` re-anchors contract-bound references through broker requests (secrets enter only via the owner link); `--rollback` restores the preserved legacy locator and needs no broker | Migration entries (`request_pending`/`anchored`/`rolled_back`) | #287 |
| Credential form accepts hostile values (newlines, oversize, wrong format) | Per-field `format`/`max_length` plus control-byte rejection at the submit boundary; `json`/`file` values must parse | Field name and validation outcome, no values | #287 |
| Effect or budget escalation at confirm/enable | Requested effects and execution limits cannot exceed the reviewed definition | Definition bounds and deny reason | #86 |
| Rotate/revoke leaves an open session authorized | Go rereads current revision under the registry fence and stops affected workloads; the next `tools/call` is denied before backend | Credential/binding revision | #86, #102 |
| OAuth account mis-linking | Callback state binds authenticated principal, context and intended connector; account identity is displayed and explicitly accepted | Link event, provider account identifier and owner | provider OAuth issue |
| Backup or restore crosses owner/context | Backups preserve scope metadata and encryption separation; mismatch or unknown revision blocks restore | Backup identity/revision and restore outcome | #48 and operations |

## Required deny-path suites

M16 source-level disclosure controls also include scoped knowledge snapshots
with explicit stale/unavailable states and immediate unbind. Knowledge
revisions (including raw-file content/deletion) stop new access at binding
resolution; previously delivered replies stay in the session history Hermes
already owns. Regression owners: #249/#196, agenttools knowledge lifecycle and
context-alias tests. This does not erase previously delivered messages and
does not copy documents into personal memory automatically.

Native memory and session history use distinct owner homes. Export reads are
confined with `os.Root`; shared-spool exports require matching owner/context.
Agent replies never enter shared content-capture logs. Regression owner #184:
contextlife memory/export isolation, stack native-state grants and Honcho
namespace/disable, communication memory-log privacy, diagnostics owner/trace
deny paths. Real upstream memory/provider isolation remains a deployment gate.

Optional bootstrap uses the same managed runtime/egress authority as ordinary
jobs, a finite budget, a bounded workspace diff and private redacted attempt
receipts. Failure blocks jobs until an explicit new revision. Regression owner
#213: runtime bootstrap sandbox, timeout, failure, concurrency and receipt tests.
Idle protection remains the typed lease boundary (#214), with unknown work
preserved; no process-activity claim can override a held lease or crash budget.

Implementation issues own executable tests. At minimum they must cover forged identity
arguments, cross-user connections, stale policy/binding, removal during an open MCP
session, secret redaction and cleanup, cross-scope mounts, denied egress, resource
exhaustion, restart recovery, duplicate/replayed delivery and wrong OAuth account link.
Mocks may exercise failure handling but cannot prove ToolHive, Docker, Hermes or provider
isolation.

## Deferred boundaries

Public ingress, OIDC, organization RBAC, hostile multi-tenant scheduling, group-chat
disclosure and Vault are deferred. Until implemented, public/group access remains off,
the deployment remains private, and the Go checks above remain authoritative. OIDC may
replace bearer authentication later; it cannot replace per-call binding and ownership
checks.

Related: [ADR-0013](adr/ADR-0013-toolhub-security-boundary.md),
[ADR-0016](adr/ADR-0016-encrypted-credentials-and-oauth.md),
[ADR-0031](adr/ADR-0031-default-deny-capability-policy.md),
[ADR-0032](adr/ADR-0032-capability-execution-isolation.md),
[ADR-0049](adr/ADR-0049-broker-only-credential-intake.md),
[SPEC-0010](../specs/active/SPEC-0010-stable-identifiers.md),
[SPEC-0020](../specs/active/SPEC-0020-credentials-and-connectors.md),
and issues #11, #12, #20-#28, #46-#51.
