# CHG-0212: Memory migration, eval fixture and hygiene (M20 stage 9)

Chain: ADR-0086 D10 -> SPEC-0085 R9 -> this plan -> `state.yaml`.
Previous stage: CHG-0211. Next stage: CHG-0213.

## Goal

Existing deployments aren't stranded: a dry-run-first Honcho/native
export lands as cards; a deterministic eval fixture proves search and
rollup behavior without a live model; `hubctl memory vacuum` keeps old
vaults lean — the iva `vault-cleanup` analog.

## Read before coding

1. `research.md` (eval fixture + cleanup); `internal/migration/*`
   patterns; `internal/vault/*` from earlier stages; `agent.yaml`
   `honcho`/`honcho.json` format (`internal/stack/render_env.go`).

## Slices

### S9.1 Honcho/native export → cards (R9.1)

Anchors: new `cmd/hubctl` subcommand `memory export-honcho`;
`internal/vault/migrate.go`.

Change:
1. Reads the space's honcho/native memory export (defined input format:
   honcho dump or `hermes/` memory files), maps to `cards/notes` +
   `cards/contacts` with `source: migrated`, `confidence: medium`.
2. `--apply` writes; default is a dry-run report (counts, samples,
   conflicts). Idempotent: rerun reports `already migrated`.

Tests (red first):
- `TestExportDryRunNoWrites`.
- `TestExportIdempotent` — second run reports, doesn't duplicate.
- `TestExportMapsTypes` — contacts/decisions land in right folders.

### S9.2 Eval fixture port (R9.2)

Anchors: `internal/vault` testdata + eval runner (Go test, no model).

Change:
1. Port the iva `memory-eval` pattern: seeded vault corpus +
   canned model proposals + expected search ranks/rollup outcomes as
   Go testdata (JSON); runner asserts search order, fact formats,
   fold-up behavior deterministically.

Tests:
- `TestMemoryEvalFixture` — the fixture itself; extendable corpus.

### S9.3 `memory vacuum` (R9.3)

Anchors: `cmd/hubctl` subcommand; `internal/vault/cleanup.go`.

Change:
1. Streaming cleanup: collapse duplicated `description` growth, cap
   oversized legacy descriptions, never touch fact lines or History;
   report lines-per-file before/after; scoped commit `memory: vacuum`.

Tests:
- `TestVacuumCollapsesDup` / `TestVacuumPreservesFacts`.
- `TestVacuumStreaming` — bounded memory on a large fixture.

## Red-team checklist

- Migration never invents evidence: migrated facts carry
  `source: migrated`, not forged `[[daily/…]]` refs.
- Vacuum on a git-dirty vault → refuses without `--force` (history is
  the backup; don't vacuum into uncommitted state).
- Eval fixture must not require network/model — enforced by test env.

## Acceptance

A9.1–A9.3: dry-run report correct, apply writes; eval fixture green;
vacuum shrinks seeded bloat with facts intact.

## Out of scope

- Honcho removal (config stays supported per D10).
- Backfill of historical transcripts into `daily/` (forward-only).
