# CHG-0330: Enforce module boundaries with versioned contracts (M40 stage 2)

Chain: `docs/adr/ADR-0105-v1-topology.md` D4-D7 ->
`specs/active/SPEC-0100-v1-contracts.md` R1.3, R2, R3, R4.3 -> this plan ->
`state.yaml`. Issue: #574. Previous stage: CHG-0329. Next: CHG-0331.

## Goal

The contract pattern exists with three real contracts, the per-binary
dependency closure is gated in `just check`, nothing outside ToolHub opens
the ToolHub store, and `cmd/runtime`, `cmd/supervisor`, `cmd/communication`
no longer link the ToolHub server package. One `hub` binary serves a zone.

## Read before coding

1. ADR-0105, SPEC-0100 R1-R4, CHG-0328 `research.md` (v0 import edges).
2. `internal/toolhub/api/run_status.go` (`RunContext`, `RunAuthority`),
   `internal/supervisor/jobs.go` (`captureRunContext`, `/v1/jobs/<id>`).
3. `internal/agenttools/tools.go` `loadToolHub`, `tools_services.go`;
   `internal/stack/manifest.go` `migrateToolhubBindings`, `hosting.go`.

## Evidence (2026-11-19)

Edges into the ToolHub server package `internal/toolhub` from the three
binaries: `agenttools/tools.go` (`toolhub.Load`, `ReconnectController`),
`agenttools/tools_services.go` (`toolhub.CatalogEntry`), `stack/manifest.go`
(`toolhub.Load`, `MigrateProjection` at render), `stack/hosting.go`
(`toolhub.GenericControllerConfig`, `HostingProfile`). Communication also
links `internal/toolhub/automation`. After CHG-0329 the runtime never gets
`HUB_TOOLHUB_STORE`, so the agenttools store branch is dead code.

## Slices

### S2.1 Contract pattern and first contracts (R2.1-R2.5)

Anchors: new `contracts/{identity,task,toolsurface}/v1`.
Each package: DTOs, `Client` interface, `NewInProc(server)` (copies values
through JSON so aliasing bugs show up), `NewHTTPClient(base, token, client)`
and `Handler(server, token)`, typed errors (`ErrNotFound`, `ErrConflict`,
`ErrForbidden`, `ErrInvalid`, `ErrUnavailable`), and
`conformance.Run(t, newClient)`.
- `task/v1`: `RunRecord` (moved from `api.RunContext`), `Client.Run(ctx,
  jobID)`. Server: supervisor `/v1/jobs/<id>`; consumer: ToolHub
  `RunAuthority`.
- `identity/v1`: `Principal`, `ChannelIdentity`, `Client.Resolve(ctx,
  ChannelIdentity)`, `Client.Principal(ctx, id)`. Reference server over the
  verified user mapping (`communication.users.yaml` records); identity-hub
  replaces it in CHG-0331.
- `toolsurface/v1`: `CatalogEntry` (moved from `toolhub.CatalogEntry`),
  `Client.Catalog(ctx, envelope)`. Server: ToolHub store catalog.
Tests (red first): `TestContractConformanceInProcAndHTTP` in each contract
and in each owning module with its real server.

### S2.2 Dependency gate (R3.1-R3.3)

Anchors: `internal/devcheck` new `deps.go`, `cmd/devcheck` `deps` command,
`justfile` `check`.
Change: `go list -deps` per `cmd/*`; hard forbidden rules (runtime,
supervisor, communication must not reach `internal/toolhub` or
`internal/toolhub/automation`; `contracts/*` may reach only stdlib, contracts
and `internal/identity`); shrink-only baseline file
`internal/devcheck/deps-baseline.txt` listing each binary's current internal
closure: a new package fails, a stale entry fails. Failures print the import
path that reaches the package.
Tests (red first): `TestModuleImportAllowlist`, `TestRuntimeDoesNotLinkToolHub`,
`TestTransitiveForbiddenEdgeReported`.

### S2.3 No in-process ToolHub store outside ToolHub (R4.3)

Anchors: `agenttools/tools.go` `loadToolHub`, `Tools.ToolHub`,
`tools_services.go` store branches; `stack/manifest.go`
`migrateToolhubBindings` (called from `render_env.go`); `stack/hosting.go`.
Change: delete the agenttools store branches; move the render-time binding
migration to `cmd/hubctl` (which owns the store as operator tool) after
render; move `GenericControllerConfig`/`HostingProfile` to `toolhub/api`
(types only).

### S2.4 DTOs out of toolhub/api (partial)

Change: `RunContext` -> `contracts/task/v1.RunRecord` (alias kept in `api`
until callers move), `CatalogEntry` -> `toolsurface/v1`. The remaining
`toolhub/api` logic (Telegram, CLI release, OCI catalog, artifact build)
stays as baseline entries for CHG-0337, which empties the package.

### S2.5 Docs

Extraction map and package owner table are in `docs/architecture.md`
(CHG-0328 S0.3); this CHG adds the gate to `docs/engineering.md`.

### S2.6 One `hub` binary

Anchors: new `cmd/hub`; `cmd/supervisor`, `cmd/toolhub`,
`cmd/communication` mains.
Change: move each main body into an exported `Main(args []string) error` in
its internal package; `hub serve --zone control|agent|edge` calls the
supervisor, ToolHub or communication `Main`; old binaries call the same
function. One zone per process in this CHG; co-hosting several zones in one
process waits for the minimal topology (CHG-0353).
Tests: `TestHubServeZoneDispatch` (unknown zone refused, localized usage).

## Red-team checklist

- A forbidden package reached through a shared helper is reported with the
  path (`TestTransitiveForbiddenEdgeReported`).
- A contract that imports an internal package other than identity fails.
- Removing the store branch cannot leave a tool that silently succeeds
  without ToolHub: `service_disable` and ToolHub enable paths return a
  typed refusal.

## Acceptance

SPEC-0100 A1.3, A2.1, A2.2, A3.1-A3.3. `just check` fails on a new forbidden
import (shown by a temporary commit-free experiment recorded in state).

## Out of scope

- Emptying `toolhub/api` (CHG-0337). Physical process split and co-hosted
  zones (service CHGs, CHG-0353). Docker image changes: images keep their
  current entrypoints.
