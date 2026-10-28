# SPEC-0083: Browser sidecar lifecycle and resource budget (M26 follow-up)

Frozen: 2026-10-28. Implements ADR-0083 amendment decisions D8/D9 (CHG-0190).
Extends [SPEC-0082](SPEC-0082-image-role-budgets.md) R4: the sidecar topology
is unchanged; this spec fixes its lifecycle and ceiling so a fleet of spaces
does not hold idle browser stacks resident.

## Requirements

### R1 — Resource ceiling on the browser sidecar

- R1.1: Rendered `browser-<user>` services carry `cpus: "2.0"`,
  `mem_limit: "3g"` and `pids_limit: 256` alongside the existing
  `shm_size: "1gb"` (shm usage counts against the memory limit).
- R1.2: Limits apply to managed and unmanaged renders identically.

### R2 — Auto-start on spawn

- R2.1: `Ensure` runs `docker start
  hermes-hub-<user>-<env>-browser-<user>-1` for every runtime spawn in a
  browser- or meet-enabled space, before the runtime's readiness is probed.
  `docker start` is idempotent: running containers are a no-op, stopped ones
  resume.
- R2.2: A failed start fails the spawn with the same reclaim/degraded
  semantics as a missing space network, and the error says to run
  `hubctl up` for the space.
- R2.3: The guarantee applies to unmanaged and managed spawns alike.

### R3 — Idle sleep

- R3.1: The sweep stops a running `browser-*` compose container whose
  `hermes-hub-*-<env>` project has no live runtime — no supervisor-tracked
  entry for the space in any non-Stopped state and no running
  `hermes-runtime` compose service — once it has been idle for
  `cfg.BrowserIdleTTL` (flag `--browser-idle-ttl`, default 15m).
- R3.2: Idle deadlines live in memory only; a supervisor restart resets them
  (one grace period of leniency, never a premature stop).
- R3.3: A returning runtime clears the deadline; the next spawn's
  `docker start` revives a slept sidecar transparently.
- R3.4: Containers outside `hermes-hub-<user>-<env>` projects are never
  touched; the reaper is best-effort — a failed `docker ps` skips the pass.
- R3.5: `docker stop` (SIGTERM, default grace) lets Chromium flush the
  profile; `restart: unless-stopped` does not resurrect a manual stop.

### R4 — Documented behaviour

- R4.1: operations.md records the sleep/wake model, the flag, and the
  expected healthcheck flap while a sidecar is down (container `healthcheck`
  probes CDP whenever `marker.browser` is set).
