---
description: M17 persistent /goal mode — dual engine behind grants, judge modes, spool-driven continuation.
last_verified: 2026-10-24
---
# SPEC-0074: Persistent goal mode

Draft: 2026-10-24 — pending owner acceptance. Decision: ADR-0075. Changes: CHG-0162.
Issue: #311.

- R1 (#311): Channel command `/goal` accepts `<text>` (set), `status`, `pause`, `resume`, `clear`; ToolHub exposes `goal_set`, `goal_status`, `goal_pause`, `goal_clear`, `goal_resume` — owner/context scoped, grant-checked, audited, identical semantics on both surfaces.
- R2: Engine selection is grant-gated over `goals.engine` (`auto|hub|native`): `hub` keeps its own durable goal record keyed by `(owner, task)` — never by Hermes session id — so session rotation cannot orphan it; `native` drives upstream `GoalManager` (`goal:<session_id>` state) through the sidecar.
- R3: Judge mode `goals.judge` (`auto|aux|main|self`): `aux` calls the strict verdict prompt through the relayed `auxiliary.goal_judge` model; `main` makes the same call on the main model (the model audits its own evidence); `self` (hub engine only) requires an in-band `GOAL_STATUS:` verdict line parsed from the run output and stripped from the visible reply — malformed or missing markers count as parse failures. `auto` resolves aux → main. `self` under `native` is a named refusal. Both `aux` and `main` prompts require concrete evidence and reject bare completion claims; a `done` verdict carries a bounded evidence excerpt to the owner's notification.
- R3b: Hub-engine continuations re-state the goal text plus last verdict reason inside the envelope; the goal record lives outside the transcript, so compaction can never drop the goal mid-loop.
- R4: Exactly one active goal per conversation. The loop stops on: achieved, paused, cleared, owner message preemption, `goals.max_turns` budget exhaustion (bounded setting, default 20), `blocked` verdict, or a bounded consecutive judge-failure streak — each with a persisted reason.
- R5: Continuation is an ordinary spool job carrying goal epoch and turn index: stale continuations no-op after pause/clear/set, owner messages preempt, `/stop` cancels in-flight work, hub restart resumes cleanly, and all quota/ceiling/approval surfaces apply unchanged.
- R6: Goal records persist `{goal_text, status, turns_used, max_turns, judge_mode, last_verdict, last_reason_bounded, created_at, updated_at}`; verdict reasons are bounded and redacted; no free-form transcript or summary text is stored beyond the record.
- R7: The judge decides — the working agent cannot self-declare completion; under `self` mode the marker is the agent's own verdict, surfaced as such (`judge_mode: self` in status/audit).
- R8: v1 exposes no `draft`, `contract`, `gate`, `wait` or `subgoal` verbs on either engine; they return a named unsupported refusal.
- R9: A goal set through any surface is visible to all surfaces sharing the same conversation binding; engine switches on an active goal are a named refusal until `clear` (no silent state translation).

Acceptance A1..A9: regression tests cover command/verb semantics, grant denial, judge-mode resolution, verdict parsing (including malformed `self` markers), budget and failure-streak stops, preemption, epoch idempotency, restart resume, owner/context isolation; dev-stack acceptance runs a real goal loop to `done` on a real provider with both engines and both judge fallbacks.
