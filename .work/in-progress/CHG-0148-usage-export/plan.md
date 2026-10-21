# CHG-0148: usage export surface (M17 stage 1)

Chain: `docs/adr/ADR-0072-ops-admin-delivery.md` D1 -> `specs/active/SPEC-0069-ops-admin-delivery.md` R1
-> this plan -> `state.yaml`.

## Goal

`hubctl usage` gives the operator the journal's per-scope and per-actor
consumption over a time window without entering chat or running the gateway.

## Read before coding

1. `internal/communication/usage_journal.go` (entry shape, torn-tail contract)
2. `cmd/hubctl/main.go` op wiring; `cmd/hubctl/apply.go` flag style
3. Issue #195

## Slices

### S1.1 Journal reader (R1.1, R1.2)

Anchors: `internal/communication/usage_journal.go` `usageEntriesLocked`,
`repairUsageTailLocked`.

Change:
1. Exported `ReadUsageJournal(root string) ([]UsageRecord, error)` — opens
   `usage.jsonl` read-only, decodes each completed line, tolerates a final
   unterminated line (valid JSON tail is included; invalid tail is skipped as
   the known torn-write case), errors on a corrupt completed record.
2. Exported `UsageRecord` view type mirroring `usageEntry` fields operators
   query (scope, actor, job, run, model, tokens, measured_at).
3. `AggregateUsage(entries, since, user, model)` → per-scope and per-actor
   `UsageTotals` maps plus overall totals.

Tests (red first):
- `TestReadUsageJournalAggregates`: write a fixture journal, aggregate, assert
  per-scope/per-user sums and window filtering.
- `TestReadUsageJournalTornTail`: truncated last line ignored; corrupt middle
  line errors.

### S1.2 `hubctl usage` (R1.1)

Anchors: `cmd/hubctl/main.go` op dispatch; `cmd/hubctl/apply.go` report style.

Change:
1. `usage` op: flags `--spool` (required), `--since Nd`/`--from RFC3339`,
   `--user`, `--model`, `--json`. Prints scope table + actor table or JSON.
2. Usage entry in the `hubctl` help line.

Tests:
- `TestUsageCommandAggregates` in `cmd/hubctl`: temp journal, run op, decode
  report.

## Red-team checklist

- Journal with hostile/corrupt completed line → clear error, no partial sums.
- Missing journal file → empty report, not an error.
- Concurrent gateway append while reading → reader sees a consistent prefix.

## Acceptance

SPEC-0069 A6: exported totals equal the journal's own sums for the window.

## Out of scope

Budgets, quotas, enforcement; runtime-session usage (already `/v1/usage`).

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] state.yaml + issue #195 comment.
