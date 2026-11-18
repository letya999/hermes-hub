# CHG-0312 — observability: status model, hubctl status, metrics/alerts, backup drill

Milestone: M9 — Enterprise Readiness. Issues: #209 → #201 → #199 → #200; #354 tail if budget remains.

One bounded contract, four commits, no new subsystems or dependencies.

## S1 — #209 legible status model (commit 1)

`internal/supervisor/status.go`: closed `Phase` enum for runtimes
(`pending|running|degraded|suspended|failed|stopped|terminating|unknown`)
and named `Condition{Name,OK,Reason,Since}` with a closed reason
vocabulary (`ready`, `ownership_verified`, `connectors`, `crash_budget`).
Derivation is a pure function over the persisted runtimeEntry/jobRecord —
the same fields the supervisor already persists, so status reads identical
to state. Unknown/missing inputs collapse to `Unknown`, never a guessed
string. Jobs reuse the existing phase machine; `uncertain` stays a flag
that surfaces as condition `outcome=uncertain`.

`transitions.jsonl` beside `signals.jsonl` (same 4 MiB rotation): one
allowlisted line per runtime phase/condition change and per job phase
change — the watch-able event stream #197 can tail. Dedup via in-memory
signature map so steady state emits nothing.

`Manager.Status()` returns the projection for the API/CLI surface (S2).

## S2 — #201 hubctl status (commit 2)

- Supervisor `GET /v1/status` (existing RuntimeAuth): watchdog metrics,
  runtimes with phase+conditions+crash budget+cpu/mem, active holds,
  job counts by phase + unconfirmed.
- Gateway `GET /v1/status` (authorizeControl): spool queue counts
  (pending/running/failed), outbox pending + oldest age + failed,
  named egress-down conditions, routines next-due/overdue via
  automationCall (reports `unavailable` when ToolHub is down).
- `hubctl status [--json]`: reads both endpoints for the space, human
  table + machine JSON. Auth reuses supervisor.auth + communication env
  conventions already used by hubctl.

## S3 — #199 metrics + owner alert (commit 3)

- `GET /v1/metrics` on both services, prometheus text format hand-rolled
  (bounded label sets — no dependency): runtime/job gauges by phase,
  crash-budget sum, watchdog sweeps; gateway queue/outbox gauges,
  egress_down per channel.
- Owner alert: gateway failure deliveries get a rolling-hour rate bound
  (cap, then one coalesced suppressed-count notice — localized, never
  content). Crash-budget consumption: controlLoop sweep polls supervisor
  /v1/status with the existing RuntimeAuth; a new consumption edge enqueues
  one deduped localized owner delivery per runtime+count.

## S4 — #200 backup schedule + restore drill (commit 4)

- Supervisor `backups.go`: per-space scheduled `contextlife.Backup` into
  `<space>/archive/backups/` (purge-owned subtree), env `HUB_BACKUP_EVERY`
  (default 24h) / `HUB_BACKUP_KEEP` (default 7) / `HUB_BACKUP_DRILL_EVERY`
  (default 7d, 0=off). State `backups.json` under StateDir:
  last_success/last_drill/drill_result per space — auditable, surfaced on
  /v1/status.
- Drill: restore latest into a scratch dir under StateDir, Verify +
  manifest-hash spot check, bounded; wrong-owner restore stays denied
  (Verify already rejects manifest user mismatch — negative test).
- `hubctl context restore-drill --user X` manual one-shot using the same
  routine; prints the drill record.

## S5 — #354 anomaly watchers (tail, only if budget)

Per-entry CPU/RAM sample ring; sustained-over-threshold N sweeps records
an incident class (existing recordIncidentLocked → personalWake path).
Zero-cost when no sampler.

## Verify

- Focused tests per slice + `go vet -p=2` touched packages + `just check`.
- Live: dev-rebuild supervisor + communication-hub, `hubctl status` human
  + JSON, forced job failure → owner alert visible, backup sweep forced →
  archive + drill record.
- state.yaml updated per commit.
