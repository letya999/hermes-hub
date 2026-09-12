---
description: Reversible current-to-target migration and scale-to-zero deletion gates.
last_verified: 2026-09-12
---
# Current-to-target migration map

This map converts the M0 decisions into ordered implementation work. A target entry is
not evidence that it exists today. Existing installations remain on their current path
until an explicit stage is enabled and its rollback/deletion gate passes.

Target request flow:

```text
channel -> communication hub -> host runtime supervisor -> warm context runtime
                                  |
                                  v
                         stable Go ToolHub gateway
                                  |
                                  v
                            internal vMCP
                                  |
                                  v
                           ToolHive Runtime
                                  |
                                  v
                       remote/container MCP backend
```

Go owns authenticated identity, current binding/policy checks, exact-owner credential
selection, projection revisions and controlled MCP reconnect. vMCP owns only internal
aggregation, filtering and conflict naming. ToolHive Runtime owns workload execution,
transport adaptation and egress isolation.

## Component decisions

| Current path | Decision and target owner | Coexistence / migration | Rollback and deletion gate | Destination |
|---|---|---|---|---|
| `communication-hub` channel identity, durable job/reply spool and delivery | **Keep** in Go | Envelope continues to carry stable principal/context/runtime IDs | Roll back service image; preserve `communication-hub-data`. Never delete while queued or uncertain deliveries exist | #21 and existing gateway work |
| Private `POST /v1/execute` runtime endpoint | **Adapt** into the communication-hub to supervisor job contract; the supervisor locates or starts the bound context runtime | Keep callable during per-context rollout and route one configured context at a time | Fall back to `/v1/execute` without moving the Hermes home. Delete only after cold start, warm reuse, cancellation, idempotency, idle reaping and queued-delivery recovery pass against real Hermes | #14-#19 |
| One-shot `hermes -z` process per job | **Replace** with a warm scale-to-zero Hermes `/api/sessions` and `/v1/runs` adapter | Preserve one-shot mode as a release-scoped host-selected fallback; a new job reuses the context runtime within its idle TTL | Re-enable one-shot adapter. Delete only after real pinned-Hermes session/run/restart/sleep evidence and at least one compatibility release | #14-#19 |
| Static per-user `hermes-runtime` Compose service | **Replace** in multi-user mode with a trusted host-side Go supervisor and dynamically created per-context containers | Keep the static personal deployment as rollback while contexts migrate independently | Restore the static service without changing `spaces/<context>`; never expose Docker control to the communication or Hermes container | #14, #18, #19 |
| Native Hermes cron as a resident clock owner | **Adapt** through an explicit migration to hub-owned durable routines; Hermes remains the agent-job executor | A context with unmigrated active native cron is pinned on; dual execution is forbidden | Re-enable the pinned-on context and native schedule from the recorded migration state | #33 |
| Hermes-owned sessions, memory, profile, skills, hooks and voice | **Keep** upstream and in the scoped Hermes home | Mount the same home into each warm runtime generation; no reimplementation or implicit copy | Stop the new runtime and remount the same home read-write to the previous pinned image | #10, #14; ADR-0012, ADR-0014 |
| Generated direct MCP configuration under `spaces/<user>/generated/` | **Replace** as the primary connector registry with one stable authenticated ToolHub URL | Initially render both paths but enable exactly one per runtime. Direct config remains rollback-only and must not duplicate tools | Switch runtime config back to the previous generated file. Retire after add/remove/revoke/reconnect and conflict-name tests pass with real vMCP | #20, #21, #25, #26 |
| User/organization `mcp_servers` definitions in settings/scope | **Adapt** into immutable catalog manifests and effective bindings | Import definitions as disabled or with their previous explicit enabled state; preserve immutable source/version, allowlist and ownership | Keep source files unchanged until import report is accepted. Delete old interpretation only after round-trip comparison and deny tests | #20, #24 |
| Embedded/pinned connector packages and presets | **Adapt** into catalog definitions; **replace** in-process supervision with ToolHive workloads where supported | Existing packages remain available per connector until its real provider/runtime validation passes | Re-select the old pinned connector path. Remove package only after replacement pin, health, state, egress and provider contract gates pass | #20, #22, #25 and provider issue |
| `service_catalog` hard-coded Go entries | **Replace** data source with manifest catalog plus secret-free connection status; keep the bounded tool/API name during compatibility | Old and new results are compared for configured services; unknown/new manifests remain opt-in | Select legacy catalog reader. Delete tables only after migration reports no unmatched enabled service | #24 |
| `service_enable` writing `self-services.json` | **Adapt** to create/disable effective connection bindings through Go | Read legacy selections and materialize owner/context/runtime bindings; mutations still require a concrete owner instruction | Continue reading `self-services.json`; never infer enablement. Retire only after idempotent add/disable/remove and stale-session denial pass | #21, #24, #26, #47 |
| `env_update` and `self-env.json` | **Adapt**, then restrict to explicitly terminal-visible personal credentials; managed secrets move to scoped encrypted storage | Import key names and owner choice, never values through manifests/logs. Keep file untouched until each connection is verified | Continue loading the old overlay for that runtime only. Retire managed entries only after decrypt/inject/rotate/revoke/restart/restore tests and an accepted backup | #47, #48, #51 |
| Host/user env files and Docker secret injection | **Keep** for runtime-control keys; **replace** connector-wide exposure with per-workload injection | Split runtime-control and connector credential names; ambiguous ownership fails and does not migrate | Preserve original file and permissions. Remove a connector value only after its encrypted reference and real workload injection succeed | #47, #48, #51 |
| `spaces/<id>/connections/`, browser profiles and Telegram sessions | **Keep** owner-scoped data; **adapt** metadata to stable `connection_id`/`credential_ref` | Record metadata without copying opaque session content; stateful connector mounts default to per-user | Revert metadata only. Never delete opaque state automatically; explicit owner deletion is required | #22, #47, #51 |
| Hub-owned bounded file/HH MCP and native bridge | **Adapt** behind the stable ToolHub endpoint where the external ToolHive contract fits; otherwise keep as bounded Go backend | Preserve current authorization and filesystem checks; aggregation must not weaken them | Route directly to the prior internal server. Retire direct exposure only after equivalent deny-path and receipt tests | #21, #25 and provider issue |
| ToolHive vMCP | **Add** as internal replaceable aggregation only | Go gateway remains the only Hermes-facing endpoint; vMCP is private and can be bypassed during rollback | Route gateway to previous backend. Adoption requires pin, add/remove/conflict/filter/recovery evidence | #11, #21, #25 |
| ToolHive Runtime and ingress/egress/DNS sidecars | **Add** as conditional workload backend | Migrate connectors individually; shared workloads are allowed only for stateless per-request authorization | Stop the migrated workload and restart its previous pinned path without deleting state | #22, #25, #46 |
| Bearer runtime authentication | **Keep** as private baseline; later **adapt** identity source to OIDC | OIDC may be added at ingress without changing stable IDs or per-call binding checks | Disable new ingress and return to private bearer path | #21, future #54 |
| Files/SQLite control-plane state | **Keep** for personal deployment; later **adapt** storage without changing IDs | Schema revisions are explicit and backwards-readable for the rollback window | Restore versioned backup and previous binary; no startup-time implicit migration | #20, #47; future persistence issue |

