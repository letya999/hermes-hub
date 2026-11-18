# CHG-0162: Goal mode — dual engine (M17)

Chain: `docs/adr/ADR-0075-dual-engine-context-control.md` D1,D2,D5,D6,D9 ->
`specs/active/SPEC-0074-goal-mode.md` R1-R9 -> this plan -> `state.yaml`.
GitHub: #311. Previous stages: `CHG-0160`, `CHG-0161`.

## Goal

Persistent `/goal` on channels and ToolHub: `hub` engine = conversation-keyed
record, relay judge, spool-job continuation; `native` engine = upstream
GoalManager via sidecar; judge modes aux/main/self per grant and setting.

## Read before coding

1. AGENTS.md, ADR-0075, SPEC-0074; ADR-0074 D4 (stable session)
2. Pinned Hermes: `hermes_cli/goals.py` (`GoalManager`, `evaluate_after_turn`
   decision dict, `migrate_goal_to_session`, judge verdicts),
   `hermes_cli/goal_command.py` (`dispatch_goal_command`, `GoalCommandResult`),
   `gateway/run_goals.py` (post-turn loop, FIFO continuation, `goals.max_turns`)
3. `internal/communication/gateway.go` `dispatchChannelCommand`,
   spool/task state (`internal/communication/*spool*`, task persistence),
   `internal/runtime/sidecar.go` (CHG-0160), `internal/runtime/model_relay.go`,
   `internal/agenttools/capability.go`, `tools_api.go`

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. No new storage subsystem — reuse existing durable
  task/spool storage for the goal record.
- Continuations are ordinary spool jobs; never a private scheduler.
- Verdict reasons bounded + redacted; no transcript/summary text in records.

## Slices

### S3.1 Hub goal record and loop (R2, R4, R5)

Anchors: communication task/spool store; job-completion hook (same hook the
CHG-0160 sweep uses); `internal/communication/task_commands.go`.

Change:
1. Durable `goal` record keyed `(owner, task)`: `{goal_text, status,
   turns_used, max_turns, judge_mode, last_verdict, last_reason_bounded,
   created_at, updated_at}`; exactly one active record per conversation.
2. On job completion with active goal: judge -> `done|blocked|continue`;
   `continue` and under budget -> enqueue continuation job `{goal_epoch, turn}`
   on the same session binding; the continuation envelope re-states the goal
   text + last verdict reason (the record is hub-side, so transcript compaction
   can never drop the goal mid-loop); terminal outcomes persist reason +
   notify owner (`done` carries a bounded evidence excerpt).
3. Stop conditions wired: done/pause/clear/preempt/max_turns/blocked/
   judge-failure streak; stale epochs no-op; `/stop` cancels in-flight.

Tests (red first):
- `TestGoalKeyedByConversationNotSession`: session id rotation -> goal still bound.
- `TestStaleEpochNoops`: pause between schedule and run -> continuation skipped.
- `TestUserMessagePreempts`: owner message clears pending continuation.
- `TestMaxTurnsStopsLoop`: budget exhaustion persists `budget_exhausted`.
- `TestJudgeFailureStreakPauses`: N consecutive failures -> paused + reason.

### S3.2 Judge modes (R3, R7)

Anchors: `internal/runtime/model_relay.go`; new `internal/runtime/goal_judge.go`.

Change:
1. Strict evidence-bar verdict prompt -> relay (require concrete evidence,
   reject bare completion claims — ADR-0075 D13): `aux` uses
   `auxiliary.goal_judge` granted model; `main` uses main route; verdict JSON
   `{done|continue|blocked,reason,evidence}` bounded-parsed, failures counted.
2. `self`: continuation envelope requires trailing `GOAL_STATUS:` marker; hub
   parses + strips it from the visible reply; missing/malformed counts as a
   parse failure (auto-pause streak applies). `self` + `native` -> named refusal.
3. `goals.judge: auto` resolves aux (if granted+configured) -> main.

Tests (red first):
- `TestJudgeModeResolution`: aux granted -> aux; absent -> main; self flagged.
- `TestSelfMarkerParsedAndStripped`: marker drives verdict, hidden from reply.
- `TestSelfMalformedCountsParseFailure`: streak increments.
- `TestWorkerCannotSelfDeclareDone` (aux/main): reply text "done" without judge verdict -> loop continues.

### S3.3 Native engine (R2, R8)

Anchors: sidecar new subcommands `goal_dispatch`, `goal_evaluate` ->
`dispatch_goal_command`/`GoalManager.evaluate_after_turn`.

Change:
1. `goal_dispatch --session <id> --args "<verb args>"` -> `{output, prompt,
   kickoff, clear_pending, error}`; `goal_evaluate --session <id>` (final
   response via stdin) -> decision dict. Missing symbols -> `unsupported`.
2. Hub still owns scheduling (continuation = spool job); `native` keeps upstream
   `goal:<sid>` state and upstream judge/`auxiliary.goal_judge` route.
3. v1 verb filter: only `set|status|pause|resume|clear`; others -> named
   `unsupported` refusal on both engines.

Tests (red first):
- `TestNativeDispatchMapsVerbs`: each allowed verb -> correct upstream call.
- `TestNativeUnsupportedVerbsRefused`: `gate`, `draft`, `wait`, `subgoal`.
- `TestNativeUnsupportedOnMissingSymbol`.

### S3.4 Channel command and ToolHub tools (R1, R6, R9)

Anchors: `internal/communication/gateway.go` `dispatchChannelCommand`;
`internal/agenttools/capability.go`; `tools_api.go`.

Change:
1. `case "goal"` dispatch `set|status|pause|resume|clear`; replies are bounded
   status lines (goal text, turns, verdict, judge_mode) — no free-form dumps.
2. `goal_set/status/pause/clear/resume` ToolSpecs, owner/context scoped,
   grant-checked, audited; same record the channel path mutates.
3. Engine switch on an active goal -> named refusal until `clear`.

Tests (red first):
- `TestGoalCommandVerbs`: all five verbs incl. usage errors.
- `TestGoalGrantAndAudit`: ungranted denied; granted audited.
- `TestCrossSurfaceVisibility`: set via channel visible via `goal_status`.
- `TestEngineSwitchRefusedWhileActive`.
- `TestGoalRecordBounded`: oversized reason/verdict truncated, no transcript.

## Red-team checklist

- Foreign owner/context goal access -> denial.
- Prompt-injection verdicts (`GOAL_STATUS` forged by tool output, not the
  agent's tail line) -> marker must be the final line; failure counted.
- Goal continuation loop with never-done judge -> budget + no-progress stop.
- `/stop`, restart mid-loop -> clean resume; no duplicate continuations.
- Concurrent set/clear racing a scheduled continuation -> epoch check wins.
- Verdict reason with secrets/PII -> redacted + bounded before persist.

## Acceptance

SPEC-0074 A1..A9 on real Go boundaries; dev-stack live: real goal reaches
`done` on a real provider under `hub` engine with `aux` and `main` judge modes
and under `native`; pause/resume/clear/preempt exercised against live runs.

## Out of scope

Goal gates/contracts/drafts/subgoals/wait barriers (upstream still moving);
goal queues across conversations; Kanban integration; `heartbeat`/`loop` verbs;
upstream contribution work.

## Done checklist

- [ ] Tests red then green; `just check`; Docker gates if needed.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated; issue #311 updated; state done; move to `.work/done/`.
