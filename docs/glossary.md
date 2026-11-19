---
description: Canonical terms - v1 identity and execution vocabulary (principal, layer, agent, space, channel identity, conversation, session, assignment, task, run, connector, connection, credential, memory scope) plus configuration and catalog r5 terms.
last_verified: 2026-11-19
---
# Glossary

Terms that historically drifted across README, passports and docs. v1 terms
follow ADR-0105..ADR-0109; where a v0 name still exists in code it is noted.

## Identity and execution (v1)

| Term | Meaning |
|---|---|
| principal | Who acts. Kinds: *human* (a user, stable opaque id, ADR-0011), *agent profile*, *system principal*. Owned by identity-hub. Never a channel id, email or display name. |
| layer | A policy level: `platform -> org -> user -> agent profile`. Each layer only narrows the one above; effective rights are the intersection (ADR-0106 D2). |
| agent / agent profile | A named sub-entity of exactly one user (for example `research`) with its own mode default and a subset of the user's grants. Credentials stay the user's. v0 `capability_profile` is the closest existing concept. |
| system principal | `system:<service>` for platform-initiated work (proactive clock, watchdog). Holds no user credentials; work for a user runs as that user's agent profile with `initiator` recorded. |
| space | The per-principal home `spaces/<id>`: six passports, workspace, state. A user and an organization each have one. Docker `dev`/`prod` is an environment choice, not a namespace. |
| channel identity | A verified link from a transport id (Telegram user, Slack team/user) to one principal; lifecycle `pending -> verified -> revoked/gone`. Dies at the edge: never a routing or authorization key. |
| conversation | One thread of exchange inside a space (a DM, a forum topic, a Slack thread). Lane key part: `(principal, context, conversation)`. |
| context | The execution scope that owns a warm runtime for a principal (v0 `context_id`). |
| session | The upstream Hermes transcript for a conversation (`context + conversation + revision`); stable across jobs, replaced only by an explicit owner action (`/new`). |
| assignment | A long-running owner intent with acceptance criteria, budget, deadline, task ledger and progress ledger (ADR-0108). Sibling kinds: *watch* (deterministic condition over events) and *routine* (clock-triggered work). |
| task / job | One unit in the task-engine ledger with an execution class (`interactive`, `assignment`, `routine`, `system`) and the lifecycle of ADR-0107 D6. v0 code says "job". |
| run | One execution of a job inside a runtime generation. Carries a signed run token (job, run, generation, policy digest, mode). |
| run token | Ed25519-signed snapshot of a run's identity, verified by every agent-zone service before any backend (ADR-0106 D4). Not a grant. |
| policy bundle | Immutable, signed compilation of the six passports plus org and platform layers; its digest is the policy version. |
| action mode | `read-only`, `plan`, `ask`, `ask-critical`, `full-access`: how much a run may do without asking, under its grants (ADR-0109). |
| owner decision | Durable approval/denial/question record bound to an exact call digest; resumes the parked job. |
| report | The only way proactive work reaches the owner: a record routed by the attention inbox to interrupt, digest or log. |
| connector | An integration definition (catalog entry) describing what can be connected and through which backend format (MCP, bounded CLI, provider API, hub executor). |
| connection | An owned, configured instance of a connector with lifecycle state and a credential reference. |
| credential | Secret material for a connection (token, key, OAuth grant). Held only by credential-broker; everything else sees a reference. |
| memory scope | Whose memory a read or write touches: the user's vault, an agent profile's notes, or an org knowledge view. Owned by memory-hub; never widened by a profile. |
| module / process / zone | A module is code with a contract (`contracts/<name>/v1`); a process hosts modules of one zone; a zone is a trust class (control, agent, edge, secret, egress). |

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
| `ToolHub` / tool-hub | Gateway for projection, admission, dispatch and connector lifecycle; v1 narrows it to admission and routing (ADR-0105). |
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
| `requires` | Explicit rights a tool member demands (e.g. `files.read`). v1 adds an `effect` class per member (ADR-0109 D2). |
| `executor` | Where a member runs: `handler:`, `runtime:`, `flow:` - distinct from config `via:`. |
| `tool_call` / `tool_search` | The pinned platform pair that discovers and invokes extended tools; admission is identical to a direct call. |

The full agent-facing reference is
[`hermes-hub-system-guide`](../internal/systemguide/SKILL.md). Inventory
answers include all evidenced tool groups for unqualified MCP/connector
questions and label each format; an explicit "only MCP" request narrows the
result.

Related: [architecture](architecture.md), [spaces](spaces.md), [boundaries](boundaries.md).