## Rollout stages

1. **Freeze contracts:** complete manifests (#20), scoped connections/bindings (#47),
   workload classes (#22) and this map. No runtime routing changes.
2. **Introduce the boundary:** add the stable Go ToolHub endpoint (#21) with the same
   effective tools and deny behavior as today. ToolHive stays private.
3. **Add the backend:** integrate pinned ToolHive/vMCP (#25) one connector at a time,
   proving remote MCP, Linux stateful persistence, egress, health and resource limits.
4. **Move control data:** migrate catalog/enablement (#24) and managed credentials
   (#48/#51) with dry-run reports, explicit apply and exact-owner verification.
5. **Enable dynamic projection:** add/disable/remove plus controlled reconnect (#26).
   A stale session must fail at Go before backend execution.
6. **Adopt scale-to-zero Hermes:** move selected contexts from `/v1/execute` and
   `hermes -z` to the pinned session/run API behind the host supervisor. Prove cold
   start, warm reuse, idle stop and restoration from the same scoped home.
7. **Move routines:** migrate native cron explicitly to hub-owned schedules, or keep
   the affected context pinned on until its migration is accepted.
8. **Retire legacy:** remove a legacy reader/path only after its row's deletion gate,
   one compatibility release and deployment acceptance have all passed.

Stages are monotonic per user runtime, not global. Failure rolls back only that runtime
or connector. Migration never deletes a home, connection, secret, browser profile,
session or queued delivery.

## Mixed-version contract

- The communication envelope keeps stable IDs and `policy_version`; old consumers may
  read compatibility fields, while new consumers reject missing canonical authority.
- Exactly one MCP projection is active for a runtime. Dual rendering is allowed for
  comparison, dual execution is not.
- A manifest/binding revision is immutable. In-flight calls retain their recorded
  revision, but revocation is checked against current state before backend execution.
- Old and new binaries may share immutable catalog data, never mutable workload leases.
- Unknown schema/revision, ambiguous ownership or partial migration fails closed and
  leaves the previous active configuration unchanged.

## Migration report and deletion proof

Every dry-run/apply report contains source/target revision, stable owner/context/runtime
IDs, object counts, key names (not values), checksums where safe, selected stage and
rollback command. It records skipped/ambiguous objects as errors. No automatic startup
migration is permitted.

Deletion requires all of: accepted migration report, backup/restore evidence, real
integration evidence for external runtimes, cross-user and stale-policy denial tests,
restart/recovery evidence, no unmatched enabled objects, and an explicit cleanup
change. Passing mocks or merely hiding a tool is insufficient.

Related: [architecture](architecture.md), [threat model](threat-model.md), ADR-0009,
ADR-0012, ADR-0013 and issues #8-#13.
