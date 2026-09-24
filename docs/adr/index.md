---
description: Accepted architectural decisions; append new decisions when changing direction.
last_verified: 2026-09-21
---
# Decisions

| Record | Decision |
|---|---|
| [ADR-0025](ADR-0025-context-parallel-gateway-workers.md) | Bounded worker pool; FIFO claim skips busy context keys, serial per context |
| [ADR-0024](ADR-0024-toolhive-local-vmcp.md) | Defer ToolHive local vMCP as the stable endpoint after failed dynamic-rebind evidence |
| [ADR-0023](ADR-0023-optional-mcp-images.md) | Optional Google/Atlassian/Telegram Python MCP trees are dedicated images, not the hub runtime |
| [ADR-0022](ADR-0022-credential-broker-boundary.md) | Keep Credential Broker as a separate opt-in Go module/process with explicit Hub adapters |
| [ADR-0021](ADR-0021-toolhub-control-plane.md) | Control MCP, operator grants and loopback credential elicitation |
| [ADR-0020](ADR-0020-local-account-preparation.md) | Owner-selected PC, fixed read-only ToolHive controller and protected local account setup |
| [ADR-0019](ADR-0019-official-workspace-telegram-sdk.md) | Official Workspace MCP and owner-approved Python Telegram account workload |
| [ADR-0018](ADR-0018-personal-provider-api.md) | Stateless official provider HTTPS calls behind ToolHub authorization |
| [ADR-0017](ADR-0017-personal-assistant-channels.md) | Telegram/Slack communication channels, voice envelope and context lifecycle |
| [ADR-0016](ADR-0016-encrypted-credentials-and-oauth.md) | Ciphertext store behind ToolHub locators, OAuth broker and audit |
| [ADR-0015](ADR-0015-durable-job-runtime-mappings.md) | Durable job, session, run and generation mappings in local stores |
| [ADR-0014](ADR-0014-scale-to-zero-context-runtimes.md) | Scale-to-zero warm Hermes runtime per active context; Hub owns routine schedules |
| [ADR-0013](ADR-0013-toolhub-security-boundary.md) | Go ToolHub authorizes; ToolHive executes and aggregates internally |
| [ADR-0011](ADR-0011-stable-identity-identifiers.md) | Stable opaque IDs with a compatibility mapping for current spaces and jobs |
| [ADR-0010](ADR-0010-direct-mcp-atlassian.md) | Install pinned direct `mcp-atlassian` stdio server instead of Atlassian Rovo |
| [ADR-0009](ADR-0009-scoped-homes-and-communication-hub.md) | Peer organization/user homes and separate communication-hub |
| [ADR-0008](ADR-0008-telegram-service-self-service.md) | Telegram connector catalog and self-service enablement |
| [ADR-0007](ADR-0007-communication-gateway.md) | Channel-neutral gateway with isolated on-demand Hermes jobs |
| [ADR-0006](ADR-0006-work-service-boundaries.md) | Provider-native boundaries for Atlassian, GitLab and Google |
| [ADR-0005](ADR-0005-self-env-and-telegram-boundaries.md) | Separate Telegram paths and user self-env overlay |
| [ADR-0004](ADR-0004-organization-user-scope.md) | Organization policy overlay with isolated user runtimes |
| [ADR-0001](ADR-0001-isolated-hermes-deployments.md) | Historical initial design; partly superseded by ADR-0002 |
| [ADR-0002](ADR-0002-standalone-user-spaces.md) | Standalone Hermes, spaces/user and Docker environments |
| [ADR-0003](ADR-0003-go-first-tooling.md) | Just and Go for project-owned tooling and runtime supervision |
