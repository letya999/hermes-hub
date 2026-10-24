---
description: M16 bounded sessions, private memory, group disclosure and sandbox bootstrap.
last_verified: 2026-10-20
---
# ADR-0074: Session and runtime discipline

Status: Accepted for isolated implementation; deployment acceptance pending.

## Context
Milestone 17 (M16), issues #151/#183/#184/#196/#207/#213/#214/#249.
The source baseline reuses conversation sessions, supports staged knowledge, and protects active runtimes with leases.

## Decisions
1. ZIP extraction limits each entry to 8 MiB in addition to aggregate bounds.
2. Todo remains native session scratch; it is not a durable task tracker. Session identity is the authority; no external sync.
3. Memory is owner/context scoped; provider disable withdraws access without deleting or migrating content. External providers remain opt-in reviewed connectors, never guessed APIs.
4. Session identity stays native: one stable conversation session across jobs and retries; a new session is only an explicit owner action (`/new`, `/use`). Hermes owns transcript continuity and compaction; the hub keeps only exact admission receipts and a runaway token ceiling above the managed compaction trigger. Rejected: per-job fresh sessions with a hub-held continuity cache — it duplicates state Hermes already owns, breaks in-loop compaction and native `/goal`, and replaces an explicit `/new` boundary the owner already controls.
5. Groups require explicit owner-configured dedicated space and member allowlist. Personal homes are never selected for group turns. Non-mentioned messages are bounded untrusted transcript, not memory writes.
6. Goal bootstrap runs once inside the selected sandbox, with a finite deadline, explicit retry and reviewable changes; failure blocks ordinary jobs.
7. Existing job/stream/approval leases satisfy idle protection; do not add speculative process sampling.
8. Knowledge reads reauthorize each call, use confined files and return provenance. Disabling a source invalidates access immediately; exports are snapshots with explicit freshness, not live connectors.

## Alternatives
No required database, vector index, tracker service or second agent loop. No upstream patches.

## Invariants
Provider writes require concrete user authority. No secret material in results or logs. No automatic personal-to-group sharing. Tests cannot stand in for real provider acceptance.
