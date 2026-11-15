# M32 evidence, 2026-11-04

Read-only code audit before implementation. Paths are code anchors, not live
user data.

- `cmd/hubctl/space_apply.go:runSpaceApply` calls
  `migration.CompileSources`; `internal/migration/sources.go` reads five
  `passport-src` layers. `internal/migration/passport.go:grantLedger`
  derives grants from tool selections. `internal/stack/space.go` still
  opens `agent.yaml` in managed reads and supports legacy reads/writes.
- `internal/communication/gateway.go` mounts one vault and owns both
  proactive and nightly tick loops. `internal/communication/nightjob.go`
  chooses the first enabled user. `memory_capture.go` captures only that
  user's turns and updates shared `runtime/run-scope/current` with an
  unprotected read/modify/write sequence and shared temporary filename.
- `internal/agenttools/memory_card.go:runCardAuth` trusts any live owner
  run in that shared file. `memory_search.go` reads the same file.
  `internal/stack/mount_plan.go` and Compose give runtime vault RW access.
- `internal/routines/engine.go` already contains the domain engine, but
  its injected lock, durable record IO, job queue and delivery queue come
  from the communication spool (`internal/communication/spool.go`).
  `internal/communication/occurrences.go` ticks and dispatches it.
- `internal/catalog/rights.go` has memory read/search/remember only.
  `tools.select` conflates invoke control with tool-surface policy.
  `internal/agenttools/capability.go` catalogs memory tools, but managed
  execution still routes through runtime.

The existing CHG-0281 draft assumed a new `memoryd` and a shared run-scope
file. Owner review replaced both with ToolHub ownership and exact-run
attestation. CHG-0282 is already used by an unrelated change.
