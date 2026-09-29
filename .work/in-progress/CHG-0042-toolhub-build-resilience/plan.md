# CHG-0042 — ToolHub build resilience

Incident: a self-install (`prepare_source`) failed when a second buildkitd hit
the shared-state lock; the onboarding record did not exist until after build
(`status` → "record not found"); builder/proxy/volumes leaked silently.

## Changes

- `internal/toolhub/artifact_build.go`
  - `restrictedBuildMu` serializes restricted builds process-wide (the shared
    buildkit state volume cannot host two daemons anyway).
  - `reapStaleBuildResources` at build start removes leftover builder/proxy/
    seed containers, build networks and per-build volumes (skips the shared
    state volume) — a stale builder would hold buildkitd.lock forever.
  - Cleanup defer logs removal errors instead of swallowing them; build start
    is logged.
- `internal/toolhub/control.go`
  - `prepareSelfInstall` writes a `preparing` onboarding record before
    review/build so `status` and request_key dedupe work mid-flight; failures
    leave a durable `failed` record. Superseded stubs (post-bump key drift)
    are removed.
  - request_key dedupe re-prepares when the stored record is `failed`/`removed`
    instead of returning it forever.

## Deferred (reported, not fixed here)

- MCP streamable-http result delivery when the client stream flaps mid-build
  ("Connection closed" after ~400s while build continued). Needs a response
  buffer/resume design at the MCP layer.
- Container churn: observed restarts were debugging-era kills plus the 5min
  warm-ttl idle reap, not a defect on its own.
