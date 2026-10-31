# CHG-0205: Provenance-aware daily capture (M20 stage 2)

Chain: `docs/adr/ADR-0086-memory-vault-openknowledge.md` D3/D11/D18 ->
`specs/active/SPEC-0085-memory-vault-openknowledge.md` R2 -> this plan ->
`state.yaml`. Evidence: `../CHG-0204-vault-layout/research.md`,
`../CHG-0204-vault-layout/spike-m20.md`.
Previous stage: CHG-0204. Next stage: CHG-0207.

## Goal

Every accepted owner turn and agent reply lands verbatim in
`vault/memory/daily/YYYY-MM-DD.md` with unforgeable provenance in the
hub-owned `runtime/memory/prov.db` sidecar — background kinds captured
for audit but promotion-ineligible, recalled content marked
non-extractable, and a pre-compaction flush that loses nothing.

## Read before coding

1. ADR-0086 D11; SPEC-0085 R2; spike-m20.md (Trigger values, compaction
   anchors); OpenClaw provenance classes (research.md).
2. `internal/communication/gateway.go` (job admission),
   `gateway_delivery.go` (reply path), `internal/commodel/delivery.go`
   Job, `internal/compaction/compact.go` (flush hook point),
   `internal/vault` writer (CHG-0204).

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first. Go stdlib only.
- `just check` before handoff.

## Slices

### S2.1 Entry schema + sidecar store (R2.2)

Anchors: `internal/memory/entry.go`; `internal/memory/provdb.go` —
`runtime/memory/prov.db` (outside vault).

Change:
1. `Entry{ID, SourceRef, Provenance, Kind, Audience, ObservedAt}`; stable
   IDs (`<date>-<seq>` from a sidecar counter, replay-deterministic).
2. prov.db = newline-delimited JSON sidecar (no new deps), fsync'd
   append before the leaf write is acked.
3. `PromotionEligible(entry)` = `owner|agent` && `Kind=interactive` —
   the single function every downstream gate calls.

Tests (red first):
- `TestEntryRoundtrip`, `TestProvDBAppendFsync`, `TestReplayStableIDs`,
  `TestPromotionGateMatrix`.

### S2.2 Verbatim leaf append (R2.1, R2.3)

Anchors: `internal/communication/gateway.go` job admission;
`gateway_delivery.go` `deliverOne`; `internal/vault` writer.

Change:
1. On accepted owner turn → append `### HH:MM — owner` fenced block to
   today's leaf via VaultWriter; on agent reply → `agent` block.
   Timestamp/speaker/channel/audience in a display-only comment.
2. Untrusted classification from transport metadata (forwarded/external/
   tool-originated), stamped in the sidecar AND a `> [!untrusted]`
   display marker; text inside can never reclassify the entry.

Tests:
- `TestLeafAppendVerbatim`, `TestUntrustedAtCapture`,
  `TestForgedPrefixStaysUntrusted`.

### S2.3 Kind from `job.Trigger` (R2.4)

Anchors: `internal/commodel/delivery.go` Job.Trigger; capture call sites.

Change:
1. `KindForTrigger`: `message`→interactive, `cron`→routine,
   `goal`→goal, `compact`→compaction, `wake`/`restart`/`auto-title`→
   system; assignment/probe map from their job fields.

Tests:
- `TestKindTriggerMatrix`, `TestBackgroundPromotionIneligible` (100
  wakes → 0 eligible).

### S2.4 Recalled-block exclusion (R2.5)

Anchors: capture path; the D13 recalled marker constant.

Change:
1. The recall injection (CHG-0238) and any recalled text inside a reply
   carry the marker; capture strips/skips marked spans — they can never
   land as new entries.

Tests:
- `TestRecalledBlockNotCaptured`, `TestRecallEchoExcluded`.

### S2.5 Pre-compaction flush (R2.6)

Anchors: `internal/communication/gateway_compact.go`;
`internal/compaction/compact.go` before the compress call.

Change:
1. Before compact drops turn content, flush unsaved entries into the
   leaf as `agent`/`compaction` — audit-visible, never
   promotion-eligible.

Tests:
- `TestCompactionFlushCaptured`, `TestFlushKindCompaction`.

### S2.6 Boundary forging (R2.2)

Anchors: leaf writer.

Change:
1. Body escaping: fence markers and provenance-comment prefixes inside
   bodies are neutralized (indented/escaped); a body cannot mint an
   entry boundary.

Tests:
- `TestFenceBreakEscaped`, `TestForgedProvenanceCommentInert`,
  `TestConcurrentAppendsSerialized`, `TestDSTMidnightBoundary`.

## Red-team checklist

- Forged provenance comment inside owner text → ignored (sidecar
  authoritative).
- Injected "agent:"-style prefixes cannot reclassify an entry.
- Leaf path symlink/path escape rejected; concurrent appends serialized
  through the writer.
- Untrusted entry containing directives never promoted downstream.

## Acceptance

A2.1–A2.7 on the dev stack: exchange lands verbatim; forward →
untrusted; kill -9 loses nothing acked; compaction flush present.

## Out of scope

- Search, recall lane, rollup, card writes.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
