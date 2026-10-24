---
description: Operator-published Team records, team-owned shared connections, member bindings with sender-attributed audit, and fail-closed member lifecycle denial.
last_verified: 2026-10-19
---
# ADR-0058: Team connections

Status: Accepted 2026-10-19. Implements the contract half of GitHub issue
#288 (M13 P7). Builds on [ADR-0055](ADR-0055-unified-connector-surface.md)
(the `connector_*` surface and operation journal),
[ADR-0056](ADR-0056-ownership-finalizers.md) (ownership labels and
finalizers) and the Credential Broker boundary (sole credential authority).

## Context

Until now every connection was owned by exactly one principal; sharing a
connector with a team meant either copying credentials per member or
abusing the legacy shared-credential policy, which records no operator
visible membership and mixes lifecycle authority with use authority.

## Decision

- **D1**: A `Team` is an operator-published record — `team_id`, `members`,
  optional `write_members`, `allow_writes`, monotonic `revision`,
  `issued_by`, `status` — confirmed by the same digest machinery as
  capability policies, persisted in the store snapshot and restored on
  `Load`/`Reload`; stale revisions are rejected so membership cannot be
  downgraded by an old record. Membership is evaluated on every call.
- **D2**: `OwnerRef{team}` names a team as connection owner and never
  matches an identity envelope directly. Members hold their own bindings
  and projections over the team connection while audit identity stays the
  member's.
- **D3**: `shareable: credential` shares only the credential reference —
  each member still gets a per-member workload. `shareable: connection`
  shares one workload and is admitted only for stateless (`state: none`)
  definitions; unknown modes fail closed at validation.
- **D4**: Write-effect tools stay hidden from members unless the team
  record enables them (`allow_writes` or a named `write_members` entry).
  Member lifecycle verbs on a team connector resolve to the stable
  `not_permitted` code — members never mutate team-owned rows, while a
  member removing their own binding drops only that binding and workload;
  the shared connection and credential survive even the last member's
  remove.
- **D5**: Operator rotation bumps the connection/credential revision and
  members follow the current revision on the next call without reconnect;
  in-flight calls finish on their resolved snapshot. Revoke cuts member
  resolution immediately; `DropTeamConnection` is the operator remove —
  it purges every member binding plus the shared rows once.
- **D6**: Call audit records the channel `JobEnvelope` sender
  (`sender_id` = transport identity, not merely the session principal)
  plus `team_id`, `connection_id` and `binding_id`; `connector_list`
  marks shared entries with the `team` badge.

## Consequences

A membership change takes effect on the next authorization call with no
reconnect; member removal purges exactly that member's projection and
workload while the shared credential reference stays. A live acceptance
with two real principals sharing one connection on a deployed stack
remains a deferred gate.
