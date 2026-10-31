# CHG-0190: browser sidecar idle lifecycle (M26 follow-up)

Chain: `docs/adr/ADR-0083-lean-images-capability-packs.md` amendment D8/D9 ->
`specs/active/SPEC-0083-browser-sidecar-lifecycle.md` -> this plan ->
`state.yaml`. Not a GitHub milestone issue; closes the 100-user audit gap
found after M26 delivery (sidecars were resident forever with no ceiling).

## Goal

Restore scale-to-zero economics for the browser stack: a sidecar runs only
while its space has a live runtime (plus grace), auto-starts on the next
spawn, and is capped like every other bounded workload. Compose keeps owning
the container shape; the supervisor only toggles its run state.

## Read before coding

1. `internal/stack/compose.go` (`browserService` ~L284-314, `common` L210,
   toolhub limits L509-511), `internal/supervisor/ensure.go` (spawn L287-346,
   net connect L311-325), `internal/supervisor/lease.go` (`Reap` L262+),
   `internal/supervisor/supervisor.go` (`sweep` L524, Config L99+),
   `cmd/supervisor/main.go` (flags ~L55, Config wiring ~L159).
2. Reference pattern: open-duet `idle_minutes` + `auto_start` + lease-aware
   upkeep; hermes-fleet `idle_timeout`; Browsermint idle auto-pause.

## Slices

- S1: compose — `cpus/mem_limit/pids_limit` on `browserService()` (R1).
- S2: supervisor — `ensureBrowserSidecar` on spawn for browser/meet spaces,
  unmanaged + managed; failure reclaims spawn (R2).
- S3: supervisor — `ReapIdleBrowserSidecars` in sweep: one `docker ps` of
  compose containers, idle deadline per sidecar container, `docker stop`
  after TTL, in-memory map, env-scoped project filter (R3).
- S4: config — `BrowserIdleTTL` Config field + `--browser-idle-ttl` flag
  (default 15m) (R3.1).
- S5: docs — operations.md sidecar lifecycle paragraph (R4).

## Test plan (red first)

- `internal/stack`: rendered `browser-<user>` carries the three limits,
  managed + unmanaged.
- `internal/supervisor`: browser-space spawn issues `docker start
  <project>-browser-<user>-1`; failed start aborts spawn and reclaims the
  runtime container; managed spawn starts it too.
- `internal/supervisor`: reaper — sidecar-only project idle past TTL gets
  `docker stop`; project with running `hermes-runtime` or live item skipped;
  non-`hermes-hub-*-env` project untouched; ps failure is a no-op.
- `go test ./internal/...` green; `just check`.
