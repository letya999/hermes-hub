---
description: v1 execution - spawn-controller as the single container authority with instant space creation, task-engine with one job ledger, lanes and execution classes, harness-runtime isolation with pod-link and an instance pool, browser-runtime tab leases.
last_verified: 2026-11-19
---
# ADR-0107: Execution - spawn-controller, task-engine, runtimes

Status: accepted direction (M40, owner issue #572). Date: 2026-11-19.
Delivery: CHG-0333 (spawn-controller), CHG-0334 (instant spaces), CHG-0335
(task-engine), CHG-0336 (harness-runtime), CHG-0351 (browser-runtime).
Supersedes: ADR-0040 (single-stack supervisor). Partly supersedes: ADR-0090
(lane key and fairness are kept; their owner moves from the gateway spool to
task-engine). Keeps: ADR-0014 (scale-to-zero), ADR-0036 (run budget,
uncertain reconciliation), ADR-0101..ADR-0104 (backends).

## Context

- Container authority is split: the supervisor spawns runtimes, ToolHub's
  workload controller spawns cells (`generic_*.go`, `cell_*.go`), and
  `hubctl`/`space apply` renders Compose for per-space services. Three
  writers reconcile overlapping Docker objects by label.
- A new space needs a full render of Compose plans and generated files
  before its first message can run; documented cold start is 60-80 s, warm
  lease ~0.6 s (docs/runtime-operations.md), against the SPEC-0053 S3 target
  of p95 <= 25 s from `waking` to `admitted`.
- Four job ledgers exist (ADR-0105 context). Each implements its own claim,
  retry and uncertain handling; a job can be "done" in one and "pending" in
  another.
- `hermes-runtime` contains Hermes plus egress, ToolHub and model relays, so
  the untrusted container also hosts boundary code.

## Decisions

1. **spawn-controller is the single container authority.** It owns
   runtimes, sidecars and workers on every backend (Docker default,
   Kubernetes, Firecracker, sandbox runtimes). Other modules request
   workloads through the spawn contract; none holds Docker or cluster
   credentials. It also owns passport publication (bundle activation,
   ADR-0106 D6) because publication decides what it spawns.
2. **Instant space creation.** A space is created from an identity (from
   identity-hub) plus six passports instantiated from versioned templates.
   Creation writes the passports, publishes a signed bundle and renders only
   the space runtime container spec; it renders no Compose project and
   touches no other space. Shared services are not re-rendered per space.
3. **Pod-link and instance pool.** A *pod* is the unit spawn-controller
   creates and destroys together: one harness-runtime plus its optional
   browser-runtime, joined by a pod id and one space network. Images are
   pre-pulled at install and on catalog change; per-space networks and
   volumes are created at space creation. Backends that support late volume
   attach (Firecracker share disk, Kubernetes pre-provisioned pods) may keep
   an *instance pool* of started, unbound instances that are linked to a
   space at claim; the Docker backend cold-creates from pre-pulled images.
   A pooled instance never carries data of a previous space: it is destroyed,
   not returned, after use.
4. **Runtime lifecycle is one table.** spawn-controller owns it; no other
   writer changes runtime state.

   | From | Event | To |
   |---|---|---|
   | absent | claim (job admitted, lease) | starting |
   | starting | readiness probe passes | warm |
   | starting | budget exceeded or probe fails | failed |
   | warm | lease acquired | busy |
   | busy | last lease released | warm |
   | warm | idle TTL | draining |
   | draining | stop confirmed by inspect | absent |
   | any | stop unconfirmed | quarantined |
   | failed, quarantined | operator or reconciler confirmed removal | absent |

   A generation counter increments on every `starting`; it is the `gen` claim
   of run tokens (ADR-0106 D4), so a restart invalidates outstanding tokens.
5. **task-engine owns the one job ledger.** All jobs (channel messages,
   routine occurrences, assignment steps, research, system work) live in one
   ledger with one claim, lease, retry and reconciliation implementation.
   Jobs carry an execution class:

   | Class | Starts a runtime | Lane | Notes |
   |---|---|---|---|
   | interactive | yes | conversation | owner is waiting; highest priority |
   | assignment | yes | assignment | long-running, budgeted, preemptible |
   | routine | only for `brief`/`sink=agent` kinds | routine | deterministic kinds never wake a model |
   | system | no | system | watchdog, maintenance; system principal |

   Lanes keep ADR-0090: FIFO inside a lane key, parallel across keys,
   fairness per (principal, context), a per-context cap below the upstream
   limit. Interactive jobs preempt assignment jobs at turn boundaries, never
   mid-tool-call.
6. **Job lifecycle.**

   | From | Event | To |
   |---|---|---|
   | - | accepted with idempotency key | queued |
   | queued | lane free, rights current, lease acquired | admitted |
   | admitted | runtime confirms run start | running |
   | running | final result recorded | succeeded or failed |
   | running | owner decision needed (ADR-0109) | waiting_owner |
   | waiting_owner | decision recorded | queued |
   | waiting_owner | decision expired or denied | cancelled |
   | admitted, running | outcome unknown (lost ack, crash after effect) | uncertain |
   | uncertain | observation proves outcome | succeeded or failed |
   | queued, admitted | revoke, pause or cancel | cancelled |

   `uncertain` is never retried automatically; it is resolved by
   observation (`/v1/observe`, ADR-0036) or by the owner. A job that provably
   did not start may retry inside its budget.
7. **harness-runtime is only execution.** Hermes plus the private `/v1/*`
   job contract and the space mounts. Egress relay, ToolHub relay and model
   relay move to egress-gateway, tool-hub and model-gateway; the runtime
   reaches them only on its agent network with run tokens. Hermes tools are
   limited to passport grants (native toolsets included).
8. **browser-runtime shares logins with tab leases.** One browser-runtime
   per space keeps the space's browser profile (logins). A session receives
   a *tab lease*: `free -> leased(session, ttl) -> released | expired`. Only
   the lease holder can drive its tab; expiry closes the tab; profiles are
   never shared across spaces. This replaces the shared-browser default
   (#406).

## Boundary crossings

| Crossing | Allowed | Refused |
|---|---|---|
| any module -> spawn-controller | spawn/stop/restart requests through the spawn contract for an admitted job or an operator action | direct Docker/cluster calls from any other module |
| task-engine -> runtime | `/v1/jobs` with a signed run token | unsigned jobs, jobs for a space the token does not name |
| runtime -> runtime | nothing | any cross-space network path |
| session -> browser tab | the tab its lease names, while the lease lives | another session's tab, another space's profile |
| pooled instance -> space | one link for its whole life | reuse after unlink |

## Alternatives

- **Keep three container writers with label conventions.** Rejected:
  overlapping reconcilers already produced orphan and finalizer incidents
  (ADR-0056).
- **Database-backed queue for the ledger.** Rejected: SQLite (ADR-0091) is
  the existing substrate; no new queue or database dependency.
- **Warm pool on Docker via late bind mounts.** Rejected: Docker cannot add
  mounts to a started container; faking it would share volumes across
  spaces.

## Preserved v0 invariants

- Default-deny: spawn renders only granted capabilities into a pod.
- Fail-closed passports: no signed bundle, no spawn.
- Uncertain outcomes are never retried (D6).
- Credentials never enter the model plane: pods receive references; values
  are injected at egress-gateway or delivered by the broker to workers.
- Scale-to-zero: pods drain on idle TTL; homes persist.
