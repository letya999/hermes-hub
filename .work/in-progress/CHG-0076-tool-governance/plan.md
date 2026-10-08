# CHG-0076: Tool governance — inventory, policy store, grants, enforcement

Issue 139. Prepared 2026-10-07. Single authoritative model of every tool section
a Hermes runtime can emit, a hub-owned durable policy store, scope layering and
per-user grants. Default posture: `native:terminal` and `user_mcp` denied for
new scopes; existing spaces keep their current surface as explicit recorded
allow rules (migration notes), never a silent switch.

## Model

- `Governance` JSON document at `<space>/runtime/toolhub/governance.json` —
  same durability class and locking as the ToolHub registry, host-owned, never
  mounted into an agent runtime. Holds `inventory`, `rules`, `grants`,
  `history` (bounded revision log).
- `ToolSection`: id + axes (owner, official, dynamic, prepared, effect) +
  section-level default (`allow|deny`). Trust tier derived, never assigned.
- `PolicyRule{scope, subject, section|match, effect, reason, granted_by,
  grant_id, expires_at, status}` — scopes global/org/user/default.
- `ToolGrant` — principal-bound, expiring, reasoned, revocable; `pending` →
  `active` only via the in-conversation request + nonce acknowledge pair
  (`grant_request`/`grant_acknowledge` control ops + audit).

## Evaluation (fixed, total)

1. Any active unexpired matching `deny` at scope user/org/global → deny.
2. Active unexpired grant for (principal, section/match) → allow or cap:read.
3. user > org > global > default scope precedence among matching rules.
4. Inventory `Default`; unknown section → deny (bounded warning).

`cap:read` allows only read-effect tools; unsplittable sections treat it as
deny. `user_mcp` denies at section default so a grant can unlock it;
`external` ToolHub installs deny at default scope (grant-overridable);
operator `deny` rules are absolute ceilings.

## Enforcement planes

1. `stack.finishRead` resolves the governance file (env `HUB_TOOL_GOVERNANCE`,
   space dir, sibling `spaces/*/` scan). Absent file → seed + migration allows
   for currently-emitted deny-default sections (`native:terminal`, `user_mcp`).
   Corrupt file → fail closed. `mcp:` entries error when `user_mcp` denied;
   `via:` entries for denied sections strip to `off` before render.
2. `stack.Config`/`disabledToolsets`/`rawMCPServers` filter emitted toolsets,
   hub servers (`hub`, `browser`, `browser_guest`) and raw MCP by decisions.
3. ToolHub gateway: `ListProjectedTools` and `AuthorizeProjectedCall` evaluate
   `toolhub` with attributes {definition_id, owner, prepared, transport,
   effect} at list and call time; mid-session deny cuts in-flight access.
4. Hub-owned stdio: render writes `generated/tool-policy.<env>.json`, mounts
   it read-only at `/config/tool-policy.json`, `HUB_TOOL_POLICY` env feeds
   `tools-exec`/`tools-daemon` per-family deny + cap:read enforcement.

## user_mcp unlock

Host `hubctl governance --kind grant` (confirmed record) → `pending` grant
with expiry → agent calls `grant_request` → single-use nonce → protected
loopback `/grants/<id>?nonce=` form (GET renders, POST acknowledges)
activates + audit. Expiry restores deny without restart ambiguity. The
acknowledge step is an HTTP form, not an MCP op, so the model can carry the
link but only a human completes it.

## Admin surface

`hubctl governance --kind status|rule|grant|revoke` — host-only writes,
`--confirm` digest stamping like `hubctl grant`/`capability`.

## Tests

Deny-wins, scope precedence, grant expiry/revoke, unknown section deny,
corrupt store fail-closed, cross-user isolation, `mcp:` Read rejection +
unlock path, render filtering (terminal absent, browser cap), call-time deny,
snapshot enforcement, CLI confirm gate. `just check` ≥85%.
