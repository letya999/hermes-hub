---
description: M17 dual-engine manual/automatic context compaction and persistent goal mode behind grant-selected engines.
last_verified: 2026-10-24
---
# ADR-0075: Dual-engine context compaction and goal control

Status: Proposed — pending owner acceptance; implementation not started.

## Context
Issues #309 (manual compact), #310 (explicit automatic compaction policy), #311
(persistent `/goal` mode). Verified against pinned Hermes `869228cab4a8276d3b4c78da9d9939670c47bd0f`:
the managed `api_server` surface exposes `/v1/runs` and session resources but no
compact endpoint, no slash dispatch, no post-turn goal hooks and no compaction
telemetry. The container does ship the machinery as importable libraries:
`ContextCompressor`, `partial_compress` helpers, `GoalManager`,
`dispatch_goal_command`, cross-process `compression_lock`/`session_turn_lease`,
and the atomic in-place commit `SessionDB.archive_and_compact`. Auto-compaction
already runs natively in-loop; the hub renders `compression.*` today.

## Decisions
1. Two engines per feature, selected by owner grant, like shell execution:
   `native` delegates to upstream Hermes machinery; `hub` is our own Go-side
   implementation. Precedence: grant (which engines are allowed) > space
   `features.<name>.engine` setting (`auto|hub|native`, bounded, fail-closed) >
   default. `auto` resolves to `hub` until upstream advertises structured
   surfaces via `/v1/capabilities`, then prefers `native`.
2. Managed transport is a hub-owned Python sidecar inside the runtime image. It
   imports upstream libraries and exposes only primitives: cross-process locks,
   raw transcript read, atomic transcript apply, and (for `native`) upstream
   calls. It is not a Hermes patch and never patches upstream code. When upstream
   ships structured HTTP endpoints the sidecar is replaced by an HTTP client;
   hub contracts do not change.
3. Hub compact engine is ours end-to-end as a cheapest-first cascade
   (Osinara/OpenCode/Pi consensus): (a) tool-output prune-only pass that commits
   with zero LLM calls when it fits, (b) a precomputed summary swap-in when one
   is ready, (c) one summary call, (d) one shrink-and-retry, (e) an explicit
   drop-checkpoint naming the loss — never an unbounded retry. Structured
   anchored summary prompt goes through the model relay; refuse-if-grows
   verification, alternation check, commit, journal. The commit still uses
   `archive_and_compact` — it is a storage primitive, not policy. The
   append-only transcript stays the truth; compaction changes the view, never
   the archive.
4. Automatic compaction is two independent mechanisms: native in-loop
   `ContextCompressor` (mid-run, policy-rendered, always the last defense) and a
   hub between-jobs sweep that compacts an over-threshold session before
   admitting the next job — deterministic, never mid-run. They carry different
   purposes: the sweep threshold is a quality knob (context-rot research shows
   degradation far below the window), the native trigger is survival against
   the wall.
5. Hub goal engine keeps its own durable record keyed by hub conversation
   identity (`owner, task`), not by Hermes session id — rotation-proof by
   construction because the declared session key already re-points to the
   compaction tip. Continuation is an ordinary spool job: quotas, `/stop`,
   owner preemption, restart survival and audit come free. The `native` engine
   drives upstream `GoalManager` state (`goal:<session_id>`) through the sidecar.
6. Goal judge has modes `aux|main|self`. `aux` uses `auxiliary.goal_judge`
   through the model relay (a separate supervisor model). `main` makes the same
   strict judge call on the main model — the model audits its own evidence.
   `self` (hub engine only) adds an in-band `GOAL_STATUS` verdict line to the
   continuation envelope — zero extra calls, weaker trust. `auto` = aux → main.
7. Any transcript mutation happens only at a turn boundary under upstream's own
   cross-process locks; an in-flight run makes compaction refuse or queue —
   never a mid-run rewrite, never a literal `/compress` as model input, never a
   fresh session for compaction.
8. Observability is allowlisted and honest: journal rows carry `{trigger,
   engine, method, tokens_before_est, tokens_after_est, failure_class, at}`;
   unknown fields stay `null`; summary text and verdict reasoning never enter
   diagnostics, logs or other contexts.
9. Threshold ladder is a contract: managed compressor trigger (200k) < hub
   runaway ceiling (256k) < provider window; the ceiling must not reject what
   compaction could still fit. Sweep threshold is independently bounded below
   the in-loop trigger.
10. v1 scope cut: no goal `draft`/`contract`/`gate`/`wait`/`subgoal` exposure
    (gates execute shell), no `micro_compact`/idle compaction/`context.engine`
    opt-ins. Provider-native compaction (Anthropic `context_management`,
    OpenAI `compact_threshold`) is a recognized future engine class
    `provider`, deferred until the relay and hub can negotiate it honestly.
11. Hub sweep precomputes: when a session estimate crosses ~85% of the sweep
    threshold, a background summary is started so a threshold crossing commits
    instantly (OpenCode precompute pattern). Bounded: model-stable (swap-in
    only for the same resolved model), still-describing check (held summary
    covers the same tail), max 2 consecutive background failures per session,
    never on the user-visible path when late.
12. Post-compact re-injection: after a commit, the next turn carries a bounded
    notice (compaction happened + durable hub state re-stated: active goal
    text, pending items). Hub-owned goal text lives outside the transcript, so
    compaction can never lose it (NanoClaw post-compact reminder / Osinara
    todo re-injection insight).
13. Judge evidence bar: `aux`/`main` judge prompts require concrete evidence
    and reject bare completion claims (PraisonAI/Anthropic/ECC consensus —
    different-model judging preferred to avoid self-grading bias). A `done`
    verdict surfaces a bounded evidence excerpt to the owner, not just a flag.

## Alternatives
Rejected: patching or vendoring upstream Hermes (project rule); waiting only
for upstream HTTP surfaces (blocks delivery indefinitely); a Go writer against
state.db schema (fragile second writer); hub-held per-job sessions for goals
(rejected in ADR-0074 D4); a private goal scheduler instead of spool jobs;
shipping gates/subgoals now (upstream surface still moving: #98452, #143820).

## Invariants
No upstream patches. Owner grants are fail-closed. No secrets in DTOs, tool
results, argv or logs. Allowlist diagnostics only; unknown is `null`, never
fabricated. Provider mutations need concrete owner instruction. Mock tests are
not live acceptance evidence. No new dependencies without necessity.
