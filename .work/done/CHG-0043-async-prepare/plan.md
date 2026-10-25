# CHG-0043 — Asynchronous prepare_source

Incident follow-up to CHG-0042: a slow or lost HTTP response discarded the
review+build result — the agent saw "Connection closed" after ~400s while
ToolHub kept working. MCP's own direction (SEP-1686 tasks, SEP-2575 stateless)
is the same shape: the call returns a handle; the outcome lives in durable
state and is read back.

## Changes

- `internal/toolhub/control.go`
  - `prepareSelfInstall` split into `resolveSelfInstallSource`,
    `writePreparingOnboarding`, `finishSelfInstall`.
  - `prepareSelfInstallAsync` (used by `prepare_source`) waits
    `PrepareSyncWindow` (default 90s, negative = fully synchronous) for
    review+build, then returns the durable `preparing` record; the work
    continues on a detached 35-minute context.
  - `status` bodies now carry `next_action` for `preparing` (poll) and
    `failed` (retry with same request_key) phases.
  - `PrepareDone` hook fires after a backgrounded prepare settles;
    `latestPrepareRecord` resolves the real onboarding when a patch bump moved
    the result off the stub id.
  - `ensurePreparing` dedupes an identical in-flight prepare (also for keyless
    retries via the deterministic stub id) and overwrites stubs older than the
    background deadline, which belong to a crashed prepare.
  - `FindOnboardingByKey` prefers a live record over a removed stub sharing the
    request_key (patch-bump sibling).
  - Build completion/failure is logged with onboarding id and phase.
- `internal/toolhub/gateway.go` — `notifyPrepareDone` sends a best-effort
  `notifications/message` to the owner's open MCP sessions (no-op for clients
  that never set a log level; `status` remains authoritative).
- `internal/toolhub/server.go` — wires `control.PrepareDone`.
- `internal/toolhub/control_http.go` — prepare_source description documents the
  preparing/poll-status contract.
- `internal/toolhub/control_async_test.go` — async window, durable failure,
  no-duplicate-retry, wake hook regression tests.

## Deferred / rejected

- Full MCP tasks primitive (SEP-1686): maps cleanly onto onboarding_id later.
- SSE resumability / bigger client timeouts: explicitly rejected by SEP-2575.
- Context churn: per CHG-0042 the observed restarts were warm-ttl idle reap
  plus debugging kills, not a defect.
