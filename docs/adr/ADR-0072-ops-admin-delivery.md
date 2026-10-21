---
description: Ops, admin and delivery milestone — usage export, declarative space manifests, image rollout pins, admin diagnostics principal, incident replay.
last_verified: 2026-10-22
---
# ADR-0072: Ops, admin & delivery plane

Status: Accepted (implements milestone "M17 — Ops, Admin & Delivery", issues
#141, #195, #197, #198, #206).

## Context

The hub's own operation had five gaps: usage data was journaled but only
reachable in-chat; `hubctl apply` converged routines and knowledge but not the
space's own documents; there was no live status surface; the agent image moved
only by rebuild with no verified rollback; onboarding forensics required a human
reading upstream source by hand; and a failed job could not be re-run in
isolation. All five are control-plane concerns on a single host, so they are
decided together to keep the seams consistent.

## Decision

D1 — **Usage export is a file read, not a service.** `hubctl usage` parses the
append-only `usage.jsonl` directly, tolerates the torn tail the writer
quarantines on its next append, and never opens the spool. Aggregation is
grouped by scope and by actor over an operator-chosen window. No new daemon,
index or endpoint.

D2 — **Space manifests extend the existing apply file.** A versioned manifest
may carry `space:` with `agent:` and `workspace:` subtrees that are complete
desired documents, validated by the same strict parsers the stack uses for
on-disk files. Everything in the manifest is validated before the first write;
each changed file lands atomically; a byte-equal re-apply reports the space as
already converged. Secret material stays in `secrets.<env>.env` and is rejected
in manifests.

D3 — **Watch polls real state, no event pipeline.** `hubctl watch` samples the
supervisor control API and/or the local spool once per second and prints
transitions only. Polling authoritative state is deliberately preferred over a
new subscription stream.

D4 — **Rollout pins are per-space operator files.** `generated/image-pins.json`
records `{current, previous, canary}` per environment. Spawn-time resolution is
dev overlay pin, then rollout pin, then the flag default — the same seam that
already resolves dev images. `promote` writes a pin only after the recorded
smoke gate passes and keeps the previous verified image; `revert` restores it
in one command and re-verifies; `canary` overrides one named context and is
surfaced in job status. An unsmoked image can only ever run as a marked
single-context canary, never as the pin.

D5 — **The admin diagnostics principal is a token scope, not a new identity
system.** The enrolled-tokens contract gains an optional `scopes` list;
`scopes: ["admin"]` on a `hermes-admin` bearer unlocks the read-only
`/v1/admin/*` surface (cross-principal onboarding reads, `diagnose`,
`inspect-source` over the pinned upstream commit). The surface is serialized
and rate-limited by construction, every call is audit-tagged with the admin
principal and target, it never returns secret values, it performs no provider
mutations, and recipe proposals are response payloads — never applied.

D6 — **Replay is an isolated gateway run, not a second runtime.** A bundle
exports the durable envelope, the prompt when it is durable (or supplied by the
operator for sensitive jobs), recorded versions and a redacted config digest.
`hubctl replay run` materializes a scratch space with no secrets, forces the
mock channel, and re-admits the job — against a stub runner or an
operator-supplied replay supervisor URL. Nothing in the bundle path can reach
Telegram, providers or credentials by construction.

## Alternatives

- A metering database or per-usage HTTP endpoint (D1): rejected — the journal
  is the source of truth and a file read cannot diverge from it.
- Field-level settings merge (D2): rejected — typed whole documents reuse the
  existing validators and keep convergence byte-exact.
- An event bus for watch (D3): rejected — one-second polling of authoritative
  endpoints is honest state within the required seconds.
- A registry-side image promotion service (D4): rejected — the supervisor
  already owns spawn-time image resolution; a pin file is the smallest state
  that survives restarts.
- Admin as a normal user with broad grants (D5): rejected — grants cannot
  express cross-principal read; a scope on the bearer does, and stays auditable.
- Full container replay on a clone stack (D6): deferred — the isolated gateway
  path covers admission, policy and capture; a docker-backed replay supervisor
  is the same `--runtime-url` seam when an operator needs the agent itself.

## Invariants kept

- No secret value ever crosses a read surface: usage rows are counters,
  manifests refuse secret keys, admin responses carry names/shapes only, replay
  bundles strip credentials.
- Provider mutations still require a concrete user instruction; nothing here
  issues one.
- The supervisor, spool and tokens files remain the single writers of their
  state; every new surface is read-only or converges through existing writers.

## Consequences

Operators get metering, convergence, liveness, rollback, forensics and replay
without a new service. The cost is six small seams (usage reader, manifest
section, watch poller, pin file, admin scope check, replay runner) and one
additive token-file field.
