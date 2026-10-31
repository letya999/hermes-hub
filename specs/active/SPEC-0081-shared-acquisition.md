# SPEC-0081: Shared acquisition pipeline (sources, cursors, deterministic filters)

Frozen: 2026-10-27. Implements GitHub issue #332 (absorbs closed #336).
Decision: [ADR-0082](../../docs/adr/ADR-0082-standing-assignments.md) D5–D7.
Amends: SPEC-0054 (subscriptions), SPEC-0059 (probe pipeline).

## Requirements

### Stage 1 — acquisition pool and pull discipline (CHG-0179)

- R1.1: `Acquisition` (`spool/acquisitions/<key>.json`) records one physical
  pull source: `key` = sha256(boundary‖canonical probe) where boundary =
  principal‖context‖runtime‖policy_version and canonical probe =
  `{kind,url|tool+args}` (extract/filter/script/needs excluded — sharing is
  at fetched-body level). Cross-boundary keys can never collide.
- R1.2: Membership: schedule create adds `schedule_id` to `members`;
  delete removes it; the last member retiring deletes the record and any
  stored body. Membership lives on the schedule (`acquisition_key`) so a
  corrupt pool record can be rebuilt from schedules.
- R1.3: Reuse: when a member's probe tick finds `body_at` fresher than the
  member's own interval hint (schedule expression spacing recorded at
  create, `interval_sec`), it consumes the stored snapshot (`shared_hits++`)
  without a fetch; otherwise it fetches, stores bounded body (≤128 KiB —
  larger bodies are fetched but never shared), `etag`, `last_modified` and
  `fetches++`. Each consumer still runs its own extract/filter/dedup and
  its own authorization — sharing is bytes, never verdicts.
- R1.4: Conditional HTTP: pulls send `If-None-Match`/`If-Modified-Since`
  from the stored acquisition headers; `304` settles the tick as
  `no_change` without extract or model work; `429`/503 `Retry-After`
  (seconds or HTTP-date, ≤1 h clamp) and consecutive transport failures
  raise `backoff_until` on the watch state (exp ×2, cap 15 min) — due
  occurrences before it skip cheaply and mark `retry_after`.
- R1.5: Due-time jitter: occurrence `due_at` for watcher/check/event kinds
  gains a deterministic offset derived from `schedule_id` hash, ±5% of
  `interval_sec` (cap 60 s) — stable per schedule so cadence never drifts.
- R1.6: Cursors: an `mcp_tool` event envelope key `cursor`/`next_cursor`
  persists into `EventState.cursor`; `cursor_expired:true` or an auth-class
  error sets `EventState.gap_since` + one deduped owner notice; next
  success clears it. `gap_since` is shown in routine_get / status cards.
- R1.7: Hook intake: `probe.kind=hook` event subscriptions mint a 128-bit
  token at create (sha-256 stored, raw token returned once);
  `POST /v1/hooks/{schedule_id}` requires constant-time bearer match,
  bounded body (256 KiB), parses `{events:[...]}|[]|{...}` with the same
  extractor, then filter→seen-set→fanout. Wrong/absent token or unknown
  id ⇒ 404-class refusal; paused/cancelled ⇒ 410-class refusal.
- R1.8: Poll bounds unchanged globally: existing interval minimums and
  `max_retries` bounds still apply; sub-minute `every:` stays rejected as
  before. No global routine limit is weakened.

### Stage 2 — deterministic event filters (CHG-0179)

- R2.1: `probe.filter` (event schedules only) is Starlark source ≤8 KiB,
  syntax-checked at create, executed per extracted event in the isolated
  runtime with `input()` = the event JSON (id/text/fields); builtins:
  `emit(keep|drop|urgent[, reason])`, `time_now()`; `needs()` prefetch of
  ≤2 declared read tools allowed. Runtime unavailable ⇒ filter outcome
  `keep` is *not* assumed: the event is delivered and the failure is
  recorded on `EventState.last_error` (fail-open for delivery, never for
  authorization).
- R2.2: Verdicts: `drop` ⇒ mark seen, no delivery, counted
  (`filtered_out`); `keep` ⇒ normal path; `urgent` ⇒ `not_before=now`
  ignoring quiet window, daily cap still applies, text prefixed `[urgent]`.
  `sink=agent` urgent wakes ignore quiet defer identically.
- R2.3: Filter identity participates in the seen-set scope: changing the
  filter bumps `probe_revision`, resetting the seen-set — already-free via
  the existing `ProbeRevision` gate on `EventState`.
- R2.4: Watcher (`check`/`watcher`) deterministic filtering stays the
  existing expect/extract/baseline pipeline — `filter` is rejected there.

### Stage 3 — live acceptance (CHG-0181)

- R3.1: Multi-source live run: ≥3 real HTTP endpoints (public API + local
  fixture) + 1 Prometheus-style query endpoint + 1 file/command source +
  1 MCP-tool source if a connector is configured; mixed intervals; evidence
  of shared fetches and conditional pulls in counters/logs.
- R3.2: Restart mid-poll ⇒ no replayed effects, no lost decided events;
  kill-during-delivery ⇒ uncertain/verified states stay honest.
- R3.3: Load check: 1/10/50 idle subscriptions tick overhead measured on
  the dev host (docker stats + loop counters) — recorded, not asserted.

## Acceptance

- A1.1 `TestAcquisitionSharesWithinBoundary`: two subscriptions, same
  probe, same owner ⇒ one fetch, two consumers; second tick reuses.
- A1.2 `TestAcquisitionNeverCrossesBoundary`: same probe, different
  principal ⇒ two acquisitions, two fetches.
- A1.3 `TestConditionalHTTP304`: ETag round-trip ⇒ 304 ⇒ `no_change`,
  zero extract calls.
- A1.4 `TestRetryAfterBackoff`: 429+Retry-After ⇒ `backoff_until` set;
  due ticks skip until expiry.
- A1.5 `TestCursorAndGap`: envelope cursor persists; `cursor_expired` ⇒
  `gap_since` + one owner notice; recovery clears.
- A1.6 `TestHookIntake`: tokenless/wrong-token ⇒ refused; valid ⇒ events
  dedupe through seen-set; replayed batch is a no-op.
- A2.1 `TestEventFilterVerdicts`: keep/drop/urgent behave per R2.2 incl.
  urgent-quiet interplay and daily-cap enforcement.
- A2.2 `TestFilterRevisionResetsSeen`: filter edit ⇒ new probe revision ⇒
  old events re-evaluate once.
- A3.1 Live evidence in CHG-0181 `state.yaml` (shared-hit counters,
  304s observed, gap surfaced, restart receipts).
