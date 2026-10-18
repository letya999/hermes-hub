---
description: Knowledge lives in runtime state — staged via hubctl apply --state, read in-process by agenttools, relocated offline via hubctl spool migrate.
last_verified: 2026-10-18
---
# ADR-0050: Knowledge in runtime state + spool migration

Status: Accepted 2026-10-18. Implemented as CHG-0121 (relocation) and
CHG-0126 (offline migration). Planned as ADR-0048 in the phase plan,
renumbered (that number went to the routines package).

## Context

Knowledge sources were staged under `<spool>/knowledge/<principal>[/context]`
and served to agent runtimes over an HTTP hop (`/v1/knowledge`) with a
control token — an extra network surface for content that is operator-staged
on the same host anyway.

## Decision

- Knowledge is a runtime-state concern: agenttools read
  `<state>/knowledge/<principal>[/context]` in-process via `os.Root`; no
  HTTP, no control token. `/v1/knowledge` is deleted.
- `hubctl apply` stages knowledge under `--state <dir>` and rejects specs
  carrying knowledge without it.
- Existing deployments keep their data via `hubctl spool migrate --spool
  <dir> --state <dir> [--apply]`: an offline command under `LockSpool` that
  copies the legacy tree into the new home. It copies rather than moves —
  a rolled-back old image still finds its sources; the operator deletes
  `spool/knowledge` manually once the release is proven. Conflicting
  targets are reported, never overwritten; the run is idempotent.

## Consequences

- Knowledge content no longer transits the gateway at all.
- The migration is the only bridge: no compatible read of the old path was
  kept in new code, so `spool migrate` must run before relying on
  previously staged sources in a new deployment.
- This is the single on-disk relocation of the comm-plane refactor; all
  other spool directories keep their paths, preserving phase-by-phase
  rollback.
