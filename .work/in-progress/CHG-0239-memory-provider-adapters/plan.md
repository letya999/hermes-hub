# CHG-0239: External memory provider adapters via seam (M20 stage 11)

Chain: `docs/adr/ADR-0086-memory-vault-openknowledge.md` D15/D19 ->
`specs/active/SPEC-0085-memory-vault-openknowledge.md` R11 -> this plan ->
`state.yaml`. Evidence: `../CHG-0204-vault-layout/spike-m20.md` Q2.
Previous stage: CHG-0211.

## Goal

First external `memory.Provider` adapter — Hindsight over its verified HTTP
API — behind an explicit per-space capability grant, plus leaf-replay
migration and the `export-honcho` importer. Upstream `memory.provider`
plugins are never rendered (ADR D19).

## Read before coding

1. ADR-0086 D15/D19; SPEC-0085 R11; spike-m20.md Q2 (adapter-path verdict).
2. `internal/memory` Provider + `memorytest.Conformance` (CHG-0204);
   replay model (CHG-0205); scope minting.
3. Upstream `plugins/memory/hindsight/` at pin `869228c` — the verified
   API surface (`retain`/`recall`, `bank_id`, local embed endpoint).

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first. Go stdlib only — adapter is a net/http client.
- `just check` before handoff. Live provider checks stay pending per the
  no-docker scope and are recorded, not claimed.

## Slices

### S11.1 Managed admissibility (R11.2)

Anchors: `internal/stack/config.go` features; `internal/stack/render.go`.

Change:
1. `memory_provider` capability: per-space grant renders provider config
   (endpoint, bank scope) under governance; deny-wins; disable removes
   config and egress.

Tests (red first):
- `TestProviderGrantGated`, `TestProviderDisableRemovesConfig`,
  `TestProviderNoSilentFallback`.

### S11.2 Hindsight adapter (R11.1, R11.3)

Anchors: `internal/memory/providers/hindsight` (new).

Change:
1. `Provider` impl over Hindsight HTTP: retain→Capture, recall→Search/
   Recall, `bank_id`=opaque scope, ops status→Health, empty≠unavailable.
2. `memorytest.Conformance` green against a fixture HTTP server.

Tests:
- `TestConformanceHindsight`, `TestHindsightDegradedHonest`,
  `TestHindsightScopeIsolation`.

### S11.3 Leaf replay (R11.4)

Anchors: `internal/memory` replay; `hubctl memory replay`.

Change:
1. Resumable, idempotent replay of leaf ranges into `Provider.Capture`;
   equal search results vs the source provider for fixture queries.

Tests:
- `TestReplayIdempotent`, `TestReplayResumable`, `TestReplaySearchParity`.

### S11.4 `export-honcho` importer (R11.5)

Anchors: `hubctl memory export-honcho`; cards writer.

Change:
1. Honcho export → draft cards `source: migrated`, review-then-commit.

Tests:
- `TestHonchoImportReviewable`, `TestHonchoImportIdempotent`.

## Red-team checklist

- Provider sees only what scope+egress allowlist permit; no transcript
  streaming without the grant.
- Space A can never read space B via the provider (bank isolation).
- Replay re-run → identical state, no duplicates.
- Provider config forged by space content cannot mint scope/egress.

## Acceptance

A11.1–A11.6. Live protocol (flush-on-stop, session-switch recall,
revocation) recorded as pending — requires live stack (deferred —
no-docker scope); mocks never count.

## Out of scope

- Multiple simultaneous external providers; org-tier shared providers;
  vector-lane tuning inside the vault; upstream plugin rendering.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
