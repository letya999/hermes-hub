---
description: Authorization model — principals, sections, evaluation order, grants, capability policies, profiles, teams, connector lifecycle.
last_verified: 2026-10-30
---

# RBAC, grants and capability policy

All authorization is default-deny and decided host-side; model-visible tools are
a projection of allowed state, so a denied capability is invisible rather than
rejected. Governing records: SPEC-0040/0041/0042 (profiles), ADR-0031 (default
deny), ADR-0032 (isolation), ADR-0049 (broker-only credentials), ADR-0055
(unified connector surface), ADR-0056 (ownership finalizers), ADR-0058 (team
connections).

## Layers

Two documents cooperate per call:

- **Governance** (`internal/toolhub/api/governance.go`) — section policy over
  the tool inventory. Answers "may this principal reach this section at all".
- **Capability policy + profile** (`api/capability_policy.go`) — org ceiling,
  per-principal allows/denies, per-runtime-generation projection. Answers
  "which exact capability tuples may run".

Neither layer alone can widen the other: a grant that exceeds the org ceiling,
or a section the governance document denies, cannot execute.

## Principals and identity

Every call carries an `identity.Envelope`: `principal_id`, `context_id`,
`runtime_id`, `organization`, `policy_version` (internal/identity/identity.go).
Adapters mint envelopes from authenticated transport identity — a Telegram
sender id or Slack user/team id maps to a principal; the model can never set
`principal_id` itself. `runtime_id` plus policy version fence replay across
runtime generations.

Principal roles in practice: **owner** (the space's human, issues confirmations),
**operator** (host control path / hubctl issuing policy and grants under
confirmation), **member** (team record membership, for team connections).

## Sections and effects

`ToolSection` is the inventoried surface. Well-known ids:

| Section | Contents |
|---|---|
| `user_mcp` | `mcp:`/`mcp_servers` declarations from user settings |
| `org_mcp` | organization-provided MCP catalog |
| `toolhub` | dynamic projected tools — attribute-matched |
| `hub:tools` | hubctl tools executor surface |
| `hub:browser` | persistent-profile Playwright server |

Effects: `allow`, `deny`, `cap:read` (read-effect tools of the section stay
reachable, writes drop). A section's `Default` is the floor when nothing
matches — `deny` for `user_mcp` and restrictive surfaces. Trust tier derives
from section axes (owner/official/dynamic/prepared), never a stored label.

## Evaluation order

`Governance.Evaluate` runs one fixed order (governance.go):

1. Any active deny at user, org or global scope wins outright.
2. An active unexpired grant is the only widening path.
3. Scoped rules by precedence user → org → global → default; exact-section
   rules outrank attribute rules; same-scope conflicts sort deny first.
4. The section's inventory default; an unlisted section denies.

## Grants

`Grant` kinds: `catalog-default`, `definition`, `self-install`,
`shared-credential`, `control-operation`. Every grant requires a
`Confirmation` — a digest over the canonical record bytes whose issuer must
equal the grant issuer, so one identity cannot stamp for another.
`IssuedBy == "model"` or `"hermes"` is refused at validation: model and agent
paths cannot mint authority. Grant lifecycle is `pending` → `active` →
`revoked`; expiry derives from `expires_at`, never stored.

`ControlOperations` is the closed list of control-plane operations:
`discover`, `prepare_source`, `status`, `required_credentials`, `confirm`,
`enable`, `rotate`, `disable`, `revoke`, `remove`, `diagnostics`,
`grant_request`, `install_shared` — plus `invoke` for actual tool calls.
`invoke` is admitted under a managed profile as part of the connectors
bundle (`personal` / `personal+team`); under `connectors: ""` the strict
`control_operations` allowlist stands (ADR-0089). `system_guide` and
`tools_list` are always projected and never grant anything — both only
expose the caller's own admitted surface.

## Capability policy (org ceiling)

`CapabilityPolicy` binds an organization: `ceiling` (the hard maximum),
`defaults`, `default_groups`, `denies`, member list, revision and
confirmation. Denies here cap everything below; a profile cannot grant past
the ceiling.

## Capability profile (per runtime generation)

`CapabilityProfile` is published by the host control path for an exact
`principal_id`/`context_id`/`runtime_id`/`environment`/`generation` tuple and
carries `policy_version`/`policy_revision` fencing — restart a runtime and the
old profile cannot be replayed. Contents: `selections` (which tool
definitions project), `allows`/`allow_groups`, `denies`, plus:

- `control_operations` — reviewed allowlist of control-plane ops for the
  managed token; absent means none.
- `self_install` — admits the GitHub-source prepare path; installation still
  requires the single-use human confirmation (SPEC-0041 CP-05).
- `connectors` — projection mode for the nine `connector_*` tools: `""` keeps
  legacy per-operation grants, `none` hides them, `personal` projects them
  over own connections, `personal+team` adds lifecycle on team connections
  where the caller is a member.

## Capability rules

One rule is one complete tuple: `capability_id` + `implementation_digest` +
`action` + `resource` (+ optional `connection_id`, `path_prefix`, limits,
`expires_at`). Action and resource cannot be borrowed across rules; prefixes
constrain logical paths, not filesystem traversal. `CapabilityGroup` is an
embedded snapshot with a revision — widening members requires a new policy
revision, old snapshots never grow. `implementation_digest` pins the reviewed
code so a re-published tool cannot silently change behavior under a live
grant.

## Teams and shared connections

`Team` is an operator-published, confirmed member list (`api/types.go`).
`IsMember` is evaluated per call against the stored record — removing a
member revokes access without touching bindings. `CanWrite` additionally
needs the team's `allow_writes` flag or a named `write_members` entry.
Connections carry an `OwnerRef` (`principal` | `context` | `team`); team
ownership is what `connectors: "personal+team"` consults.

## Connector lifecycle (onboarding record)

Phases: `preparing → review → awaiting-credentials → awaiting-confirm →
awaiting-oauth → confirmed → enabled`, with `disabled`, `revoked`, `removed`,
`cleanup_incomplete`, `failed` as terminal/side states. Highlights:

- `writes_enabled` defaults off; `connector_allow_writes` flips it only
  through a protected confirm link (`pending_writes`).
- Remove is a convergent finalizer ladder (ADR-0056): each step idempotent
  and recorded; a bare retry resumes at the first incomplete step and
  `connector_status` reports where cleanup is stuck. `remove_receipt` is the
  verified-zero summary after Docker/Broker/filesystem label queries all
  report empty — revoke wins over partial cleanup.
- Credential fields land in the broker (`broker_credential_id`,
  `broker_grant_id`, `broker_contract_id`) — never in this store
  (ADR-0049). OAuth connections record `oauth_provider` so a rejected call
  may refresh and retry once.

## Audit

`governance_store.go` logs every admit/allow/deny with the full identity tuple
(`principal_id`, `context_id`, `runtime_id`, `policy_version`, binding and
connection revisions) and drops revoked bindings from projection
(`reason=revoked`). `Governance.History` keeps the newest 8192 changes as
durable evidence. Enforcement points: `governance_store.go` (projection +
dispatch), `capability_store.go` (policy/profile evaluation under live
revision).
