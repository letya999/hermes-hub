# CHG-0081: M10 Phase B — proactive assistant

Worktree `hermes-hub-m10a`, branch `feat/m10-phase-a` (continues from 028fb6e).
Issues: 248 (watchers), 251 (conditional follow-ups), 250 (morning digest),
249 (read-only knowledge sources). Research: `watcher-research.md`.

## Research conclusions

Studied: OpenClaw (`everything is a cron`, payload kinds, per-job pacing
bounds), PraisonAI (`monitor` job hashes source per tick, model only on
change, `no_change` status distinct from `skipped`, first tick seeds
baseline), jarela ADR-0027 (poll tool → hash → fire only on diff, hand
prev+current to the firing prompt, zero tokens between changes),
changedetection.io (fetch → extract via selector/regex → diff → notify;
baseline snapshot), Ankita dev.to writeup (regex/CSS extraction → digest,
cache-disabled fetch, shared check fn, atomic store, re-read before mutate),
AgentMonitors (stateful source plugins, runtime owns diffing), IMTI/denyx
(Starlark as the bounded script language: `SetMaxExecutionSteps` + ctx
cancel + host-declared bindings; no fs/net/random/clock by default).

## Design

- `job_kind=watcher`: schedule carries a `WatchProbe{URL, Regex}` spec.
  Dispatch is two-phase: occurrence → `probing` (durable), probe runs
  outside the spool lock with bounded HTTP fetch (timeout, size cap,
  no-cache), settle re-locks: per-schedule `watchers/<id>.json` state
  (baseline hash, last value, checked_at, changed_at, consecutive errors).
  First success seeds baseline silently; a changed hash enqueues one
  outbox Delivery (bounded old/new preview); `no_change` records the tick
  with zero tokens. Crash between probing and settle is safe — a probing
  occurrence is re-probed, delivery IDs dedupe.
- `job_kind=script`: real bounded runner = embedded Starlark
  (`go.starlark.net`, pure Go). Limits: script <= 16 KiB, execution steps
  cap, wall-clock ctx timeout, `emit(text)` capped; no fs/net/random —
  the script can only compute and emit. `emit` output becomes outbox
  Deliveries. Replaces the phase-A `blocked` stub; `routine_create` accepts
  `kind=script` again after a syntax dry-run at create time.
- `job_kind=check` (#251): probe spec + `expect` (`changed`/`unchanged`)
  with the baseline seeded at schedule creation; at fire, probe compares —
  met → deliver text, else `skipped`. Covers "remind me if the PR is still
  open by Friday".
- `job_kind=digest` (#250): at fire, collect the owner's watcher states
  changed since the last digest marker, format one deterministic message,
  deliver via outbox, stamp the marker. Zero LLM.
- `knowledge_list`/`knowledge_read` (#249): read-only tools over
  `spool/knowledge/<user>/` via control endpoints — per-principal scoping,
  GET-only, content never lands in the workspace.
- `routine_create` gains `url`/`regex`/`expect` args for watcher/check.

All new kinds keep SPEC-0013 invariants: durable occurrence, idempotent
dispatch, re-authorization at fire time, verified audience only.
