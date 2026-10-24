# CHG-0151: Daily leaf ingest (M20 stage 2)

Chain: ADR-0073 D2/D3 -> SPEC-0070 R2 -> this plan -> `state.yaml`.
Previous stage: CHG-0150. Next stage: CHG-0152.

## Goal

Every accepted owner turn and agent reply lands verbatim in
`vault/memory/daily/YYYY-MM-DD.md` in the space timezone — append-only,
attributed, untrusted-content tagged — so the night rollup has a
lossless raw layer.

## Read before coding

1. CHG-0150 (vault exists); `internal/communication/*` message flow;
   `docs/channels.md`, `docs/routines.md`; iva `agent/lib/vault-daily.ts`
   behavior (research.md).

## Slices

### S2.1 Leaf writer (R2.1, R2.3)

Anchors: new `internal/vault/leaves.go`; caller: the communication
plane's accepted-turn path (`internal/communication`).

Change:
1. `AppendLeaf(vaultDir, tz, entry{ts, channel, speaker, body, flags})` —
   opens `memory/daily/<date>.md` (create with `# <date>` header if
   absent), appends a fenced entry block, fsyncs, returns offset.
2. Entries are verbatim: no normalization beyond newline fencing;
   speaker ∈ {owner, agent, system}.
3. Day boundary uses `agent.yaml` `timezone` (IANA), not UTC.

Tests (red first):
- `TestAppendLeafVerbatim` — bytes preserved, order stable.
- `TestAppendLeafCreatesDayFile` — first entry writes header.
- `TestAppendLeafTZBoundary` — 23:59 UTC vs owner tz splits correctly.
- `TestAppendLeafFsyncDurability` — read-back-after-write contract.

### S2.2 Untrusted marking (R2.2)

Anchors: ingest call sites in `internal/communication` (forwarded
message metadata path).

Change:
1. Entries whose transport metadata marks them forwarded/external get a
   `> [!untrusted]` block wrapper decided at ingest — never by content.

Tests:
- `TestForwardedEntryMarked` — marker present, decided by metadata flag.
- `TestOwnerEntryUnmarked` — plain turns have no marker.

### S2.3 Scoped commits (R2.4)

Anchors: `internal/vault/git.go` (new, shared with later stages).

Change:
1. `CommitScope(vaultDir, scope, paths)` — stage named paths, commit with
   `memory: <scope>` prefix; batching caller (timer-based, e.g. 15 min or
   on day roll), no per-message commits.
2. Honor `.gitignore`: excluded paths log as outside-backup, never fail.

Tests:
- `TestCommitScopeBatches` — multiple appends → one commit.
- `TestCommitScopeIgnoresExcluded` — excluded file doesn't block.

### S2.4 Wiring into accepted turns (R2.1)

Anchors: the communication plane's accepted inbound/outbound handlers.

Change:
1. After a turn is accepted/answered, append both sides to the leaf for
   spaces with `memory_vault` enabled; ingest failures log and never
   break message delivery.

Tests:
- `TestExchangeLeavesTwoEntries` — owner+agent order.
- `TestIngestFailureDoesNotBreakDelivery`.

## Red-team checklist

- Body containing `]]` or fence-breakers cannot forge an entry boundary
  (length-prefixed or escaped fencing — pick one, test it).
- A poisoned day file (hand-edited) never crashes the appender.
- Huge inbound bodies are appended (leaves are lossless), but a single
  entry is size-capped with an explicit truncation note inside the fence.

## Acceptance

A2.1–A2.4 on dev stack: Telegram exchange → verbatim leaf; forwarded →
marked; kill -9 between appends → no loss; `git log` shows scoped commits.

## Out of scope

- CLI `hubctl chat` sessions ingest (follow-up if wanted).
- Rollup consumption of leaves (CHG-0152).
