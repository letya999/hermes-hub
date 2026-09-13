# M2 stage-3 evidence audit

This is a local implementation audit. Source code and fixture discovery are
not treated as production or live-provider acceptance.

| Criterion | Current evidence | Status | Remaining gate |
|---|---|---|---|
| #21 authenticated ToolHub endpoint and current authorization | `Gateway.call` uses `AuthorizeProjected` under the registry read fence; secret-free audit fields; autostart `hub-toolhub` | ✅ | authenticated gateway probe with real backend |
| #22 workload classes and lifecycle | strict shared/per-user/per-job validation, persistent per-user and cleaned per-job workspace tests | ✅ | Linux/container enforcement measurement (follow-up with #25) |
| #23 bounded CLI and isolation | `RoutingBackend` sends `bounded-cli` to `CLIRunner.Call`; missing isolation fails closed | ✅ | approved Linux sandbox/resource proof |
| #24 manifest catalog and migration | persisted enable/disable, reload on list/call, org disable-only, `hubctl migrate-toolhub` dry-run/apply, generated MCP under space `generated/`, rollback-on-failure flag | ✅ | retire generated MCP only after live round-trip (not this change) |
| #25 ToolHive/vMCP backend and enforcement | private MCP adapter and controller receipt validation for actual limits/digest/mount/egress | ⚠️ | follow-up: healthy pinned vMCP with real remote/stateful calls and Linux VPS inspect |
| #26 projection/reconnect | persisted monotonic revision, `toolhub-reconnect.request` marker, at-most-once hook; stale call denied at gateway | ⚠️ | follow-up: real Hermes transport reconnect with preserved session/home/run state |
| #47 M2 slice | exact-owner/current-revision resolution; ambiguous connections denied; values never in catalog/audit/marker | ✅ M2 | encrypted storage, OAuth and secret injection remain M3 |

Observed upstream fixture blocker: `hermes-stage2-vmcp.err.log` reports
`no backends returned capabilities`, then terminates the client session. The
fixture had an unhealthy/stopped `remote-stage2` backend. The repository does
not claim this as a successful vMCP or Hermes integration.
