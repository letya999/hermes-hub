# CHG-0310: M30 — reliable multi-user agent execution

Owner instruction: implement GitHub milestone 30 (issues #397–#400) per the
revised plan — four targeted fixes to existing state owners, no new contracts.

Design stance (locked after FSM audit + competitor study): the machinery this
milestone needs already exists — `api.RunAuthority` supervisor lookup, the
durable `audit.Ledger` unsettled-correlation fence, `ErrBeforeEffect` /
`ReverifyEffective` / `afterAdmission` two-phase dispatch, the supervisor
phase table with separate `Unconfirmed`, and per-space `relayMeter` counters.
An earlier draft proposed `AdmittedOperation`, evidence-strength fields, a new
outcome FSM, a separate reservation ledger and a durable recovery queue — all
rejected as parallel architecture.

## Slices

### S1 #400 — atomic conservative budgets

- `internal/communication/budget.go`: new `scopeBudget.UnknownTokens`;
  unknown settlement (metered but unmeasured) carries `res.Charged` into it;
  daily admission expression adds it; UTC rollover clears it.
- `internal/runtime/model_meter.go`: `load`/`save` return errors; new
  `admit` does load → check → reserve → persist under one lock, fail-closed
  on every failure; `exhausted` stays as cheap pre-skip.
- `internal/runtime/model_relay.go`: per-attempt `admit` immediately before
  `bounded.Do`; post-dispatch `charge` removed (double count).
- Tests: `TestBudgetUnknownTokensHoldDailyCapacity` (20k cap, 15k unknown,
  10k refused incl. reopen, day rollover frees), `TestRelayMeterAdmit*` —
  atomicity under `requests=1`, per-attempt token charging, corrupt/unwritable
  fail-closed.

### S2 #397 — verified run correlation

- `internal/toolhub/memory_run.go`: extract `lookupLiveRun` from
  `requireLiveMemoryRun` — record exists, live status, current generation,
  principal/context/runtime/environment from the durable record equal the
  authenticated envelope; assignment wait retained. Memory path keeps its
  `MayUseOwnerMemory` classification on top; `liveRunConversation` reuses it.
- `internal/toolhub/api`: run-scope marker (`WithRunScope`/`RunScoped`) and
  `CallCorrelation.Scoped`; gateway stamps an internal marker header on
  `/mcp-run` (and strips it on `/mcp`) so the detached MCP tool context knows
  the endpoint is run-scoped.
- `internal/toolhub/gateway_call.go`: `corr.Scoped` requires both IDs; any
  non-empty correlation verifies via `lookupLiveRun` before `chargeRunCall`.
- Tests: verified vs forged vs aliased vs stale vs missing matrix.

### S3 #398 — stable operation identity, cleanup outcome, operator egress

- `internal/toolhub`: for `WriteEffect` tools `fields["correlation_id"]` =
  sha256 digest of principal/context/env/job/run/binding/tool/canonical args;
  reads keep empty correlation (no self-fence). Ledger writer stops
  overwriting `CorrelationID` with `OperationID` — `trackOperation`'s
  unsettled-correlation check becomes the fence (unsettled only; completed
  operations allow identical retries).
- `internal/toolhub/api/backend_remote.go` + `api.BackendResult.CleanupErr`:
  `CallTool` success + `Close` failure returns the result with a bounded
  cleanup detail; audit records `execution=succeeded` + cleanup-error, never
  `uncertain`, never replay.
- `cmd/hubctl/audit.go`: `hubctl audit pending` lists unsettled operations
  (allowlisted fields), `hubctl audit resolve` appends the completion event
  for a pending operation with declared outcome + bounded evidence reference.
- Tests: retry-new-ToolCallID dedup, changed args → new operation, completed
  → admitted, restart + parallel fence, close-error classification,
  pending/resolve round-trip.

### S4 #399 — post-admission dispatch + bounded recovery fan-out

- `internal/toolhub/gateway_call.go`: the default branch of
  `AuthorizeProjectedCall` defers to `afterAdmission` with
  Reload + `ReverifyEffective` + execute (same shape as the existing
  connectors/channels/handlers branches) — no backend call under the
  registry lock.
- `internal/supervisor/reconcile.go`: `RecoverMissingWork` processes runtime
  keys through a small bounded worker pool (per-key `recovery:<key>` locks
  unchanged); `reconcileUnconfirmed` submits through the same bound.
- Tests: slow-backend vs unrelated call + revoke (channel/barrier
  synchronization), reverify on policy change, hung vs healthy runtime
  recovery, worker cap, per-key serialization.

## Non-goals

`AdmittedOperation`, evidence taxonomy, new outcome FSM, second ledger,
durable queue, snapshot-read framework, pricing feed/invoice settlement,
per-job relay correlation, merged ledgers, exactly-once/HA claims.

## Validation

Per-slice focused tests + `go vet -p=2` on touched packages; final
`just check` + affected package tests. CI owns race/shuffle/coverage.
