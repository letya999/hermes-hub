# M10 phases A–C stabilization

Implement the remediation plan accepted by the owner on 2026-10-11. Work on
feat/m10-phase-a in hermes-hub-m10a; do not publish, push, or contact real accounts.

1. Preserve original artifacts; isolate ZIP extraction and inbound transfers,
   bound actual bytes, and make retries safe.
2. Enforce single spool writer; strict, fully validated offline apply with
   explicit routing and confined knowledge writes.
3. Recheck authorization and schedule revisions before effects; cancel queued
   work and bind mock chats, ids and attachments to their owners.
4. Bound background workers, network fetches and scripts; use process/container
   isolation for scripts rather than claiming step limits bound memory.
5. Correct watcher/check semantics, durable results and digest cursors;
   expose quiet mode, source selection, freshness and errors.
6. Fix knowledge authentication and bounded reads; implement contextual sources,
   provenance and revocation through verified read-only contracts.
7. Record usage deltas and unknown measurements honestly; support useful reports.
8. Send multiple images in one inspection; complete deterministic Telegram
   controls and forum topic routing; verify activity-based idle behavior.
9. Pin and correct the ai-stp compatibility note.
10. Upgrade all Go pins to 1.27.2, update docs and add regression evidence.

Validation: retain the 14 audit reproductions as regressions, inspect test
assertions before execution, run targeted tests, just check (>=85% coverage),
just security and applicable Docker gates. Record unavailable gates honestly.
Reuse existing Go components and dependencies; no new database or queue.

Owner updates on 2026-10-11: Docker work was stopped, then explicitly authorized
again after offline gates passed. Complete local integration acceptance with
dedicated m10a-verify tags and temporary fixtures; preserve the working stack.
Commands and acceptance criteria are in [verification-plan.md](verification-plan.md).
