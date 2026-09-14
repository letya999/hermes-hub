# CHG-0007: Communication gateway and scoped Hermes jobs

## Goal

Let the owner use Hermes through one Telegram bot while keeping channel routing,
per-user state and future runtime placement separate.

## Scope

1. Add host-owned gateway configuration with one organization ID, Telegram bot
   settings and an injective numeric Telegram sender ID to internal user ID mapping.
   Validate referenced spaces at startup and reject duplicate or unknown mappings.
2. Add a Go communication gateway with a compiled-in Telegram adapter. Normalize
   inbound updates into the SPEC-0007 job context; do not expose routing identifiers
   as model-controlled arguments.
3. Add a bounded host-owned file spool for accepted jobs and outbound deliveries.
   Use Telegram update ID for inbound deduplication, atomic state transitions and
   explicit records for uncertain outbound outcomes.
4. Add one worker with global concurrency one. For every job, resolve effective user
   and organization settings, construct user-specific paths and environment, then run
   a fresh bounded Hermes invocation without mounting another user's state.
5. Keep shared deployment LLM configuration as the default and allow an explicitly
   configured user override through the existing effective-settings render path.
6. Implement `/start`, `/status` and `/connections`; derive connection state from the
   existing catalog, effective policy and `doctor` checks.
7. Route explicit connector env updates through the existing constrained
   `env_update` boundary and request best-effort deletion of the secret-bearing
   Telegram message without logging or persisting its value in the gateway spool.
8. Add regression tests for identity collisions, unknown/disabled users, path
   isolation, cross-user replay, job/delivery recovery and Telegram-versus-connector
   separation.
9. Update operator setup, current architecture and validation evidence after the
   implementation exists. Run `just check`; run live Telegram acceptance separately
   because mocks do not prove Bot API delivery or deletion.

## Delivery slices

1. Single owner: one configured mapping, Telegram ingress, one worker, isolated state
   and a durable reply.
2. Configured users: multiple mappings and spaces with isolation and replay tests.
3. Self-service commands: status, connections and constrained credential update flow.

## Deferred

Slack and group adapters, Authentik account linking, RBAC, multiple organizations,
worker pools, database-backed queues, always-on/scheduled/triggered agents, per-user
containers and Firecracker placement are deferred until their first real consumer.
They must reuse the SPEC-0007 job and storage-ownership contracts.

## Open implementation checks

- Verify the pinned upstream Hermes invocation and shutdown contract before choosing
  the subprocess command and timeout behavior.
- Choose the gateway configuration filename while implementing, reusing the existing
  settings parser if it can represent the mapping without creating a second source of
  user configuration.
