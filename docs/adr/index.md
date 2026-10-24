---
description: Architectural decisions and clearly marked proposals; append new records when changing direction.
last_verified: 2026-10-20
---
# Decisions

| Record | Decision |
|---|---|
| [ADR-0073](ADR-0073-memory-vault-openknowledge.md) | Proposed: per-space markdown memory vault (leaves/cards/summaries/CORE) with deterministic nightly rollup, plus OpenKnowledge as opt-in knowledge surface (M20) |
| [ADR-0072](ADR-0072-connector-recovery-and-system-guide.md) | Owner-bound encrypted recovery, truthful inventory and mandatory reference guidance |
| [ADR-0071](ADR-0071-dynamic-session-contract.md) | Bounded owner-only dynamic JSON session contracts |
| [ADR-0070](ADR-0070-recipe-oauth.md) | Recipe-driven OAuth and lease-local token file views |
| [ADR-0069](ADR-0069-broker-effective-contract.md) | Imported from services/credential-broker ADR-0010: effective contract on request — extras and demote |
| [ADR-0068](ADR-0068-broker-allow-shared.md) | Imported broker ADR-0009: allow_shared in dynamic contracts |
| [ADR-0067](ADR-0067-broker-dynamic-contract-registration.md) | Imported broker ADR-0008: dynamic contract registration via control API |
| [ADR-0066](ADR-0066-broker-consumer-id.md) | Imported broker ADR-0007: consumer_id discriminates multiple credentials per connection |
| [ADR-0065](ADR-0065-broker-memory-bank.md) | Imported broker ADR-0006: medium memory-bank adaptation |
| [ADR-0064](ADR-0064-broker-bounded-oauth.md) | Imported broker ADR-0005: bounded OAuth, no runtime platforms |
| [ADR-0063](ADR-0063-broker-single-process-journal.md) | Imported broker ADR-0004: single-process encrypted journal |
| [ADR-0062](ADR-0062-broker-shared-by-delivery.md) | Imported broker ADR-0003: shared credentials allowed by delivery method |
| [ADR-0061](ADR-0061-broker-browser-approval-hub.md) | Imported broker ADR-0002: browser confirmation via Communication Hub |
| [ADR-0060](ADR-0060-broker-not-secret-store.md) | Imported broker ADR-0001: the broker is not a canonical secret store |
| [ADR-0059](ADR-0059-browser-workloads-as-connectors.md) | Accepted: browsers as ordinary connectors — web egress, per-connection profile isolation, one-time owner viewer links, staged toolsets, bounded shm budget; agent-browser stays blocked |
| [ADR-0058](ADR-0058-team-connections.md) | Accepted: operator-published Team records, team-owned connections with member bindings, shareable credential/connection modes, not_permitted member lifecycle, sender-attributed audit |
| [ADR-0057](ADR-0057-ci-built-connector-images.md) | Accepted: catalog tiers T0/T1/T2, CI-built digest-pinned images with zero-build admission, deterministic `connector import` under --confirm, staged toolset projection, bounded prefetch |
| [ADR-0056](ADR-0056-ownership-finalizers.md) | Accepted: ownership labels on spawned resources, persisted finalizer ladder with verified-zero receipts, one reconciler, revoke-wins commit, bounded store compaction |
| [ADR-0055](ADR-0055-unified-connector-surface.md) | Accepted: nine `connector_*` tools over one durable operation journal; typed codes, no prose markers or link material in agent results; same facade on MCP, hubctl and control HTTP |
| [ADR-0054](ADR-0054-human-action-surface.md) | Accepted: one-time `?t=` human-action links on an opt-in public surface, origin-channel delivery, device flow and paste-redirect OAuth, transactional credential save |
| [ADR-0053](ADR-0053-recipe-source-ledger.md) | Accepted: content-addressed secret-free recipes, ranked source ledgers, fail-closed same-rank ambiguity, digest-referenced onboardings |
| [ADR-0051](ADR-0051-deterministic-work-isolation.md) | Accepted: local writer ownership, durable result handoff and isolated script containers (renumbered from a duplicate ADR-0033) |
| [ADR-0050](ADR-0050-knowledge-in-runtime-state.md) | Accepted: knowledge in runtime state, staged via apply --state, relocated offline via hubctl spool migrate |
| [ADR-0049](ADR-0049-broker-only-credential-intake.md) | Accepted: credential intake only via Broker or runtime self-env; legacy gateway surface removed |
| [ADR-0048](ADR-0048-routines-package.md) | Accepted: internal/routines leaf owns routine semantics and engine behind a Deps seam |
| [ADR-0047](ADR-0047-channel-contract.md) | Accepted: 12-method Channel contract, Caps flags, deps-injected adapters, registry dispatch and conformance suite |
| [ADR-0046](ADR-0046-comm-plane-zone-map.md) | Accepted: comm-plane zone map — commodel/channel/routines/commops/serviceauth owners, fixed import direction |
| [ADR-0045](ADR-0045-watcher-probe-pipeline.md) | Accepted: watcher probe = fetch + deterministic extract pipeline; isolated script/command probes; snapshotted recipes |
| [ADR-0044](ADR-0044-routine-destinations.md) | Accepted: verified task destinations and per-run origin for routine delivery |
| [ADR-0043](ADR-0043-compact-routine-tools.md) | Accepted: five public routine tools; lifecycle operations retained internally |
| [ADR-0042](ADR-0042-routine-lifecycle.md) | Accepted direction: routine lifecycle, revision fences and interval windows |
| [ADR-0041](ADR-0041-proactive-automation.md) | Accepted: proactive automation on the occurrence pipeline — bounded follow-up retry, quiet/cap/escalation policy, control-plane tool calls, owner brief, event subscriptions (M12) |
| [ADR-0040](ADR-0040-single-stack-supervisor.md) | Accepted direction: one Compose supervisor using the existing control image; container deployment verified live in CHG-0097 |
| [ADR-0039](ADR-0039-hermes-pin-raise.md) | Proposed: conditional Hermes pin raise once upstream merges the MCP breaker fix (D12c) |
| [ADR-0038](ADR-0038-embedded-store.md) | Proposed: embedded single-writer store gated on measured post-deploy latency (D12b) |
| [ADR-0037](ADR-0037-supervisor-in-linux-container.md) | Proposed: containerized supervisor via the ToolHub socket proxy (D12a) — answered by ADR-0040's simpler accepted direction |
| [ADR-0036](ADR-0036-execution-chain-reliability.md) | Accepted: one run budget with idle deadlines, internal uncertain reconciliation, lease-safe lifecycle, rights-only policy hash, capability readiness, event-driven wakeups, correlation IDs (M19) |
| [ADR-0035](ADR-0035-telegram-operation-contracts.md) | Accepted: byte-based images, explicit MCP env, routine creation receipts, current approval UI and exact file handoff |
| [ADR-0034](ADR-0034-cli-package-policy.md) | Accepted: private package home, exact shipped CLI trust and atomic host catalog binding |
| [ADR-0033](ADR-0033-bounded-cli-transport.md) | Accepted: bounded CLI as a ToolHub transport with executor-container isolation receipts |
| [ADR-0032](ADR-0032-capability-execution-isolation.md) | Accepted: isolate control state, Hermes, scoped file executors and scratch-only code workloads |
| [ADR-0031](ADR-0031-default-deny-capability-policy.md) | Accepted: zero-capability baseline, explicit profiles, canonical identities and one ToolHub policy boundary |
| [ADR-0030](ADR-0030-cliproxy-image-routes.md) | CLIProxy image ids use two calls: images endpoint, or Gemini chat completions |
| [ADR-0029](ADR-0029-bounded-pdf-office-webp.md) | Bounded pdf, xlsx, pptx, and webp stay in the hub process; Word and a user zip tool stay out |
| [ADR-0028](ADR-0028-configurable-image-capability.md) | Image capability selects provider, model, and delivery; CLIProxy is the default, Fal stays external |
| [ADR-0027](ADR-0027-workspace-document-image-profile.md) | Hub document and image tools; separate credentials, workspace artifacts, native vision and image_gen omitted |
| [ADR-0026](ADR-0026-shared-control-plane.md) | One shared ToolHub/broker/controller on `hermes-hub-runtime`; multi-token endpoint, `infra` render split |
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
| [ADR-0014](ADR-0014-scale-to-zero-context-runtimes.md) | Scale-to-zero warm Hermes runtime per active context; Hub owns routine schedules (five-minute idle superseded by 30-min warm TTL in ADR-0036) |
| [ADR-0013](ADR-0013-toolhub-security-boundary.md) | Go ToolHub authorizes; ToolHive executes and aggregates internally |
| [ADR-0012](ADR-0012-hermes-api-contract.md) | Accepted: pinned Hermes authenticated API server as the runtime-adapter contract target |
| [ADR-0011](ADR-0011-stable-identity-identifiers.md) | Stable opaque IDs with a compatibility mapping for current spaces and jobs |
| [ADR-0010](ADR-0010-direct-mcp-atlassian.md) | Install pinned direct `mcp-atlassian` stdio server instead of Atlassian Rovo |
| [ADR-0009](ADR-0009-scoped-homes-and-communication-hub.md) | Peer organization/user homes and separate communication-hub |
| [ADR-0008](ADR-0008-telegram-service-self-service.md) | Telegram connector catalog and self-service enablement |
| [ADR-0007](ADR-0007-communication-gateway.md) | Channel-neutral gateway with isolated on-demand Hermes jobs |
| [ADR-0006](ADR-0006-work-service-boundaries.md) | Provider-native boundaries for Atlassian, GitLab and Google |
| [ADR-0005](ADR-0005-self-env-and-telegram-boundaries.md) | Separate Telegram paths and user self-env overlay |
| [ADR-0004](ADR-0004-organization-user-scope.md) | Organization policy overlay with isolated user runtimes |
| [ADR-0003](ADR-0003-go-first-tooling.md) | Just and Go for project-owned tooling and runtime supervision |
| [ADR-0002](ADR-0002-standalone-user-spaces.md) | Standalone Hermes, spaces/user and Docker environments |
| [ADR-0001](ADR-0001-isolated-hermes-deployments.md) | Historical initial design; partly superseded by ADR-0002 |
