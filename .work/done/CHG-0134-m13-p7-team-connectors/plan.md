# CHG-0134 — M13 P7: explicit team connectors

Issue: https://github.com/letya999/hermes-hub/issues/288
Branch: feat/m14-toolhub-connector-platform
Depends on: CHG-0128 (recipes), CHG-0130 (surface), CHG-0131 (finalizers), CHG-0133 (broker)

## Scope (contract-testable slices)

### S7.1 Team record
- `api.Team{team_id, members[], revision, issued_by, status}` — confirmed
  record reusing the capability-policy confirmation machinery; no IAM dep.
- `OwnerType` gains `team`; `OwnerRef{Type: TeamOwner}` never matches an
  envelope directly — membership is evaluated per call against the stored
  team.

### S7.2 shareable gate
- `shareable: connection` allowed only for `state: none` recipes —
  `Workload.Stateful` definitions reject it at validation/publication.
- `shareable: credential` allowed for stateful ones and yields one workload
  per member with the shared credential reference.

### S7.3 Member bindings and fail-closed deny
- A member binds against the team connection: own binding, own projection,
  own audit identity; resolve admits members via the team record.
- Non-members deny on `connector_status`, projection and invoke.

### S7.4 Write gating
- Write-effect tools stay hidden for members unless the operator enabled
  writes for the team (`allow_writes`) or the member is named.

### S7.5 Operator lifecycle without own onboarding
- `hubctl connector rotate` on a team connection succeeds for a policy-only
  operator (no member onboarding required); member calls pick the new
  revision via existing Authorize; in-flight calls finish on the old one.
- revoke/remove: member projections drop, channel notice, P4 finalizers
  clean the shared resources once.

### S7.6 Member removal
- Removing a member drops their projection; for `shareable: credential` the
  per-member workload is purged — the shared credential stays.

### S7.7 Sender attribution audit
- Call audit records the human sender from the channel JobEnvelope (not the
  session owner) plus team, connection and binding.

### S7.8 Surface
- `connector_list` shows team connections with a `team` badge.
- Member attempts at rotate/revoke/remove return `not_permitted`.

### S7.9 Docs + closeout
- ADR "Team connections" appended; SPEC-0023 delta; docs/operations.md.

## Out of scope (live gates)
- Two real principals on one stack sharing one team connection (live gate).

## Done checklist
- [x] Tests red then green per slice; `just check` green.
- [x] ADR appended; docs updated.
- [x] Live acceptance deferred honestly.
