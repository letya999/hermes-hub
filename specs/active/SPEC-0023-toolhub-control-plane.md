---
status: active
title: Multi-tenant ToolHub control plane — grants, credentials and lifecycle
---
# ToolHub control plane

This specification freezes the M5.2 control plane on top of SPEC-0017,
SPEC-0018, SPEC-0019 and SPEC-0020. It does not switch the default Hermes
runtime to ToolHub, patch Hermes or ToolHive, or claim a live chat
reconnect (#74 / M5.3).

## Separation of records

| Record | Meaning | Who may create or change it |
|---|---|---|
| Catalog `ToolDefinition` | Administrator-preflighted immutable manifest | Operator import/preflight only |
| User `ToolDefinition` | Result of a granted self-install review | ToolHub after `prepare_source`; visibility is the installing principal |
| `Grant` | Catalog-default, definition assignment, or self-install | Operator (`hubctl grant` / `PutGrant`). The model cannot create or widen a grant |
| `Onboarding` | Async install/review/confirm state | Authenticated control operations for that principal only |
| `ToolBinding` | Effective projection for one principal/context/runtime | `confirm`/`enable` for the authenticated principal |
| `CredentialReference` | Opaque locator and key names | Created per binding by default; shared locator only under an explicit admin policy |
| Workload | Running instance | Started on demand; shared across policy members only when the definition is `Shared` and explicitly `sharable` |

Promoting a user MCP to a trusted catalog item publishes a shared
definition. Existing user bindings stay principal-owned and do not become
shared workloads.

## Control MCP

The shipped ToolHub MCP handler exposes these logical operations as tools:

`prepare_source`, `status`, `required_credentials`, `confirm`, `enable`,
`disable`, `revoke`, `remove`.

Principal, context, runtime and policy come from the authenticated
request. Hermes does not pass an arbitrary `user_id`, `owner_id` or
credential locator into ToolHub. Model-supplied `user_id`, `owner_id`,
`principal_id`, `context_id`, `runtime_id`, `policy` / `policy_version`,
`locator`, `backend`, `mcp_endpoint` and grant fields are rejected before
any store write.

`prepare_source` and `enable` are idempotent on a client `request_key`.
Onboarding is persisted so `status` survives process restart.

### Discovery surface

Every projection additionally carries two always-present tools that need no
grant, because they only ever expose the caller's own admitted surface, plus
the dispatch tool governed by the control grant rules below:

- `system_guide` — the static reference document.
- `tools_list` — the live caller-scoped inventory, recomputed per call from the
  same `ListProjectedTools` + governance-filter inputs as `tools/list`. Default
  entries are compact (`name`, `definition`, `effect`); `query` and exact
  `name` return descriptions and input schemas. Output is bounded by `limit`.
  A connector or MCP tool enabled in this or another session is listed on the
  next call without reconnect, respawn or prompt regeneration.
- `invoke` — dispatch by exact projected tool name through the standard
  `AuthorizeProjectedCall` admission path, for names the client's cached
  `tools/list` has not yet refreshed. `invoke` widens nothing: under a managed
  capability profile it rides with the connectors bundle (`personal` /
  `personal+team`); `connectors: ""` keeps the strict `control_operations`
  allowlist, `connectors: none` and the legacy grant path are unchanged.

### Two install modes

1. **Administrator preflight catalog.** The operator imports, builds and
   reviews an MCP. A principal may `prepare_source` / `confirm` / `enable`
   that definition only with a `catalog-default` or definition grant.
2. **User self-install.** Any principal submits a public GitHub URL with an
   exact commit SHA. ToolHub imports, reviews
   permissions/effects/credentials and, after `confirm`, creates only that
   principal's binding. Self-install is allowed by default; a disabled or
   revoked `self-install` grant denies that principal.

The model cannot issue a grant. A principal cannot see or call another
principal's binding.

## Credentials

`required_credentials` returns names, types and hints only. Production
input is a loopback HTTPS/HTTP form bound to the principal and onboarding
id, or MCP URL elicitation that points at that form. Form-mode elicitation
and ordinary chat / Telegram `KEY=value` are not the production MCP
credential path.

Values are stored in `internal/credstore` ciphertext. They are injected
only after `AuthorizeProjected` into the selected workload. They never
appear in Hermes, ToolHub HTTP responses, vMCP, logs, audit content, image
layers or process arguments.

Defaults:

- one credential reference per binding;
- several bindings per user are allowed;
- one external credential shared across users only with an explicit admin
  shared-credential policy;
- member bindings on a shared policy resolve the store owner's single
  connection and workload id;
- a shared workload is allowed only for stateless definitions; stored
  credentials require the reviewed `sharable` flag, member lifecycle actions
  stay scoped to the member's binding, and only the policy's store owner may
  rotate or revoke the shared credential;
- a non-sharable shared definition still rejects stored credentials.

OAuth uses authorization-code with PKCE (S256), state bound to principal /
context / onboarding, allowlisted redirect, TTL and a single-use nonce.
Expired confirmation and replayed nonce fail closed.

Rotate and revoke increment credential and connection revision, revoke the
affected binding, stop starting/running workloads, and deny the next
`tools/call` on an already-open MCP session before backend execution.

## Lifecycle

Repeated `enable` of the same binding is idempotent (same binding id, no
second workload). `disable` and `remove` omit the binding from projection
and delete only that binding's volumes. Lifecycle operations resolve a
conversational `selector` (definition id, source repository URL or basename;
`name`/`source` are aliases) and refuse ambiguous matches with a candidate
list. `remove` is two-phase: a bare call returns a deletion preview and
`remove_nonce`; only the nonce-carrying repeat deletes credential ciphertext
and unreferenced connection/binding/credential rows, keeping a scrubbed
onboarding tombstone for idempotent status replay. `status` also resolves an
onboarding by `operation_id` (the original `request_key`). Effect or budget escalation above
the reviewed definition is denied before write or start. Concurrent
`enable` / `disable` / `revoke` serialize on the registry fence so a later
call observes the new state; an already admitted call finishes under its
recorded revision.

Ninety-five unique credential references plus five uses of one shared
reference keep distinct locators/workloads/state. Sequential uniqueness is
enough; one hundred concurrent MCP+ToolHive stacks are not required.

## Team connections (M13 P7)

A `Team` record (`team_id`, `members`, `write_members`, `allow_writes`,
monotonic `revision`, `issued_by`, `status`) is operator-published and
confirmed like a capability policy; the store keeps it durable across
`Load`/`Reload` and rejects stale revisions so membership cannot be
downgraded by an old record. A connection may be owned by
`OwnerRef{team}` — such an owner never matches an identity envelope;
membership is evaluated against the stored team on every call, so member
removal denies on the next `connector_status`, projection or invoke.

Recipes and definitions declare `shareable: credential` (per-member
workloads over the shared credential reference, valid for stateful
definitions) or `shareable: connection` (one shared workload, admitted
only for `state: none` definitions); unknown modes fail closed at
validation. Members bind their own binding and projection against the team
connection and keep their own audit identity; write-effect tools stay
hidden unless the team record enables writes team-wide or names the
member.

Member lifecycle verbs (`connector_disable`, `connector_reauth`,
`connector_remove`, `connector_allow_writes`) on a team connector return
the stable `not_permitted` code and never mutate team-owned rows; a member
removing their own binding drops only that binding and its workload —
the shared connection and credential survive even the last member's
remove. Operator rotation propagates to members on the next call without
reconnect while in-flight calls finish on their resolved snapshot;
`DropTeamConnection` is the operator remove, purging member bindings plus
the shared rows once. `connector_list` shows team connections with a
`team` badge and call audit carries the channel `JobEnvelope` sender as
`sender_id` plus `team_id`, `connection_id` and `binding_id` attribution.
Operator surfaces: `hubctl capability --kind team` publishes the confirmed
record, `--kind team-share` mints the shared connection under an
operator-supplied credential locator, `--kind team-drop` removes it.

Team connections coexist with the older `shared` workload class rather
than replacing it: `shared` grants every caller of a shared credential
policy the same implicit workload, while a team connection enumerates its
membership in a reviewed record and denies non-members on every call.
New connectors that need multi-principal access should prefer team
connections; the `shared` class stays for existing policies.

## Browser workloads (M13 P8)

Browsers are ordinary connectors: install/enable/disable/reauth/remove,
operation journal, ownership labels and finalizers apply unchanged; the
hub-owned `browser`/`browser_guest` runtime is untouched. Container-MCP
workloads may declare `egress_class: web` — arbitrary outbound HTTPS
through the audited per-workload proxy, deny-first on private, loopback,
link-local, CGNAT, metadata and hub-internal destinations with
per-request DNS checks; connection metadata may carry bounded
`egress_allow`/`egress_deny` refinements, deny always winning. Web
definitions list reviewed extra deny entries in `egress`, not allowed
hosts.

Each connection owns an isolated Chromium whose profile lives on the
`connection-state` volume (removed by the ordinary finalizer). The viewer
is a one-time, short-TTL, owner-bound nonce minted by `connector_status`
and delivered on the owner's channel — `GET /viewer/<id>?t=<nonce>`
serves it once (410 reused/expired, 403 wrong); it is never a published
port and never reaches the model.

The T0 `playwright-mcp` entry is digest-pinned (MCR image, pinned source
commit, `--caps vision,pdf`); default projection is read-only while
`act`, `eval` and `network` are separate toolsets, `act` additionally
gated by `connector_allow_writes`. All projection paths — self-install
list and dispatch included — iterate `ProjectedToolSpecs(binding)`. The
optional `browser-session` Broker contract imports a storage-state file
and origin lists. `agent-browser` stays blocked: no reviewed runnable
image. `ShmSizeMiB` is a bounded execution budget (1024 MiB for
Chromium), validated against a global cap and workload memory, compared
in execution equality, expressed as `--shm-size` on the workload
container only — the ToolHive path forwards it only when its capability
probe advertises the flag, else fails closed or falls back.
