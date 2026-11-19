---
description: Canonical terms - v1 identity and execution vocabulary (principal, guest, layer, agent, space, channel identity, conversation, session, job, run, run token, effect, owner decision, service, zone, profile) plus configuration and catalog r5 terms.
last_verified: 2026-11-20
---
# Glossary

Terms that historically drifted across README, passports and docs. v1 terms
follow ADR-0105..ADR-0109; where a v0 name still exists in code it is noted.

## Identity and execution (v1)

| Term | Meaning |
|---|---|
| principal | Who acts. Kinds: *human* (a user, stable opaque id, ADR-0011), *agent* (owned by one human), *system* (`system:<service>`). Owned by identity-hub. Never a channel id, email or display name. |
| guest | A group-chat participant other than the space owner: opaque id, untrusted input, cannot approve or hold grants (ADR-0106 D3). |
| layer | A policy level: `platform -> org -> user -> agent`. Each layer carries all or part of the six passports and only narrows the one above; spawn-controller computes the effective spec and its digest (ADR-0106 D2). |
| agent | A named agent of one user (a special agent or a subagent) with the narrowed agent layer of that user's passports. Credentials stay the user's. v0 `capability_profile` is the closest existing concept. |
| system principal | `system:<service>` for platform-initiated work (proactive clock). Holds no user credentials; work for a user runs as that user's agent with `origin=unprompted`. |
| space | The per-principal home `spaces/<id>`: six passports, workspace, state. A user and an organization each have one. Every conversation belongs to exactly one space. Docker `dev`/`prod` is an environment choice, not a namespace. |
| channel identity | A verified link from a transport id (Telegram user, Slack team/user) to one principal; lifecycle `pending -> verified -> revoked/gone`, verified only by in-channel proof. Dies at the edge: never a routing or authorization key. |
| conversation | One thread of exchange inside a space (a DM, a forum topic, a Slack thread). In v1 it has a queue in task-engine (`queue` or `interrupt`). |
| context | The execution scope that owns a warm runtime for a principal (v0 `context_id`). |
| session | The upstream Hermes transcript for a conversation (`context + conversation + revision`); stable across jobs, replaced only by an explicit owner action (`/new`). In v1 a runtime runs several sessions in slots. |
| routine / assignment / goal / watch | Proactive objects owned by proactive-engine (ADR-0108). An assignment is a long-running owner intent; its task and progress ledgers are M41. |
| job | One unit in the task-engine ledger with `origin owner\|unprompted`, `class interactive\|scheduled\|subagent` and the lifecycle in [state-machines.md](state-machines.md#job). |
| run | One execution of a job inside a runtime generation. Carries one run token. |
| run token | Ed25519-signed token issued by identity-hub, one per run: `sub`, `agent`, `space`, `run`, `parent_run`, `gen`, `policy_digest`, `origin`, `aud` (list of services the run may call), `exp`. Verifiers check signature, own name in `aud` and expiry, then generation and revocation at task-engine (ADR-0106 D5-D6). |
| policy digest | Digest of the effective spec computed by spawn-controller; consumers read their slice by digest. Replaces the v0 signed policy bundle idea. |
| effect | What a tool does: `read`, `write-local`, `write-external`, `critical`; unknown tools are `critical` (ADR-0109 D1). Action modes over effects are M41. |
| owner decision | Durable record `pending -> approved \| denied \| expired`, `approved -> used \| revoked`, bound to `sha256(tool + canonical args)`, single use; replaces the five v0 confirmation mechanisms (ADR-0109 D5). |
| connector | An integration definition (catalog entry) describing what can be connected and through which backend format (MCP, bounded CLI, provider API, hub executor). |
| connection | An owned, configured instance of a connector with lifecycle state and a credential reference. |
| credential | Secret material for a connection (token, key, OAuth grant). Held only by credential-broker; everything else sees a reference (ADR-0112). |
| memory scope | Whose memory a read or write touches: platform reference, user's vault, agent notes, or the company brain in the org layer and shared space. Owned by memory-hub; layers only narrow access. |
| service / zone / profile | A service is one binary with its own database and contract (`contracts/<name>/v1`); a zone is a network and secret rule (control, agent, edge, secret, egress, space); a profile is an installation set (`minimal`, `family`, `enterprise`) (ADR-0105). |
| `trusted` | Installation flag. `false` refuses process mode and runc without gVisor and makes egress-gateway mandatory. |

## Configuration

| Term | Meaning |
|---|---|
| `feature` | Declarative flag that renders mounts, env and toolset wiring (e.g. `browser`, `hh`). Older docs say "integration". |
| `tool` | One callable function the model can invoke (e.g. `file_read`, `shell`). Enforced per tool by ToolHub admission. |
| `toolset` | A group of tools granted as a unit. Native (upstream Hermes) toolsets are granted whole - per-tool `only`/`access` cannot apply on `via: native`. |
| `capability` | The user-visible ability a feature/toolset provides. Exactly one surface per capability (native toolset, hub MCP entry, owner `mcp_servers`, or ToolHub projection). |
| `backend` / `via:` | Where a tool executes: `native` (in-runtime, no per-call admission), `toolhub` (managed executor), `mcp[:server]` (ToolHub-mediated connector), `mcp-raw:name` (owner-defined MCP, org-scoped), `off` (explicit deny; absent = denied). A minimal stack uses `via: native` only. |
| `grant` | A recorded authorization for a principal to call a tool/connector - a policy object, not a feature wish. |
| `surface` | The one execution path a capability is reachable through - reserved against owner `mcp_servers` to prevent shadowing (SPEC-0040). |
| `ToolHub` / tool-hub | Gateway for projection, admission, dispatch and connector lifecycle; v1 narrows it to catalog, admission, OwnerDecision and routing (ADR-0105, ADR-0109). |
| `agent-tools` | ToolHub transport for reviewed hub operations. `hub-agent-tools` is a definition/group name. |
| `binding` | Association of a connection's tool surface with principal, context, runtime and policy. |
| `workload` | Execution instance when the transport needs one. `shared`, `per-user` and `per-job` are workload classes. |
| `per-team` | Informal team sharing description, not a workload-class enum. |
| `container-mcp` / `remote-mcp` | ToolHub backend transports for managed MCP workloads / remote MCP endpoints. |
| bundled CLI / `bounded-cli` | Bundled describes binary packaging; bounded CLI is a transport with validated arguments and limits. Neither proves provider authentication. |
| `lang` / locale | A `locale.Lang` code (`en`/`ru`/`kk`/`es`). User-facing text follows `Delivery.Lang`; operator tooling resolves `HUB_LANG`/`LANG`/OS culture. |

## Catalog r5 (ADR-0094, docs/tool-design.md)

| Term | Meaning |
|---|---|
| `core` / `extended` | Placement tiers of an admitted toolset: `core` is the visible prefix (at most 20), `extended` is reachable by name through `tool_search`/`tool_call`. A family never splits. |
| `audience` | `model` (projected), `internal` (hub plumbing), `operator` (control surface only). |
| `uncurated` | Raw MCP surfaces (`user_mcp`, `org_mcp`); never in `core`. |
| `requires` | Explicit rights a tool member demands (e.g. `files.read`). v1 adds an `effect` per member (ADR-0109 D1). |
| `executor` | Where a member runs: `handler:`, `runtime:`, `flow:` - distinct from config `via:`. |
| `tool_call` / `tool_search` | The pinned platform pair that discovers and invokes extended tools; admission is identical to a direct call. |

The full agent-facing reference is
[`hermes-hub-system-guide`](../internal/systemguide/SKILL.md). Inventory
answers include all evidenced tool groups for unqualified MCP/connector
questions and label each format; an explicit "only MCP" request narrows the
result.

Related: [architecture](architecture.md), [spaces](spaces.md), [boundaries](boundaries.md).
