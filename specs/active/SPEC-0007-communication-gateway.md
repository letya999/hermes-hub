---
status: active
title: Communication gateway and scoped Hermes jobs
---

# Requirements

1. A hub-owned communication gateway is the only component that accepts user
   messages and returns replies through communication channels. Telegram Bot API is
   the only channel adapter required in the first release.
2. Telegram transport and the optional personal Telegram connector are independent:
   the former carries conversations with Hermes, while the latter exposes Telegram
   as user-owned data and actions.
3. The gateway resolves a verified numeric Telegram sender ID through host-owned
   configuration to exactly one enabled internal user ID before creating work.
   Telegram usernames and message content never select a user, organization or
   filesystem path.
4. Every accepted message produces an immutable job context containing
   `organization_id`, `user_id`, `actor_id`, `scope_id`, `channel`, `trigger` and the
   channel's idempotency key. Hermes and its tools cannot override those fields.
5. Each configured user keeps a separate `spaces/<user>` configuration and separate
   Hermes home, workspace, connector credentials, OAuth/browser state and memory.
   A job may mount or load only the resolved user's state.
6. The first release runs one job at a time through one worker and starts a bounded
   Hermes invocation with the resolved user state. User state persists between jobs;
   the Hermes invocation does not remain shared between different users.
7. Accepted jobs and outbound replies survive a gateway or worker restart. Telegram
   update IDs and outbound delivery records are idempotent; an uncertain delivery is
   not blindly duplicated.
8. The Telegram adapter supports `/start`, `/status` and `/connections`. The
   connections response is derived from the resolved user's effective configuration
   and distinguishes configured, missing-credential, ready and policy-disabled
   connections without returning secret values.
9. The deployment may provide a shared LLM endpoint and credential. A user-specific
   LLM configuration, when explicitly configured, overrides the shared default only
   for that user's jobs.
10. Logs, job records and delivery records exclude message credentials and connector
    secret values. Explicit connector env updates retain the restrictions of
    SPEC-0005 and secret-bearing Telegram messages are deleted on a best-effort basis
    after processing.
11. Isolation tests prove that a Telegram identity mapped to one user cannot read or
    modify another user's home, workspace, memory, connector state or credentials,
    including concurrent or replayed input.

# Compatibility contract

The identifiers and job context are channel-neutral. A later channel adapter may map
its authenticated external principal and conversation to the same `actor_id` and
`scope_id` contract without changing Hermes tools or user storage. Human, group and
service scopes remain distinct even when they use the same communication channel.

The runtime contract permits later worker concurrency and dedicated always-on,
scheduled or triggered runtimes. Those modes must preserve the same immutable job
context and user-state isolation.

# Out of scope

The first release does not provide Slack or group adapters, Authentik/OIDC/SAML login,
account linking, RBAC administration, multiple organizations, multiple concurrent
workers, one container or Firecracker guest per user, always-on Hermes instances, or
a general adapter/plugin framework.
