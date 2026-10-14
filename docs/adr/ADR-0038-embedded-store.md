---
description: Proposal — replace the gateway spool and supervisor state file trees with an embedded single-writer store, or keep files.
last_verified: 2026-10-14
---
# ADR-0038: Embedded single-writer store for spool and supervisor state (D12b)

Status: proposed, 2026-10-14. Decision gate is measured evidence, not novelty.
No implementation before owner acceptance; adding a dependency requires
`just security` and its own review.

Requirements: [SPEC-0053](../../specs/active/SPEC-0053-execution-chain-reliability.md) R4.
Origin: [ADR-0036](ADR-0036-execution-chain-reliability.md) D12b,
[CHG-0096](../../.work/in-progress/CHG-0096-control-plane-simplification/plan.md) P4b.

## Context and measured evidence

D8 (stage 2) was the conditional: adopt an embedded store only if file
persistence still dominates latency or causes correctness incidents.

Measured after stage 2 on the current file layout:

- Enqueue→claim ~26.4 ms/op, fsync-bound — the floor set by durability, not by
  the file format (BenchmarkEnqueueToClaimLatency).
- Idempotency lookup ~131–172 µs/op at 10k mappings via the `by-key` index;
  the scan fallback exists only for pre-index records and self-heals.
- Idle loop cost removed entirely (notify-driven, ≤1 poll/s per loop).
- Supervisor persists only on durable-fact changes; terminal records compact
  after 24 h; spool compacts hourly with a 336 h default retention.
- Zero correctness incidents attributed to the file store in stages 0–3
  regression suites; the races found were goroutine lifecycle bugs, not
  storage-format bugs.

## Proposal

Two options, decided by the gate below:

A. Keep files (default). The measured numbers already meet the SLO table;
   the store change carries migration risk and a new dependency for no
   demonstrated gain.
B. If post-deploy SLO data shows persistence still dominates: embed pure-Go
   SQLite, one writer per store file, `journal_mode=DELETE` (WAL is unsafe
   across VirtioFS bind mounts — NanoClaw finding), with the same durability
   contract (`PRAGMA synchronous=FULL`), the same file boundaries (spool per
   gateway, state per supervisor), and an import/export tool that round-trips
   to the current JSON layout.

## Isolation impact

- Option B keeps per-principal file boundaries identical; a single embedded
  database per store must not merge user namespaces.
- No new network surface; no background service.
- Dependency risk: `just security` gate, pinned version ≥7 days old,
  pure-Go implementation only (no cgo — the control image is CGO_ENABLED=0).

## Migration and rollback

- Option B migrates by import-on-open: JSON files remain the archive format and
  are written through unchanged for 30 days; rollback = delete the DB files and
  restart on the JSON layout.
- The single-writer rule means only the owning process ever opens its store —
  unchanged from today.

## Test and chaos plan

- The entire stage 0–3 suite reruns against the store backend behind the same
  interfaces (`Spool`, supervisor `load`/`save`).
- Chaos grid plus a kill-9-during-commit cell per backend.

## Go / no-go

Go (option B) only if live SLO data after stage 3 shows durable-persistence
latency > 2× the fsync floor or a file-store correctness incident with a
reproducing test. Otherwise record option A and close the proposal.
