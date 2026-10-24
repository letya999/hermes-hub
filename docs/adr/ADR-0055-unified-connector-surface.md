---
description: Nine uniformly named connector_* tools replace the legacy control-operation projection under a connectors capability bundle; a durable operation journal owns lifecycle, and agent-facing results carry typed state only — never link material or prose markers.
last_verified: 2026-10-19
---
# ADR-0055: Unified connector surface — nine tools, one operation journal

Status: Accepted 2026-10-19. Implements GitHub issue #284 (M13 P3).
Builds on [ADR-0054](ADR-0054-human-action-surface.md) one-time human links
and the recipe ledger of [ADR-0053](ADR-0053-recipe-source-ledger.md).

## Context

Before this change the model-facing surface was a ladder of control operations
(`prepare_source` → `status` → `required_credentials` → `confirm` → `enable`)
whose sequencing lived in prose `instructions` strings. "Install this
connector" took five calls; phase branches matched stored Russian error
prefixes; and every step relied on the agent passing nonces and ids back
verbatim. The same verbs existed only on the MCP tool surface — hubctl and the
control listener had no shared implementation.

## Decision

- **D1**: A `connectors` capability bundle (`none` | `personal` |
  `personal+team`) on the profile projects exactly nine tools —
  `connector_search`, `connector_install`, `connector_list`,
  `connector_status`, `connector_enable`, `connector_disable`,
  `connector_allow_writes`, `connector_reauth`, `connector_remove` — and
  admits no legacy control operation alongside them. Profiles without the
  bundle keep the grant-per-operation model.
- **D2**: Lifecycle is owned by a durable `Operation` journal persisted in the
  registry snapshot. An operation carries identity, intent, normalized target,
  principal/context/runtime/policy scope, a deterministic request key
  `(principal, intent, target)`, state
  (`queued`/`awaiting_user`/`awaiting_confirm`/`running`/`done`/`failed`), an
  ordered step ladder, a typed `error_code` and onboarding/connection links.
  Repeating a call with the same request key returns the same operation;
  concurrent identical installs converge on one operation and one onboarding.
- **D3**: The selector is deterministic and owner-scoped: exact id (onboarding,
  connection or definition), alias, source URL, repository basename, registry
  name — first stage with a unique match wins. Removed, failed, superseded and
  foreign records are excluded; ambiguity returns `ambiguous_target` plus a
  public candidate list.
- **D4**: Agent-facing results are `{operation_id, state, code?, summary,
  link_sent?, candidates?}` only. No prose instructions, nonces, OAuth state,
  internal URLs or link material ever cross the boundary — the human action is
  delivered on the channel (ADR-0054) and the operation resumes on the next
  call. `statusBody` drops `instructions` entirely; `next_action`,
  `error_code` and a bounded `error` line carry the contract.
- **D5**: No prose-marker control flow. Branches key off `error_code` and phase
  — the "credential probe failed" record carries `probe_failed`, the rotate
  hint reads the code, and a package-level static check fails on any Cyrillic
  literal in a `strings.HasPrefix`/`Contains`/`TrimPrefix` condition.
- **D6**: One implementation serves all surfaces: `ControlPlane.InvokeConnector`
  backs the agent tool dispatch, `hubctl connector <verb>` and the control
  listener (`POST /v1/connectors:install`, `POST /v1/connectors/{id}:disable`,
  `GET /v1/operations/{id}` under the existing control-token /
  principal-bearer auth).
- **D7**: `connector_allow_writes` is the explicit write gate: `on:true` arms a
  protected `/confirm` link and write-effect tools stay unprojected until the
  human click flips `WritesEnabled`; `on:false` applies immediately. `enable`
  and `disable` preserve credentials and bindings; `remove` journals
  confirm → cut_authorization → purge → verify_zero → notify and converges to
  zero resources on repeat.

## Consequences

- Agents see a smaller, uniform surface; the multi-step ladder disappears when
  the bundle is on, so skills and prompts shrink to verb+target calls.
- Crash-safe resume is the journal's job, not the model's: every step is
  persisted before its effect runs, and a repeated call continues exactly
  where the record stopped.
- Legacy control operations remain available internally and to e2e flows, but
  they are unreachable from a bundled profile's projection.
- Prose-driven control flow is gone: the static check keeps new branches off
  stored human text, and the journal's typed codes are the only machine
  channel.
