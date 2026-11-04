# CHG-0208: memory_card contract, soft forget, zone guards (M20 stage 6)

Chain: `docs/adr/ADR-0086-memory-vault-openknowledge.md` D7/D16 ->
`specs/active/SPEC-0085-memory-vault-openknowledge.md` R6 -> this plan ->
`state.yaml`. Evidence: `../CHG-0204-vault-layout/research.md`.
Previous stage: CHG-0206. Next stage: CHG-0212.

## Goal

`memory_card` is the only agent write path into memory zones: ops
`fact`/`truth`/`merge`/`forget`/`remember`, owner-turn-bound
authorization for merge/forget, soft forget that excludes forever, zone
guards enforced on every write surface, and full write audit.

## Read before coding

1. ADR-0086 D7/D16; SPEC-0085 R6; research.md (iva `write_card`).
2. `internal/agenttools` tool registration + ToolHub path scoping;
   `internal/vault` writer + schema (CHG-0204); `internal/memory`
   prov.db + index (CHG-0205/0207); spool `SourceRef` on the triggering
   turn.

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first. Go stdlib only.
- Tool-contract changeset ships in one commit: description,
  `internal/stack/prompt_contract.go`, `docs/tools.md`,
  `docs/routines.md`, catalog bump.
- `just check` before handoff.

## Slices

### S6.1 Ops surface (R6.1)

Anchors: `internal/agenttools` (new `memory_card` tool); vault writer.

Change:
1. `fact` (append-only dated line with evidence), `truth` (D5
   hash-checked Compiled Truth), `merge`, `forget`, `remember` →
   `cards/notes/inbox` + index refresh on write.
2. Schema errors return the corrected call shape; repeated invalid
   calls hit the bounded-failure path.

Tests (red first):
- `TestCardOpsMatrix`, `TestRememberSearchableSameDay`,
  `TestBadSchemaReturnsShape`.

### S6.2 Owner-turn binding (R6.2)

Anchors: tool runtime; spool `SourceRef` on the triggering turn.

Change:
1. `merge`/`forget` execute only when the run's triggering turn carries
   owner authorization — bound to that turn's `SourceRef`; an argument
   claiming "owner confirmed" or a replayed old ref is denied.

Tests:
- `TestMergeDeniedWithoutOwnerTurn`, `TestForgetDeniedArgClaim`,
  `TestReplayedAuthDenied`.

### S6.3 Soft forget mechanics (R6.3)

Anchors: `internal/memory` supersede marker + prov.db; search filters.

Change:
1. Struck + superseded record in prov.db → excluded from search
   (unless `history:true`), lane-1 recall and promotion — permanently,
   across restarts; git history retained.

Tests:
- `TestForgetExcludedEverywhere`, `TestForgetSurvivesRestart`,
  `TestForgottenVisibleOnlyInHistory`, `TestReingestionInert`.

### S6.4 Zone guards on all surfaces (R6.4)

Anchors: ToolHub path scoping; file tools; `memory_card`; OK MCP
deny layer (defense-in-depth while agent OK write is unrendered).

Change:
1. `memory/daily`, `memory/summaries`, `memory/cards` denied to every
   write surface except the contract path; `library/` stays writable;
   `CORE.md` bounded writer only.

Tests:
- `TestZoneGuardFileTools`, `TestZoneGuardContractPath`,
  `TestZoneNameUnicodeTricks`, `TestCoreWriterOnly`.

### S6.5 Write audit (R6.5)

Anchors: vault writer commits; audit log.

Change:
1. Every mutation logged with the authorizing turn's `SourceRef`;
   commits attributed agent/owner/rollup/ui.

Tests:
- `TestWriteAuditSourceRef`, `TestCommitAttribution`.

## Red-team checklist

- Prompt-injected `merge`/`forget` inside an untrusted turn → denied.
- Replayed old authorization → denied.
- Forget → deliberate re-ingestion of the same fact → still inert.
- Case/unicode/path tricks on zone names → denied.

## Acceptance

A6.1–A6.6 on the dev stack: "запомни" → card + same-day search hit;
"забудь" → absent after restart; crafted merge → denial in audit.

## Out of scope

- Human conflict-resolution UI (REVIEW.md covers visibility), hard
  purge internals (`memory vacuum` in CHG-0212).

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
