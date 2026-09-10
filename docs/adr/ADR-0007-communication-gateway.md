---
description: Put channel-neutral routing before isolated, on-demand Hermes jobs.
last_verified: 2026-09-10
---
# ADR-0007: Communication gateway and scoped Hermes jobs

Accepted 2026-09-10.

## Decision

Add one hub-owned communication gateway in front of Hermes. The gateway normalizes
verified channel input into an immutable job context and resolves the external actor
to a host-configured internal user before any Hermes invocation. Telegram Bot API is
the first and only adapter; the gateway is not named or modeled as a Telegram service.

Keep stable internal organization, user, actor and scope identifiers separate from
Telegram IDs. In the first release, host configuration supplies the mapping and one
organization value. Each user continues to own a `spaces/<user>` configuration and
isolated runtime state.

Use one worker with global concurrency one. It starts a bounded Hermes invocation for
each job with only the resolved user's Hermes home, workspace, connector state and
credentials. The persistent worker may be shared, but a live Hermes invocation and
its mutable process state are never reused across users. Persist accepted work and
outbound deliveries in a small host-owned file spool using locks and atomic rename;
do not add a database for the initial single-host deployment.

Keep communication adapters separate from connectors. A Telegram adapter transports
conversation messages; the independent `telegram_user` connector exposes a personal
Telegram account as tools and data.

## Evolution boundary

Future Slack, web or group adapters must translate their verified external principal
and conversation into the existing job context. Authentik may later link an OIDC
subject to the same internal actor; enterprise SAML federation remains behind
Authentik. Fine-grained roles, tenant membership and scoped grants belong to a future
control-plane policy layer, not to channel adapters or Hermes prompts.

The same job contract may later be scheduled, triggered or assigned to a worker pool,
an always-on per-user container or an isolated Firecracker guest. Runtime placement
must not change identity resolution, authorization context or storage ownership.

## Consequences

The first usable deployment needs only one Telegram bot, one configured user, one
gateway and one worker. Multiple configured users remain isolated without paying for
an always-on Hermes runtime per user. Throughput is intentionally limited to one job;
the durable spool makes that limitation observable and recoverable rather than lossy.

## Related records

SPEC-0004, SPEC-0005, SPEC-0007, ADR-0002, ADR-0004 and ADR-0005.
