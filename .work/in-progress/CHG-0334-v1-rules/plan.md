# CHG-0334: v1 rules, architecture and service README template (M40.1)

Chain: ADR-0105, ADR-0106, ADR-0110, ADR-0111, ADR-0112 (ADR-0107..0109
aligned) -> SPEC-0100 -> this plan -> `state.yaml`.
Issue: #599 (milestone 50, "M40 - v1 Service Split: minimal isolated
services"). Previous: CHG-0333 (M40.0 live fixes on `dev`). Next: CHG-0335
(M40.2 skeleton), which implements the tests this rulebook names "(M40.2)".

## Goal

One source of truth for how v1 is built, written before any code moves:
the rulebook (`docs/engineering.md`), the architecture page, the
state-machine catalog, the service README template with a devcheck rule,
the rewritten decisions and contract list, and honest states for the
earlier M40 records. No service code moves here.

## Read before writing

1. `AGENTS.md`, `docs/index.md`, `docs/status-hygiene.md`,
   `.work/milestone-playbook.md`.
2. The owner-approved working plan (git-ignored, copied into the docs, never
   linked): architecture, M40 split, research notes, immediate actions.
3. `docs/adr/ADR-0105..0109`, `specs/active/SPEC-0100..0102`,
   `.work/in-progress/CHG-0328..0332`, `.work/done/CHG-0331`.
4. Code for the state-machine catalog: `internal/supervisor`,
   `internal/communication`, `internal/toolhub/automation`,
   `internal/routines`, `internal/identityhub`, `internal/toolhub`
   (connector slot), `cmd/hubctl/space_apply*.go`.

## Preconditions and rules

- Work step 1 of the issue (branch hygiene) was done before this CHG: the
  uncommitted CHG-0332 work is commit `6e6b53c6` on
  `chore/park-chg-0332-run-tokens`. The issue text says
  `wip/chg-0332-run-tokens`; the repository hook allows only `main`, `dev`
  or `<type>/<name>` branches, so the `chore/` prefix was used.
- ADR-0105..0109 are not merged to `dev`; the issue allows rewriting them in
  this branch. Accepted ADRs on `dev` are never rewritten.
- `last_verified` moves to 2026-11-20 only on documents re-read against the
  code or the records they describe.
- No GitHub writes, no push, no live stack, no Docker.

## Slices

### S1 Rulebook (`docs/engineering.md`)

Simplicity, service, data, state-machine, security, tool, size, test-base
and process rules. Every rule names its enforcing test or gate. Tests that
M40.2 creates are named and marked "(M40.2)":
`TestStoreRefusesUnscopedQuery`, `TestMigrationLockSerializesConcurrentStart`,
`TestTransitionRejectsForbiddenAndWritesNothing`, `TestSweepMovesExpiredState`,
`TestProcessModeRefusesUntrusted`, `TestMinimalProfileAnswersMessage`,
`TestToolCatalogSnapshotUnchanged`. Later gates name their milestone.

### S2 Architecture (`docs/architecture.md`)

Service table (zone, owns, does not own, extension point, when disabled,
scale), core vs optional, profiles, layers and passports, zones, spawn flow,
message path, launch backends, tool family -> executor, v0 -> v1 extraction
map, and a short pointer section for the v0 system that runs today.

### S3 Decisions

Rewrite ADR-0105 (service model) and ADR-0106 (identity, layers in
passports, one run token with `aud`). New: ADR-0110 storage, ADR-0111 state
machines, ADR-0112 secrets. Align ADR-0107 (execution), ADR-0108
(proactive-engine, M41 parts marked), ADR-0109 (OwnerDecision, effects, run
origin; modes move to M41). Update `docs/adr/index.md` and the supersession
rows that named the old reserved CHG numbers.

### S4 Contract list (SPEC-0100)

v1 contract list and acceptance IDs per M40 issue (M40.0..M40.16); the old
reserved-CHG table is dropped. SPEC-0101/0102 references to reserved CHG
numbers are replaced by milestone issues.

### S5 Service README template and devcheck rule

`services/README.template.md`; `devcheck docs` requires each `## ` heading
of the template in every `services/<svc>/README.md`.
`services/credential-broker` is exempt until M40.13 with a recorded reason;
the exemption is shrink-only.

Tests (red first): `TestServiceReadmeTemplateHeadings` (10 subtests).

### S6 State-machine catalog (`docs/state-machines.md`)

Job, Runtime, PassportRevision, ChannelLink, Routine/Occurrence,
Assignment, Goal, ConnectorBinding, OwnerDecision: current states and
transitions read from code, the v1 target, and the known defects M40.0
(CHG-0333) fixes.

### S7 Record reconciliation

CHG-0328 superseded by this CHG; CHG-0329 and CHG-0330 local scope done
with leftovers re-homed; CHG-0332 deferred to M40.14 with the parking
branch named. `docs/index.md`, `.work/index.md`, glossary and boundaries
aligned.

## Red-team checklist

- A service README that copies a heading into prose or uses `###` does not
  pass the heading rule.
- The broker exemption cannot outlive its reason: once its README follows
  the template, the gate fails until the exemption is removed.
- No rule in the rulebook claims a gate that does not exist: rules whose
  gate is future name the milestone.

## Acceptance

- `go vet -p=2 ./internal/devcheck/` and
  `go test -short -run TestServiceReadmeTemplateHeadings ./internal/devcheck/`.
- `go run ./cmd/devcheck docs` green.
- Owner review of the four documents and the ADR set (issue acceptance).
- `just check` is run by the coordinator once across worktrees.

## Out of scope

Code moves (M40.2+), GitHub writes, changes to the live stack.

## Done checklist

- [x] Devcheck test red, then green.
- [ ] Owner reviewed `docs/engineering.md`, `docs/architecture.md`,
      `docs/state-machines.md`, the template and the ADRs.
- [x] `just check` green (2026-11-20).
- [ ] Merge with the branch; state done; folder moved to `.work/done/`.
