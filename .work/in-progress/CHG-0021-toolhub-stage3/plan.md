# CHG-0021: ToolHub stage 3 migration and enforcement foundation

## Objective

Complete the remaining M2 control-plane work for #24 and #26 and close the
stage-2 gaps in #21–#25 without switching the current runtime to ToolHub.
Reuse `internal/identity`, the existing atomic JSON store, the authenticated
gateway and current runners. Do not patch Hermes or add a second ownership
registry.

## Scope

1. Record the implementation/evidence audit for #21–#25 and update CHG-0020.
2. Require controller attestation of actual running workload limits, digest,
   mounts and egress; keep container and CLI execution fail-closed without an
   external isolation/enforcement boundary.
3. Add persisted monotonic projection revisions and an observable,
   at-most-once-per-revision reconnect hook. Gateway calls continue to resolve
   the current binding immediately before backend execution.
4. Replace the ToolHub-enabled service catalog/enable path with manifest-backed
   catalog entries and owner-scoped effective binding enable/disable. Legacy
   `stack`/`self-services.json` remains an explicit compatibility fallback.
5. Add `hubctl migrate-toolhub --apply` and dry-run planning. Import only
   explicit manifest data, preserve disabled states, copy no secret values,
   retain rollback material and report unmatched generated/discovery-only MCP.
6. Add regression tests for projection quietness, owner isolation, current
   state, secret-free admission, successful backend results and concurrency.

## Explicitly deferred

ToolHub remains opt-in. Gateway rollout, ToolHive installation/backend,
provider OAuth, reconnect implementation inside Hermes, a full secret manager,
Linux VPS resource/egress measurement and cleanup of legacy files remain later
gates. A vMCP HTTP 200/204 without a proof receipt is not integration evidence.

## Verification

- `go test -race ./internal/toolhub ./internal/identity ./internal/devcheck`
- `just check` with own Go statement coverage >=85%
- `just security` only if dependency or pin changes
- `just docker-check` when Docker is available
- explicit ToolHive/vMCP, authenticated gateway and real Hermes contract probes
  only with pinned external fixtures and controller attestations
- Linux VPS persistence/recovery/egress/resource gates recorded as unavailable
  when no approved Linux host is present

## Rollback

Leave `HUB_TOOLHUB_STORE` unset to keep the generated direct-MCP compatibility
path. Migration apply keeps a `.rollback` store; restore it before removing
the opt-in environment. Do not delete spaces, sessions, credentials or legacy
settings as part of this change.
