# CHG-0323: Company-grade security operations (MVP M9)

Chain: operator request -> issues #210, #203, #59 -> this plan -> `state.yaml`.

## Goal

Close the final MVP M9 security-operations slice: policy-gated runtime introspection, bounded SIEM export of the audit ledger, and one audited principal-disable propagation path across ingress, jobs, runs, ToolHub, connectors, routines, OAuth grants and delivery.

## Read before coding

1. `README.md`, `docs/index.md`, `docs/services/hubctl.md`, `docs/services/communication.md`, `docs/services/toolhub.md`, `docs/services/supervisor.md`, `docs/operations.md`.
2. Issues #210, #203 and #59 in milestone 7.

## Preconditions and rules

- Reuse existing service-control bearers, ToolHub `ControlToken` and Broker signed control actors; no new trust boundary.
- Keep the coordinator synchronous and local to `hubctl`; no new distributed revocation FSM.
- Audit contains bounded operation metadata only — no tokens, prompts, bodies or arbitrary responses.
- Preserve shared team/context-owned connector authority while revoking one principal.
- `just check` and affected package tests run before handoff.

## Slices

### S1. Introspection gate and audit (#210)

Anchors: `internal/stack/scope.go`, `internal/stack/config.go`, `cmd/hubctl/introspection.go`, `cmd/hubctl/main.go`.

Change:
1. Add the published `runtime.introspection` scope and org ceiling.
2. Gate `hubctl exec` and `hubctl inspect` on the effective setting.
3. Record each invocation in the local audit ledger before denial or execution.

Tests:
- `TestSettingsIntrospectionScope`: published setting and org ceiling compose.
- `TestExecInspectDeniedAuditsInvocation`: denial is durable and bounded.

### S2. Bounded audit export (#203)

Anchors: `internal/audit/export.go`, `cmd/hubctl/audit_export.go`.

Change:
1. Add a bounded `ExportPage` over existing ledger segments.
2. Add an atomic sidecar cursor outside the ledger lock.
3. Add `hubctl audit export` with HTTPS/token guards, ordered at-least-once replay and audit-export events excluded from export.

Tests:
- `TestExportReplaysAfterSinkFailure`: retries resend the same ordered page.
- `TestExportDoesNotExportItself`: export-control events do not loop.
- HTTP contract tests for non-HTTPS, token and bounded bodies.

### S3. Principal revocation composition (#59)

Anchors: `internal/supervisor/containment.go`, `internal/communication/revocation.go`, `internal/toolhub/revocation.go`, `internal/toolhub/automation/host.go`, `services/credential-broker/broker/grants.go`, `cmd/hubctl/user.go`.

Change:
1. Let supervisor `hold` defer containment for the initial ingress fence.
2. Add durable bulk revocation for communication jobs/delivery.
3. Add ToolHub bulk revocation for authority/connectors and Broker delegation for OAuth grants.
4. Pause all routines for the exact principal through the existing automation control seam.
5. Add Broker bulk grant revocation and drop leases.
6. Coordinate `ingress → jobs → runs → ToolHub → connectors → cron → OAuth → delivery` from `hubctl user disable`, auditing each stage under one correlation id and stopping on the first failure.
7. Pin the revocation scope durably: standalone deliveries carry the exact `(owner_principal_id, owner_context_id)` pair, ToolHub's outbox and `/v1/automation/deliveries` move the context through the `X-Hub-Context` service header (never the body), and `Delivery.Automation` marks service-boundary records whose send-time re-checks and foreign job bindings differ from local ones.

Tests:
- `TestUserDisablePropagatesInOrderAndAuditsStages`: exact order and one correlation id.
- `TestUserDisableStopsOnFirstFailedStage`: no later stage is invoked after failure.
- Store and HTTP contract tests for authorization, malformed scope, idempotency and shared ownership.

## Red-team checklist

