---
status: active
title: Tool governance — section inventory, scoped rules and expiring grants
---
# Tool governance

SPEC-0041 froze the default-deny capability policy for managed profiles; this
specification freezes the section-level governance layer on top (issue #139,
CHG-0076). Every tool surface the platform can emit — native toolsets, hub
executor families, hub MCP servers, the dynamic ToolHub section and the two
MCP catalogs — is an inventoried section with deterministic trust axes. A
host-owned document (`governance.json` beside `store.json`, same durability
class, never agent-mounted) decides whether each section renders and serves.

## Document

- `Governance` carries `Inventory`, `Rules`, `Grants` and a bounded `History`
  (newest 8192 `GovChange` records). The whole document validates on load; a
  corrupt or hand-edited file fails closed.
- `ToolSection` axes are facts — `Owner` (hermes|hub|org|user|external),
  `Official`, `Dynamic`, `Prepared`, `Effect` and the `Default` floor. The
  trust tier is derived from the axes (`ToolSection.Trust`), never read from
  a stored label, so a tampered document cannot launder authority.
- `PolicyRule` selects exactly one of `Section` (exact inventoried id) or
  `Match` (attribute map over owner/prepared/dynamic/official/effect/
  definition_id/transport). Scope is `global`, `org` (subject = org id),
  `user` (subject = principal) or `default`. Effects: `allow`, `deny`,
  `cap:read`.
- `ToolGrant` is the only per-principal widening path: host-created, expiring,
  reason-bearing, revocable, optionally attribute-narrowed. Pending grants
  carry no authority.
- `user_mcp` must remain a section-level `deny` default — a default-scope rule
  cannot harden it, only a grant or a user/org-scope rule can unlock it.

## Evaluation order

1. An active `deny` at user, org or global scope wins outright.
2. An active, unexpired, principal-bound grant allows (the only widening path).
3. Scoped rules by precedence: user → org → global → default; exact-section
   rules outrank attribute rules at the same scope; deterministic
   deny-then-cap-then-lexical ordering resolves ties.
4. The inventoried section default is the last resort; an unknown section
   denies.

Expired rules and grants are inert. `cap:read` narrows only to read-effect
tools; where the backend cannot enforce read-only the cap is an error, not a
silent allow.

## Enforcement planes

- **Settings read** (`stack.applyGovernance`): explicit wishes (`tools:`
  entries, `mcp:` declarations) denied by policy fail the read; implicit
  platform emissions (unmanaged default toolsets, the hub executor server)
  are filtered silently; `cap:read` forces `access: ro` rendering where the
  backend honors it. `user_mcp` denies any user-declared `mcp:` map on a
  personal space; `org_mcp` gates the merged organization catalog.
- **ToolHub list/materialization/refresh**: denied projections drop out of
  `tools/list` via `filterProjectedTools` on section plus per-tool effect.
- **ToolHub call time**: `admitGoverned` re-evaluates after ownership
  admission and before credential injection; a mid-session denial or a
  revoked grant cuts the call like a binding revocation. A governance load
  failure denies the call.
- **Hub executors**: render writes `generated/tool-policy.<env>.json`, mounts
  it read-only at `/config/tool-policy.json` (`HUB_TOOL_POLICY`). The
  snapshot carries capability-family ids only — never rules, reasons or
  other principals' data — and `agenttools` refuses denied families and
  enforces read-only caps at dispatch, on both the direct surface and
  `ExecCall`. A set-but-unreadable snapshot fails closed.

## Seeding and migration

- A missing document seeds the restrictive posture (`user_mcp` denied,
  external/unprepared dynamic tools denied at default scope) and preserves —
  as recorded user-scope `migration` allows — only the deny-default sections
  the space already selected by name. `user_mcp` is never preserved that way:
  only the explicit ToolHub migration tool records it, and only when no
  governance document exists yet.
- `HUB_TOOL_GOVERNANCE` pins the document path; otherwise the space's own
  `runtime/toolhub/governance.json` wins, then a sibling space's copy
  (secondary spaces share the infra space's ToolHub policy).

## Grant lifecycle

- `hubctl governance --kind grant` creates a pending grant behind `--confirm`
  (the same `Confirmation` digest gate as capability records); `rule` authors
  scoped rules, `revoke` cuts a grant immediately, `status` inspects. The
  agent has no write path into the document.
- `grant_request` is the in-conversation half: it requires the caller's
  control-operation grant, stamps a fresh single-use request nonce (15-minute
  TTL, rotated on re-request) onto a pending host grant and returns the
  protected `/grants/<id>?nonce=` URL. The model can carry the link; only a
  human at the loopback console activates the grant via the GET-rendered
  form and POST acknowledge — the nonce alone over MCP activates nothing.
- `GrantedBy` values `model`/`hermes` never validate; rules and grants alike
  reject them, so the model cannot mint or grant itself access.

## Audit

Every mutation — seed, migrate, rule, grant, request, acknowledge, revoke —
appends a `GovChange` (actor, kind, id, revision, reason) so the document is
self-auditing; call and list denials surface through the existing ToolHub
deny paths.
