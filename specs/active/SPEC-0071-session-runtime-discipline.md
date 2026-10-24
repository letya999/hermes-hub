---
description: Frozen acceptance contract for M16 Session and Runtime Discipline.
last_verified: 2026-10-24
---
# SPEC-0071: Session and runtime discipline

Frozen: 2026-10-20. Decision: ADR-0074. Changes: CHG-0151 through CHG-0159.

- R1 (#151): Create/unpack ZIP only within admitted workspace; reject traversal, encrypted/non-regular entries, existing targets, >8192 entries, >1 GiB total or >8 MiB per file; remove partial attempts.
- R2 (#183): Explicit session-scratch todo model, no cross-scope authority or implied external synchronization; document the lifecycle and grants.
- R3 (#184): Memory and session search cannot cross owner/context; provider contract and disable/revoke semantics explicit; no memory bodies in diagnostics or shared exports.
- R4 (#196): Conversation-session continuity is native and stable: job and retry identity never fork the session, a new session is an explicit owner action (`/new`, `/use`). Bounded context comes from Hermes in-loop compaction plus a runaway token ceiling (above the managed compaction trigger) refused pre-send with observable 413 and no upstream send; the ceiling never fires where compaction could still fit.
- R5 (#207, extended by CHG-0159): A group is silent unless explicitly configured, and then runs exactly one per-chat mode. `mention` (default): dedicated opt-in group scope/home and authorized members, mention-gated execution with bounded passive transcript, audience-bound reply, no automatic personal-memory sharing; an empty member list admits every configured Telegram user and an empty mention tag falls back to the bot's own @username. `topics`: the group is a personal interface — the sender resolves through the ordinary `telegram_ids` mapping, each forum topic adopts its own session in the sender's home with `user:` authority, and messages/commands behave like a private chat.
- R6 (#213): Optional sandbox bootstrap goal; once-only execution before jobs, hard timeout and egress boundary, reviewable diff, durable not-ready failure and explicit retry.
- R7 (#214): Active job/stream/approval prevents idle reap, deadline starts at final release, uncertain state preserved and stale claims bounded.
- R8 (#249): Read-only scoped knowledge, source/line/hash/time provenance, current reads after updates/deletes, immediate revoke/unbind, traversal/symlink rejection, explicit unavailable/stale snapshots; no implied live connector.

Acceptance A1..A8: each corresponding requirement has regression tests through its real Go boundary, updated docs, and the full local gate. Docker and real provider/channel acceptance are separate evidence and cannot be asserted from fixtures.
