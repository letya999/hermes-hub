---
description: Frozen v1 execution and provisioning - spawn-controller authority, instant space creation, runtime and job state machines, execution classes, harness isolation, browser tab leases, capacity and latency SLOs.
last_verified: "2026-11-20"
---
# SPEC-0101: v1 execution and provisioning SLOs

Frozen: 2026-11-19. Implementation: M40.3 (#601), M40.4 (#602), M40.6
(#604), M40.16 (#614, load report).

Scope note (2026-11-20, CHG-0334): the simplified v1 plan
([ADR-0107](../../docs/adr/ADR-0107-execution-spawn-task-runtime.md) as
aligned) partly supersedes this spec; [SPEC-0100](SPEC-0100-v1-contracts.md)
holds the acceptance per issue. Still in force: SLOs P1-P4 and P6, R1.2,
R1.4, R2.1-R2.3, R4.2, R4.5, R5.2, R7.1. Superseded: R1.1 and A1.1 wording
"hub-control" (read: spawn-controller is the only service with backend
credentials), R1.3 generation feed (generation is checked at task-engine,
ADR-0106 D6), R3 pods and pools (replaced by the warm pool keyed by image
digest, ADR-0107 D3), R4.1 classes (now `interactive|scheduled|subagent`
with `origin owner|unprompted`), R4.3-R4.4 lanes and preemption (now the
conversation queue `queue|interrupt`, ADR-0107 D7), R5.1 (relays leave the
runtime in M40.12/M40.13). Moved: P5 to M41 (attention), R6 browser tab
leases to M42. The CHG numbers this spec named before were a reserved
table that no longer exists.
Decision: [ADR-0107](../../docs/adr/ADR-0107-execution-spawn-task-runtime.md).
Trace: [SPEC-0100](SPEC-0100-v1-contracts.md) exit criteria EC2, EC4, EC5,
EC6. Keeps SPEC-0053 S1-S3 and SPEC-0089 lane rules unless a row below
tightens them.

## SLOs

Reference host: one Linux VPS, 8 vCPU, 16 GB RAM, SSD, Docker backend,
images pre-pulled. Measured over at least 50 samples per row.

| ID | Measure | Target | Source |
|---|---|---|---|
| P1 | Space creation: identity plus six template passports to "accepts first message" | p95 <= 3 s | spawn-controller `space_created` -> `space_ready` |
| P2 | First message on a new space to admitted run | p95 <= 20 s | task-engine `queued` -> `admitted` with runtime `starting` |
| P3 | Message to admitted run on a warm runtime | p95 <= 1 s | task-engine `queued` -> `admitted` |
| P4 | Simple text answer, warm runtime, with 3 interactive sessions and 20 active Assignments | p95 <= 25 s and <= 1.1 x the same measure with zero Assignments | `hubctl trace` received -> delivered |
| P5 | Event to owner notice for `interrupt` reports | push: p95 <= 60 s; poll: p95 <= poll interval + 60 s | inbox `ingested` -> attention `sent` |
| P6 | Idle pod stops after TTL | <= TTL + 60 s | runtime `draining` -> `absent` |

## R1 spawn-controller

- R1.1 spawn-controller is the only component with Docker, Kubernetes or
  Firecracker credentials; supervisor, ToolHub workload controller and
  per-space Compose rendering of runtimes are retired into it.
- R1.2 Runtime state follows the ADR-0107 D4 table; transitions outside it
  return an internal error and change nothing.
- R1.3 Each `starting` increments the space generation and publishes it on
  the generation feed used by run-token verifiers.
- R1.4 An unconfirmed stop quarantines that pod only.

## R2 Instant space creation

- R2.1 Creation input is an identity-hub principal plus the six passport
  templates; no hand-written files.
- R2.2 Creation renders only the space runtime container spec and the space
  network/volumes; no Compose project and no other space changes (render
  diff of other spaces is empty).
- R2.3 First message on a new space meets P2.

## R3 Pods and pools

- R3.1 A pod (harness-runtime plus optional browser-runtime) is created and
  destroyed as one unit.
- R3.2 Pooled instances (backends with late attach only) are destroyed after
  use, never re-linked.

## R4 task-engine

- R4.1 One ledger holds all job classes: interactive, assignment, routine,
  system.
- R4.2 Job state follows the ADR-0107 D6 table; `uncertain` is never retried
  automatically.
- R4.3 Lanes: FIFO per lane key, parallel across keys, fairness per
  (principal, context), per-context cap below the upstream limit.
- R4.4 Interactive jobs preempt assignment jobs at turn boundaries.
- R4.5 Idempotency key on accept; a duplicate accept returns the original
  job.

## R5 harness-runtime

- R5.1 The runtime image contains no egress, ToolHub or model relay code
  (`go list -deps ./cmd/runtime` has no relay packages).
- R5.2 Hermes tools available in a run equal the passport grants.

## R6 browser-runtime

- R6.1 One browser profile per space; tab leases per session with TTL.
- R6.2 A session cannot drive a tab it does not lease; expiry closes it.

## R7 Capacity

- R7.1 3 interactive sessions and 20 active Assignments run on the reference
  host while P4 holds.

## Acceptance

| ID | Check |
|---|---|
| AP1-AP6 | Load harness report with sample counts, p50/p95 and source SHA in the M40.16 CHG |
| A1.1 | Gate: no binary other than hub-control links a container backend client |
| A1.2 | Table-driven transition test with every illegal edge |
| A1.3 | Generation bump invalidates an outstanding run token (M40.14) |
| A2.1-A2.3 | Create-space integration test plus live receipt |
| A3.1-A3.2 | Pod create/destroy and pool non-reuse tests |
| A4.1-A4.5 | Ledger tests per class, transition table test, lane and preemption tests, duplicate accept test |
| A5.1-A5.2 | Dependency gate entry and a native-tools grant test |
| A6.1-A6.2 | Lease conflict and expiry tests |
| A7.1 | Same report as AP4 |
