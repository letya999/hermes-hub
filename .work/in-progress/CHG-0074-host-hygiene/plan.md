# CHG-0074 — host hygiene batch (issues 226→222→224→223→221+225→171→46)

Ordered hardening batch from the 2026-10-05/07 audit issues.

## Scope

- **#226** one-off residue cleanup + `just docker-clean` sweeps aged
  harness/workload orphans (containers and dangling networks) by prefix.
- **#222** diagnostics collector scoped to owning compose project + user agent
  network; per-call deadlines + cycle budget; dead containers drained once;
  per-user output under `spaces/<user>/diagnostics/`; owner-filtered
  host-supervisor lines.
- **#224** supervisor child-exit error truth, clean SIGTERM on gateway
  restarts, ephemeral test port; cliproxy health-check and gateway
  message-text log noise.
- **#223** `/usage` must not stall the Telegram update poll.
- **#221** reduce docker socket surface for toolhub/workload-controller.
- **#225** decide credential-broker prod volume boundary in dev stack.
- **#171** configurable privacy-safe diagnostic logging.
- **#46** scale-to-zero capacity measurement (best-effort local evidence).

## Non-goals

- No broad docker system/volume prunes; other projects' resources untouched.
- work-* toolhive workload containers are not individually attributable to a
  stack; the collector intentionally excludes them (fail closed).
