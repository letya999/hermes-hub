---
description: Frozen issue-sized contracts for per-user ToolHub memory.
last_verified: 2026-11-05
---
# SPEC-0098: M32 memory slices

Frozen: 2026-11-05. Implementation incomplete.
Decision: [ADR-0097](../../docs/adr/ADR-0097-m32-bounded-delivery.md).
Current worker contract for #513–#520; read assigned row plus invariants.
Trace: SPEC-0094 R1/R2/A1/A2/A5 and SPEC-0095 R2.3/R2.4.

## Invariants and inputs

User = agent = spaces/<user>. ToolHub resolves principal/environment from verified
service/runtime identity and server-owned enrollment; model arguments, URL paths
or arbitrary headers never select a vault. No new service or per-agent identity.
Vault storage, locks, work bounds and recovery are per user; dev/prod are separate.
ToolHub owns vault reads/writes; runtime and OpenKnowledge have no direct access.

Model calls require selected tool + tools.use + memory.enabled + operation right
and an exact live private run (principal, environment, runtime generation, job/run,
audience and lifecycle). System capture/night use verified service authority and
their own grants, not tools.use. Group/sensitive input never exposes private memory.
Recheck current enablement/rights before asynchronous commit. Missing/unverified
context fails closed before storage access. Existing memory formats, retention and
search citations stay; no rewrite of the storage model.

## Atomic assignments

| Issue | Deliverable and anchors | Acceptance / regression | Excluded |
|---|---|---|---|
| #513 M1 | EndpointConfigFromEnv/NewEndpointHandler consume server-controlled vault bindings already rendered by stack. Open existing vault implementation with per-user containment/lock; production startup wiring. | Two synthetic users/dev-prod map independently; unknown identity, duplicate/unsafe binding, symlink/path escape fail before open; one user's lock does not stall the other. Test startup → resolver → real temporary vault. | No public memory route, capture, exact-run implementation or mount-only claim of success. |
| #514 M2 | Verified exact-run admission used by ToolHub; typed trusted context from lifecycle authority. Reuse identity/runtime contracts; do not trust principal-only token or shared current file. | Concurrent owner/group runs, wrong generation/env/user, ended/expired/replayed run cannot borrow authorization. Source of run identity is authenticated end to end. | Vault backend or moving capture. |
| #515 M3 | memory_search reaches M1 backend after M2 admission; replace agenttools local execution. | Real temp vault search returns existing citation format; direct/indirect calls share denial path; disabled memory never opens vault. | Capture, remember and edits. |
| #516 M4 | Durable accepted-turn/completed-run facts from communication spool → authenticated ToolHub capture. Persist before transport ack; event ID, identity/run/audience/provenance/sensitivity/time and bounded content. | Lost ack/restart/retry captures once; forged/mismatched/late fact rejected or safely classified against exact historical lifecycle; disabled memory before commit prevents write. No claim that capture requires a still-live completed run. | Recall, nightly job and model-confirmed facts. |
| #517 M5 | CORE/recall use ToolHub and exact-run admission; remove every shared run-scope/current reader/writer. | Parallel private/group runs never share private context; unavailable ToolHub gives explicit unavailable result, no local vault fallback. | New memory format. |
| #518 M6 | remember/card writes through the sole ToolHub writer. | Current right/user checked; retry-safe real-vault write, concurrent users isolated; revoke/disable before commit leaves no write. | Editing/deleting existing memory without #519. |
| #519 M7 | Persist edit proposal; verified owner confirmation binds user/target/exact diff/base revision/expiry/single-use nonce. | Model/self-confirm, copied ID, another user, stale base, expiry and replay denied; commit rechecks rights and consumes confirmation atomically with mutation/recovery. | New approval service or prompt-based consent. |
| #520 M8 | Govern OpenKnowledge read/write paths through ToolHub; remove direct mounts and bypass routes. | No direct vault access; read/write identity/grants and confirmation equivalent to ordinary memory APIs; unimplemented mutation explicitly unavailable. | New UI, broad knowledge features; do not close unrelated #422 automatically. |

M1 alone does not fix the currently broken runtime-local memory backend.
No public memory endpoint may open a vault before M2. M4 completed-run capture
uses durable verified lifecycle facts; M2 live-run rule applies to model access.

## Delivery gates

Allowlisted audit: actor, operation, target ID, run, outcome; no memory bodies/secrets.
Each tool contract change ships catalog/description/prompt/docs together and tests
rebound projection. Locale parity en/ru/kk/es, just check and applicable Docker gates.
#526 proves real two-user isolation, concurrent private/group runs, capture replay
and confirmed edit on the integrated system; temporary-vault tests alone do not.
