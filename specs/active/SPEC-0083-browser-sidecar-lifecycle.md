# SPEC-0083: Browser sidecar lifecycle and resource budget (M26 follow-up)

Frozen: 2026-10-28. Implements ADR-0083 amendment decisions D8/D9 (CHG-0190).
Extends [SPEC-0082](SPEC-0082-image-role-budgets.md) R4: the sidecar topology
is unchanged; this spec fixes its lifecycle and ceiling so a fleet of spaces
does not hold idle browser stacks resident.

## Requirements

### R1 — Resource ceiling on the browser sidecar

- R1.1: Rendered `browser-<user>` services carry `cpus: "2.0"`,
  `mem_limit: "3g"` and `pids_limit: 512` alongside the existing
  `shm_size: "1gb"` (shm usage counts against the memory limit).
  *(errata: 256 was live-measured as insufficient — the persistent stack
  alone holds ~223 tasks and the SPEC-0082 R4.4 guest Chromium cannot
  launch; see ADR-0083 D9 addendum.)*
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

### R5 — Managed ToolHub route (2026-11-08 amendment)

- R5.1: Managed Hermes exposes only the ToolHub proxy, not the Playwright MCP
  sidecar as a model-selectable server. ToolHub admits the curated `browser_*`
  call and its private persistent tools-daemon connects to the existing
  owner-local Playwright sidecar on the agent network.
- R5.2: `browser_open` returns an owner-bound session ID; read, act, capture
  and close reject unknown, foreign, closed or revoked sessions and recheck
  the published user/guest and act grant on each operation.
- R5.3: A capture uses a fixed filename under the shared workspace/browser
  directory. The sidecar and its lifecycle/resource limits remain unchanged.