- Browser-origin or wrong bearer: rejected before mutation.
- Mismatched `X-Hub-Principal`/body principal or foreign context: rejected.
- Admitted/running jobs become `cancel_requested + uncertain`; evidence is not deleted.
- Sink failure leaves local ledger append working and causes ordered replay later.
- Repeating `user disable` is idempotent and records bounded failures/successes.

## Acceptance

- Local unit/HTTP tests prove policy denial, export replay, exact propagation order, stop-on-failure and Alice/Bob isolation.
- Dev-stack live acceptance recorded 2026-11-18 (see below).
- `just check` plus affected `just test` package runs pass locally; CI owns full race/security/Docker acceptance.

## Live acceptance (2026-11-18, shared dev stack)

Principal `m29-acc-03` (synthetic, enrolled temporarily) vs `artem_two` (Bob, untouched).

- `hubctl user disable` (run via `TestLiveUserDisable`): all 8 stages succeeded in exact order `ingress → jobs → runs → ToolHub → connectors → cron → OAuth → delivery`; live counts `oauth:{grants:3}`, `delivery:{deliveries:1}`, empty stages returned `{}` (idempotent).
- Ingress: `hold-m29-acc-03-m29-acc-03-1` active (epoch 1); the only active hold on the supervisor.
- Post-revoke: ToolHub `/v1/rights/decide` for m29 → `allow:false "principal outside this space"`; `artem_two` → `allow:true`.
- Spool evidence: pre-revoke seed `m29-live-seed-1` → `outbox/superseded`; post-revoke due delivery `m29-post-revoke-due` → `outbox/failed` (sendRight PDP fence); Bob seed `bob-artem-two-1` stayed `pending` until cleanup.
- Audit ledger: 18 events for the run — one correlation id, exact stage order, admitted+succeeded per stage, complete event last; no tokens/URLs in events.
- #210 live: `hubctl exec` on `spaces/artem_main` (personal scope, flag absent) → admitted + `introspection/exec` audit event `allow/admitted` (docker leg unreachable: no `hermes-runtime` service in the supervised dev stack — decision contract verified); group-scope fixture without flag → `Runtime introspection is denied by policy.` + `deny/policy` audit event at gate and CLI level.
- #203 live: `hubctl audit export --once` → 40 events delivered in ledger order to a loopback sink; cursor `after:40` persisted (destination stored as hash). Sink killed → local ledger appended events 41–42, export failed connection-refused, cursor stayed 40, `audit-export` degraded event recorded. Sink restarted → export delivered pending event 41, cursor advanced. `audit-export` self-events excluded by design.
- Two live-only bugs found and fixed during acceptance: legacy automation-outbox records without `owner_context_id` crash-looped ToolHub on re-discovery (`sameDeliveryIntent` + owner migration in `ports.go`) and `PauseAllForPrincipal` 500'd for principals with no automation storage (empty-root → zero schedules in `host.go`).
- Restore: hold released via `DELETE /v1/admin/holds` (epoch 2, inactive); m29 removed from `toolhub-tokens.json` (401 now) and `communication.users.yaml`; both test seeds superseded; `HUB_SUPERVISOR_ADMIN_AUTH` reverted to unset (admin holds surface back to 403 — it is an opt-in operator env `hubctl user disable` needs).
- Limitation: m29-acc-03 is synthetic — it had no live runtime/projection, so per-runtime teardown was not observable; the stage endpoints and PEP denials it drives were all exercised.

## Out of scope

- No new lifecycle state machine or distributed transaction journal.
- No Broker signing-key reads by `hubctl`.
- No deletion of evidence for uncertain jobs or completed provider calls.
- No removal of shared context-owned connector resources.

## Done checklist

- [x] Regression tests added for authorization, HTTP contracts, ordering and shared ownership.
- [x] `just check` and affected package tests green.
- [x] Docs updated.
- [x] Live dev-stack acceptance recorded.
- [ ] State moved to `.work/done/` after review.
