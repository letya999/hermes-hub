# SPEC-0048: Proactive routine kinds

Frozen: 2026-10-10. Issues 247/248/249/250/251 — deterministic proactive
behavior on top of SPEC-0013 occurrences. The LLM is invoked only where a
human-facing text must be composed; every tick, poll and predicate below is
deterministic and durable.

## Bounded scripts (job_kind=script)

1. Script source is the schedule input, compiled by embedded Starlark
   (`go.starlark.net`, pure Go). There is no general shell. Starlark's
   language rules supply the sandbox: no filesystem, network, randomness or
   ambient clock exists unless the host injects it — the runner injects only
   `time_now()` (the occurrence's due time, keeping runs replayable) and
   `emit(text)`.
2. Bounds: source ≤ 16 KiB, ≤ 1,000,000 execution steps via
   `SetMaxExecutionSteps`, ≤ 4 emits, ≤ 4 KiB per emit. Context cancellation
   reaches the interpreter through `thread.Cancel`.
3. Each `emit` text becomes one durable outbox Delivery at fire time. A
   script occurrence never enqueues an agent job, never wakes a runtime,
   never calls a model. A failed run settles the occurrence `failed`; it
   never falls back to an agent run.
4. Syntax is validated at creation (`routine_create` and the hub both
   reject non-compiling source) so a dead script never becomes a schedule.

## Watchers (job_kind=watcher, #248)

1. The schedule carries `probe {url, regex}`: a plain http(s) URL (no
   credentials, no other schemes) and an optional regex whose first capture
   group is the compared value. Without a regex the whole bounded body is
   compared. Malformed specs are rejected at creation.
2. Dispatch is two-phase. `DispatchDueOccurrences` moves due watcher
   occurrences to durable `probing`; `ProbeDueOccurrences` performs the
   fetch outside the spool lock and settles each occurrence under the lock.
   The gateway control loop runs the whole chain in `tickProactive` — no
   separate watcher daemon.
3. The live fetch is bounded: one GET, `Cache-Control: no-cache`, 2xx only,
   body capped at 1 MiB, 10 s timeout. It never touches the model.
4. Durable per-schedule state (`spool/watchers/<id>.json`): baseline hash,
   current value, previous value, changed_at. The first successful probe
   seeds the baseline silently. An equal hash settles `no_change` at zero
   token cost. A different hash updates prev/current and enqueues one
   Delivery carrying the schedule text and was/now values. A probe error
   settles `failed` without notifying and without touching the baseline.
5. A crash between probing and settling is safe: a `probing` occurrence is
   re-probed, and delivery ids (`watch-<job id>`) dedupe repeats.

## Conditional follow-ups (job_kind=check, #251)

1. Same probe contract as watcher plus `expect` ∈ {changed, unchanged}.
   Invalid expectations, missing probes, non-http URLs and malformed regex
   are rejected at creation.
2. The baseline is anchored at creation: `POST /v1/routines` probes the
   source once and seeds `watchers/<id>` before persisting the schedule. An
   unreachable source yields 502 and no schedule — a follow-up that could
   never evaluate must not exist.
3. At fire time the occurrence probes like a watcher, then compares: hash
   equals baseline vs `expect`. An unmet predicate settles `skipped`
   (durable, auditable — not silent); a met predicate delivers the text and
   settles `dispatched`. A probe error settles `failed`. No baseline means
   the predicate cannot evaluate — it never delivers blindly.

## Digest (job_kind=digest, #250)

1. At fire time the occurrence scans the owner's watch states, collects
   those whose `changed_at` is after the schedule's last-digest marker
   (`spool/digests/<id>.json`), formats one deterministic message
   ("<title>" + one "• <watch name>: <value>" line per change) and
   enqueues a single outbox Delivery — not one notification per watcher.
2. The marker advances to the dispatch time, so repeats report only new
   changes. Owner scoping matches the routine principal+context; foreign
   watches are never aggregated. Zero LLM.

## Read-only knowledge sources (#249)

1. Knowledge is operator-staged per-user files under
   `spool/knowledge/<user>/` — documents, saved pages, connector exports.
   The hub serves them read-only through the control API: `GET
   /v1/knowledge` lists `{name,size,mod_time}` for the caller's own
   directory, `GET /v1/knowledge/<name>` returns the bytes plus
   `X-Hub-Name`. Foreign directories are unreachable by construction —
   the path derives from the authenticated principal, not a parameter.
   Path separators in names are rejected; non-regular files are never
   served. Writes are not part of the contract (GET only).
2. Agent surface: `knowledge_list` and `knowledge_read` (read effect in
   the ToolHub catalog). Content returns as tool output only — nothing is
   written into the workspace.

## Shared invariants

- All kinds keep SPEC-0013 guarantees: durable occurrence, idempotent
  dispatch, re-authorization at fire time (`authorizeOccurrence`), the
  one-hour catch-up cap and the 16-occurrences-per-pass bound.
- routine_create exposes kind plus optional `url`, `regex`, `expect`; the
  catalog description documents that watcher, check, script, reminder and
  digest never wake an agent.
