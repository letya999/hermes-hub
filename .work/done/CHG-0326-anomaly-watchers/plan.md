# CHG-0326: Anomaly watchers over runtime metrics (MVP M9)

Chain: operator request -> issue #354 -> this plan -> `state.yaml`.

## Goal

A `watchers.metrics` policy entry opts a supervised runtime into metric
anomaly detection: the supervisor scrapes the runtime's bounded
`/v1/status`, applies sustained-breach thresholds with hysteresis, and
opens exactly one durable incident + personal-supervisor wake per
transition — using only stdlib and the counters the runtime already has.

## Design

- `internal/runtime` publishes `GET /v1/status` — allowlisted counters
  only (requests, 5xx, p95 latency over a fixed 256-sample ring, active
  runs, MCP inflight). Health/readiness/status endpoints are excluded
  from the measured stream; `statusRecorder.Unwrap()` preserves
  streaming flush.
- The supervisor scrapes on the health-sweep cadence bounded by the
  watcher's `every`; scrape failures are skipped observations, never
  incidents. Stopped/address-less/auth-less entries are skipped.
- State is one consecutive-breach counter per class per entry: breach →
  +1, inside the dead band → hold, below the low watermark → 0. At three
  consecutive breaches `recordIncidentLocked` fires **on the transition
  only** — no wake storm while the condition persists; a healthy window
  re-arms the transition.
- `protected` observes and counts breaches without opening incidents or
  waking personal supervision.
- Thresholds (docs/operations.md): cpu >90% of quota, mem >85% of cgroup
  limit (never fires limit-less), p95 >5s at ≥20 samples, 5xx >5% at ≥20
  new requests, active_runs ≥5, mcp_inflight ≥16 — all ×3 consecutive.
- `ContainerStat` gained `MemLimitBytes` by parsing the `used / limit`
  pair from Docker stats.

## Slices

### S1 Runtime counters

`internal/runtime/metrics.go` `runtimeMetrics`/`RuntimeStatus`;
`server.go` measured-middleware wiring; `mcp_proxy.go` inflight gauge.

### S2 Supervisor scrape + evaluation

`incidents.go` memory-limit parsing; `supervisor.go` entry state;
`metrics.go` `sampleRuntimeMetrics`/`evaluateRuntimeMetricsLocked`;
`reconcile.go` sweep hook.

### S3 Docs + tests

`docs/operations.md` watcher section + thresholds;
`docs/services/supervisor.md` incident classes; `metrics_test.go`
sweep-level and boundary coverage.

## Acceptance

- Sustained anomaly opens one incident on the third breach, refreshes
  without re-firing, and re-arms after a healthy window.
- Opt-in: no metrics key → zero scrapes; `protected` → zero incidents.
- Cadence honored (`every`); scrape failure never opens an incident.
- `just check` green; supervisor/runtime package tests pass.
