# CHG-0208: memory_card write contract and zone guards (M20 stage 5)

Chain: ADR-0086 D5/D7 -> SPEC-0085 R5 -> this plan -> `state.yaml`.
Previous stage: CHG-0207. Next stage: CHG-0209.

## Goal

Exactly one audited way for the agent to mutate memory: `memory_card`
with `fact`/`truth`/`merge` ops, owner-instruction-gated merge, and
ToolHub path-scoping that denies generic file writes into the memory
zones — so curated knowledge can't be silently rewritten.

## Read before coding

1. `research.md` (iva `write_card` semantics); `internal/agenttools`
   tool patterns; `internal/toolhub` path scoping (`os.Root` scopes,
   SPEC-0042); CHG-0204 card schema.

## Slices

### S5.1 `fact` op (R5.1)

Anchors: `internal/agenttools` new `memory_card.go`; `internal/vault/cards.go`.

Change:
1. `memory_card{op:"fact", card, fact, evidence}` — validates the card
   exists/type matches schema, resolves `evidence` to a real daily-leaf
   line (or explicit owner text), appends `- <date>: fact · [[daily/…]]
   HH:MM`, scoped commit attributed `agent`.

Tests (red first):
- `TestFactAppendsWithEvidence` — line format exact.
- `TestFactEvidenceMustResolve` — bogus leaf ref rejected.
- `TestFactOnMissingCard` — structured error naming the fix.

### S5.2 `truth` op (R5.1)

Anchors: same; `internal/vault/truth.go`.

Change:
1. `memory_card{op:"truth", card, truth, base_hash}` — applies only when
   the file's current hash equals `base_hash`; conflict → returns
   `conflict_owner_edit` with `truth_pending` recorded, never clobbers.

Tests:
- `TestTruthHashMismatchConflict` — owner edit wins.
- `TestTruthArchivesReplaced` — old truth lands in `## History`.

### S5.3 `merge` op + owner gate (R5.1, R5.2)

Anchors: tool admission layer (existing owner-instruction machinery —
  the "grant is not an instruction" rule from SPEC-0040 §5).

Change:
1. `merge` executes only when the call carries a concrete owner
   instruction reference; otherwise denied with the standard receipt.
2. Schema errors return the corrected call shape; N repeated invalid
   calls hit the bounded-failure path.

Tests:
- `TestMergeNeedsOwnerInstruction` — denied otherwise.
- `TestSchemaErrorReturnsCorrectedShape`.
- `TestRepeatedInvalidCallsBounded`.

### S5.4 Zone guards (R5.3, R5.4)

Anchors: `internal/toolhub` path-scope enforcement; `internal/stack`
render of the vault scope list.

Change:
1. `memory/daily`, `memory/summaries`, `memory/cards` are write-denied
   for generic file tools; reads allowed. `library/` and other vault
   paths writable. `CORE.md` writable only via the bounded writer.
2. Guard list rendered from one constant shared by scopes, tools and
   docs — no drift between layers.
3. All contract writes commit scoped with writer attribution
   (`agent`/`owner`/`rollup`).

Tests:
- `TestFileWriteDeniedInMemoryZones` — each zone denied.
- `TestLibraryWritable`.
- `TestCoreOnlyViaBoundedWriter`.
- `TestCommitAttribution` — git author per writer class.

## Red-team checklist

- Agent constructs `merge` args claiming owner confirmed → denied:
  confirmation is a protocol fact, not a parameter.
- `fact` with frontmatter-breaking content → escaped/validated, schema
  stays valid.
- Path tricks (`cards/../cards/x`, symlink inside vault) → `os.Root`
  confines; same deny list applies.
- `truth` replayed with stale hash → conflict, not silent apply.

## Acceptance

A5.1–A5.4 on dev stack: fact appends; merge without instruction refused
with deny receipt; `file_write` into `cards/` denied; git log shows
writer classes.

## Out of scope

- UI for owner merge confirmation flow (uses existing instruction path).
- Card deletion (ops limited to fact/truth/merge in M20).
