# CHG-0020: ToolHub stage 2 vertical slice

## Objective

Implement the next M2 slice from #21, #22, #23 and #25 without changing the
active generated-MCP Hermes runtime:

- one authenticated Go MCP endpoint per runtime;
- one shared resolver for `tools/list` and `tools/call`;
- direct, bounded CLI execution with explicit fail-closed limits;
- an adapter for a private ToolHive/vMCP streamable-HTTP endpoint;
- lifecycle rules for shared, per-user and per-job workloads;
- reconnect-only transport recovery, preserving the upstream Hermes home and
  session/run state.

## Constraints

- `internal/identity` remains the only ownership authority.
- ToolHive is an external execution/aggregation backend, never an auth store.
- No secret values enter the catalog, MCP request, log, or snapshot.
- No runtime routing switch, dynamic catalog migration, OAuth UX, or full
  secret-management system in this change.
- Real ToolHive/remote-MCP/stateful-container and target Linux VPS evidence is
  recorded only when the required external binary, host and credentials exist.

## Implementation slices

1. Extend the stage-1 resolver with deterministic projected tool lookup.
2. Add the stable streamable-HTTP endpoint with bearer-to-identity mapping and
   wire it into Hermes only through explicit `HUB_TOOLHUB_*` opt-in variables.
3. Add the ToolHive/vMCP HTTP MCP client adapter and an opt-in contract probe.
4. Add the bounded CLI runner: allowlisted executable, structured arguments,
   exact environment/cwd, output and timeout limits, and process-tree cleanup.
5. Add lifecycle helpers for persistent per-user state and cleaned per-job
   state; keep external CPU/memory/egress enforcement explicit.
6. Add unit, race and HTTP-contract regressions, then update SPEC-0018,
   architecture, migration and validation evidence.

## Verification

- `go test -race ./internal/toolhub ./internal/identity`
- `just check`
- `just security` if module/dependency pins change
- `just docker-check` when Docker is available
- `TOOLHIVE_VMCP_ENDPOINT=... go run ./cmd/devcheck toolhub-contract` only on a
  Linux host with pinned ToolHive/vMCP and approved fixture workloads.
- `TOOLHIVE_VMCP_ENDPOINT=... go run ./cmd/devcheck toolhub-gateway-contract`
  for the full authenticated Go gateway to vMCP path; also set explicit tool
  names and `TOOLHIVE_STATEFUL_IMAGE_DIGEST`.
- `go run -tags integration ./cmd/devcheck toolhub-hermes-contract` with
  `HERMES_CONTRACT_IMAGE`, `TOOLHIVE_VMCP_ENDPOINT` and `TOOLHIVE_REMOTE_TOOL`
  to verify real upstream Hermes plus gateway-only restart/reconnect.
- Record ToolHive restart/state evidence separately from the Go control-plane
  tests; local fixture evidence never substitutes for target Linux VPS or live
  provider acceptance.

## Rollback

The endpoint and adapter are opt-in code paths. Roll back by leaving the new
ToolHub endpoint disabled and continue using the generated direct MCP config;
do not delete Hermes homes, state directories, connections or credentials.
