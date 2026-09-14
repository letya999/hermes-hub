# CHG-0016: Scale-to-zero per-context Hermes runtimes

## Goal

Replace both one-shot `hermes -z` execution and the proposed permanently resident
per-user runtime with an isolated per-context Hermes Gateway that starts for work,
stays warm for five minutes by default and stops without losing state.

## Delivery slices

1. Freeze ADR-0014, SPEC-0012 and SPEC-0013; update affected issues and migration,
   architecture, operations and threat-model documentation.
2. Add a host-side Go supervisor with authenticated ensure/run/release/status calls,
   keyed start deduplication, exact context mounts and bounded Docker resources. **Done:**
   `internal/supervisor`, `hubctl supervisor` and opt-in Compose routing.
3. Start the pinned Hermes Gateway inside each runtime and use its authenticated
   `/api/sessions` + `/v1/runs` contract for execution. **Done:** persistent adapter
   with stable conversation-derived session IDs; `hermes -z` remains rollback-only.
4. Add leases, configurable idle TTL, global concurrency, safe reaping and
   reconciliation only for contexts with runnable work. **Done:** typed job/stream/
   approval/lifecycle/uncertain leases, authenticated lease API, warm reuse and safe
   idle reaping; durable reconciliation remains follow-up work.
5. Add a host-owned multi-user routing registry while keeping channel credentials out
   of runtimes and user/provider credentials out of the communication hub.
6. Add durable hub-owned routines that enqueue normal jobs and wake sleeping contexts;
   migrate native Hermes cron explicitly or keep its context pinned on.
7. Complete streaming, cancellation, approval and rollback integration, then remove
   the one-shot path only after one compatibility release and real runtime evidence.

## Verification

- Unit and race tests cover start/stop races, lease expiry, stale policy, cross-context
  mounts, cancellation, approval, duplicate occurrences and bounded catch-up.
- Docker tests run two isolated contexts, warm reuse, idle shutdown, cold restore and
  private networking against the pinned Hermes image.
- `just check` is required for every slice; `just security` is required for dependency
  or privilege-boundary changes; both Docker targets run when Docker is available. The
  initial supervisor slice passed all three gates on 2026-09-12.

## Non-goals

No Uno, Firecracker, Kubernetes, PostgreSQL, Redis, custom Hermes fork or general
orchestrator is introduced by this change.
