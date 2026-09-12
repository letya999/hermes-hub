---
description: Threat model for scoped scale-to-zero runtimes and future organization isolation.
last_verified: 2026-09-12
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
| Group/reply audience disclosure or replayed delivery | Group use stays disabled; delivery binds normalized conversation to verified audience and idempotency record | Conversation/audience IDs and delivery state | communication gateway; future group issue |
| OAuth account mis-linking | Callback state binds authenticated principal, context and intended connector; account identity is displayed and explicitly accepted | Link event, provider account identifier and owner | provider OAuth issue |
| Backup or restore crosses owner/context | Backups preserve scope metadata and encryption separation; mismatch or unknown revision blocks restore | Backup identity/revision and restore outcome | #48 and operations |

## Required deny-path suites

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
[SPEC-0010](../specs/active/SPEC-0010-stable-identifiers.md), and issues #11, #12,
#20-#26, #46-#48 and #51.
