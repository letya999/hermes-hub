# #248 research: how analogs do source watchers

Surveyed for the watcher design (user asked for openclaw/noclaw/goclaw/iva +
devto/HN/substack/medium/x/habr). Sources that materially shaped the design:

- **OpenClaw** — "everything is a cron". One durable scheduler carries
  payload kinds; proactive jobs are just occurrences with different
  payloads, not a parallel watcher daemon. Taken: watchers/checks/digests
  are `job_kind`s on the existing occurrence path, driven by the same
  100 ms control loop (`tickProactive`), no second timer.
- **PraisonAI scheduler** — a `monitor` job hashes the source per tick;
  the model is invoked only on change; `no_change` is a first-class tick
  outcome distinct from `skipped`. Taken: occurrence states
  `no_change`/`skipped`/`failed` are durable and auditable, unchanged
  ticks cost zero tokens.
- **jarela (ADR-0027)** — poll tool → deterministic hash → fire only on
  diff; prev+current values are handed to the firing message; zero tokens
  between changes. Taken: delivery text carries `was:`/`now:` values; the
  durable WatchState keeps baseline, value, prev_value, changed_at.
- **changedetection.io** — the canonical shape: bounded fetch → extract
  via selector/regex → diff against a snapshot baseline → notify; first
  snapshot seeds silently. Taken: `probe{url,regex}` extracts the first
  capture group (whole bounded body when no regex), hash of the extracted
  value is compared — never the raw page; first tick seeds the baseline
  with no notification.
- **Ankita dev.to writeup (RoutineStore)** — regex/CSS extraction →
  digest; cache-disabled fetch; shared check function; atomic store;
  re-read before mutate. Taken: `Cache-Control: no-cache` on probes;
  settle re-reads the occurrence under the lock before writing; watch
  state written atomically together with the occurrence.
- **AgentMonitors** — stateful source plugins where the runtime owns
  diffing. Taken: the hub owns state and diffing; a probe is a generic
  HTTP+regex spec, not one implementation per provider — no guessed
  provider APIs.
- **IMTI / denyx Starlark notes** — Starlark as the bounded script
  language: `SetMaxExecutionSteps`, context cancel via `thread.Cancel`,
  host-declared bindings only; no fs/net/random/clock by language rule.
  Taken: `kind=script` runs embedded Starlark with `time_now()` +
  `emit()` as the only injected globals; this is the runner that unblocks
  the phase-A `blocked` stub.

## Consequences baked into the implementation

- Poll cheaply, think rarely: probing is deterministic; the model is never
  invoked on unchanged ticks and never invoked at all by watcher/check/
  digest/script kinds.
- Creation-time baseline for `check`: a conditional follow-up needs the
  world-as-it-was when the user asked, so `POST /v1/routines` probes once
  and seeds; a dead source yields 502 and no schedule.
- Failure is an outcome, not a notification: probe errors settle
  `failed`; unmet check predicates settle `skipped`; nothing delivers
  blindly.
- Digest aggregates, doesn't fan out: one message listing changes since
  the last digest marker, scoped to the owner, marker advances at
  dispatch.
- Sandboxing is language-level, not a container: for scheduled scripts
  Starlark gives stronger, cheaper isolation than spawning anything —
  appropriate for trusted owner-authored routines; untrusted multi-tenant
  script execution would need a process sandbox instead.
