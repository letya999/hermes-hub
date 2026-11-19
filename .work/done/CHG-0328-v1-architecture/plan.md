# CHG-0328: v1 architecture decisions and documentation reset (M40 stage 0)

Chain: ADR-0105..ADR-0109 -> SPEC-0100..SPEC-0102 -> this plan -> `state.yaml`.
Evidence and prior art: [research.md](research.md). Issue: #572.
Next stages: CHG-0329 (M40.1), CHG-0330 (M40.2); every other M40 CHG depends
on this one.

## Goal

The v1 decisions exist before any v1 code, supersede (never rewrite) the v0
ADRs they replace, and the living docs describe the v1 target together with
the extraction path from v0 code. No code changes.

## Read before writing

1. `AGENTS.md`, `README.md`, `docs/index.md`, `docs/architecture.md`.
2. `.work/milestone-playbook.md`, `docs/status-hygiene.md`.
3. ADR-0011, ADR-0014, ADR-0036, ADR-0041, ADR-0082, ADR-0090, ADR-0095,
   ADR-0096, ADR-0099 (FSM style), ADR-0100.
4. Milestone M40 description (service extraction map, exit criteria).

## Slices

### S0.1 ADRs (R: ADR set)

Files: `docs/adr/ADR-0105-v1-topology.md`,
`ADR-0106-identity-layers-run-tokens.md`,
`ADR-0107-execution-spawn-task-runtime.md`, `ADR-0108-proactive-engine.md`,
`ADR-0109-action-modes-owner-decisions.md`. Each has numbered decisions,
alternatives, preserved v0 invariants (default-deny, fail-closed passports,
uncertain never retried, credentials never in the model plane), a boundary
crossing table (who may cross, what is refused) and, where state advances,
an explicit transition table.

### S0.2 SPECs

`specs/active/SPEC-0100-v1-contracts.md` (master trace EC1..EC7 ->
requirement and acceptance IDs -> CHG), `SPEC-0101-v1-execution-slos.md`,
`SPEC-0102-v1-proactive-modes-eval.md`.

### S0.3 Living docs

Rewrite `docs/architecture.md` (v1 target, zones, process map, extraction
map, package owner table, current v0 state), `docs/boundaries.md` (zone
crossings), `docs/glossary.md` (principal, layer, agent, space, channel
identity, conversation, session, assignment, task, run, connector,
connection, credential, memory scope).

### S0.4 Supersession

`docs/adr/index.md`: rows for ADR-0105..0109 and status changes for
ADR-0040, ADR-0041, ADR-0046, ADR-0095 (superseded) and ADR-0026, ADR-0075,
ADR-0082, ADR-0090, ADR-0094 (partly superseded). Superseded records are not
edited. `specs/index.md` and `.work/index.md` rows in the same commit.

## Red-team checklist

- Every new boundary states who may cross it and what is refused (tables in
  ADR-0105..0109).
- No ADR weakens a v0 invariant; each lists them.
- No requirement promises a guarantee that has no acceptance ID.

## Acceptance

- `go run ./cmd/devcheck docs` passes.
- Every M40 exit criterion maps to a SPEC requirement and acceptance ID
  (SPEC-0100 trace table).
- Superseded ADRs are marked in the index, not edited.

## Out of scope

- Code changes (CHG-0329 onward).
- CHG folders for M40.3..M40.25: numbers are reserved in SPEC-0100 and
  created when each issue starts.
