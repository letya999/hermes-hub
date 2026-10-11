# Change plans

Use todo / in-progress / done directories for active change folders. A folder contains
plan.md and state.yaml. Task progress belongs here, never in current architecture docs.

The table tracks current change records. Issue 122's broader default-deny design
is CHG-0065; its implementation is in progress. Issues 167-169 (task sessions,
per-task style, measured usage) shipped as CHG-0066/0067/0068 and closed
2026-10-06. Live deployment follow-ups
also remain in issues #73 (ToolHive/VPS), #74 (Hermes reconnect) and #166/#170/#172
(live channel acceptance evidence).

| Change | State |
|---|---|
| [CHG-0079](done/CHG-0079-cli-policy-completion/plan.md) | Done: six CLI policy/catalog gaps, full checks and local dev deployment verified |
| [CHG-0078](done/CHG-0078-cli-cells/plan.md) | Done: per-principal sandbox cells (sibling containers, net none, warm ladder, toolbox mounts, shared-catalog grants) — issues #239–#242, live model verified |
| [CHG-0077](done/CHG-0077-toolhub-cli/plan.md) | Done: milestone 9 ToolHub CLI connectors — SPEC-0047, admission verifier, catalog defs, prepare_source cli, digest-pinned artifacts |
| [CHG-0075](in-progress/CHG-0075-remote-mcp/plan.md) | Milestone 8 issues 83/84/85/96: owner-scoped remote MCP register/admit/project through prepare_source remote_url |
| [CHG-0074](in-progress/CHG-0074-telegram-auth-plugin/plan.md) | Opt-in in-process Telegram QR login deployed locally; live account acceptance pending owner scan |
| [CHG-0073](in-progress/CHG-0073-prepared-bundle-telegram/plan.md) | Issues 39/115: 12-ID prepared bundle discovery, selected personal Telegram source and protected onboarding; verification in progress |
| [CHG-0069](in-progress/CHG-0069-session-ux-and-quota/plan.md) | Session UX (new/list/switch/delete/archive/auto-title), forum topics, command inventory, provider quota in /usage — committed, live evidence pending |
| [CHG-0068](done/CHG-0068-usage-report/plan.md) | Issue 169 closed: measured `/usage` live-verified end-to-end 2026-10-06 |
| [CHG-0067](done/CHG-0067-task-style/plan.md) | Issue 168 closed: per-task `/style` admission-snapshotted; instructions channel live-verified |
| [CHG-0066](done/CHG-0066-task-sessions/plan.md) | Issue 167 closed: durable tasks + DM topics live on the deployed bot |
| [CHG-0065](in-progress/CHG-0065-capability-enforcement/plan.md) | Issue 122: accepted ADR-0031/0032, frozen SPEC-0041/0042; P0 probes and P1 grant hardening in progress |
| [CHG-0064](in-progress/CHG-0064-capability-profile/plan.md) | Issue 122 earlier matrix/name-reservation scope; broader enforcement continues in CHG-0065 |
| [CHG-0063](in-progress/CHG-0063-media-generation-service/plan.md) | hub-media: sidecar for media generation |
| [CHG-0062](in-progress/CHG-0062-media-sidecars/plan.md) | Issue 170 redo: standalone hub-stt/hub-tts services (separate images), swappable engines, async long-audio jobs |
| [CHG-0061](in-progress/CHG-0061-voice-readiness/plan.md) | Issue 170: separate STT/TTS readiness in `/voice`, honest unavailable state |
| [CHG-0060](in-progress/CHG-0060-artifact-delivery/plan.md) | Issue 172: run artifact handoff and chat delivery — committed, live smoke pending |
| [CHG-0059](in-progress/CHG-0059-channel-rendering/plan.md) | Issue 166: per-channel Markdown rendering and lossless multi-part replies — committed, live smoke pending |
| [CHG-0058](in-progress/CHG-0058-web-search-research/plan.md) | Issue 123: web search and bounded deep research — merged to dev |
| [CHG-0054](done/CHG-0054-diagnostics-hardening/plan.md) | Diagnostics hardening: ingest redaction, failure isolation, throttles, per-principal caps — shipped (PR #158) |
| [CHG-0053](done/CHG-0053-user-diagnostics/plan.md) | Issue 125: user-scoped diagnostics control op — shipped (PR #155, #156) |
| [CHG-0052](in-progress/CHG-0052-document-image-tools/plan.md) | Workspace document and image capability; Gemini image calls use chat completions, images-endpoint models stay on /images; Fal and a rebuilt Hermes image still open |
| [CHG-0047](in-progress/CHG-0047-ssh-capability/plan.md) | Issue 42: opt-in user-scoped SSH capability (read/write/shell/tunnel) |
| [CHG-0047](in-progress/CHG-0047-mcp-admission-and-wake/plan.md) | Credential-gated MCP admission, sibling ToolHub tokens, and lost first-message delivery |
| [CHG-0046](in-progress/CHG-0046-automatic-diagnostics/plan.md) | Automatic bounded diagnostics across users and services |
| [CHG-0045](in-progress/CHG-0045-diagnostics/plan.md) | Bounded multiuser Docker diagnostics and Telegram/job correlation |
| [CHG-0044](in-progress/CHG-0044-build-disk-control/plan.md) | Separate core/control images and reclaim stale rebuild state |
| [CHG-0037](in-progress/CHG-0037-toolhub-closeout/plan.md) | ToolHub closeout: Broker readiness, isolation, registry discovery, Hermes reconnect, prepared bundle and production acceptance |
| [CHG-0036](in-progress/CHG-0036-repository-driven-toolhub/plan.md) | Repository-driven ToolHub: prepared data, discovery and four exact-source live scenarios |
| [CHG-0035](in-progress/CHG-0035-toolhive-vmcp-audit/plan.md) | ToolHive local vMCP evaluation: no-go evidence recorded |
| [CHG-0034](in-progress/CHG-0034-noncredential-env-filter/plan.md) | Non-credential env names excluded from connection recipe; gitlab-pat broker contract |
| [CHG-0033](in-progress/CHG-0033-node-bin-aliases/plan.md) | Node bin aliases to one file resolve as a single MCP entrypoint |
| [CHG-0031](in-progress/CHG-0031-docker-reclaim/plan.md) | Docker reclaim: stale hub tags, dangling and artifact-build orphans removed by devcheck docker-clean |
| [CHG-0029](in-progress/CHG-0029-github-mcp-install/plan.md) | Safe generic GitHub MCP source review; cache mounts ignored as metadata, secret mounts remain denied |
| [CHG-0028](in-progress/CHG-0028-credential-broker-merge/plan.md) | Перенос Credential Broker и opt-in интеграция с Hermes Hub |
| [CHG-0027](in-progress/CHG-0027-m53-e2e/plan.md) | M5.3 Hermes-mediated MCP onboarding |
| [CHG-0026](in-progress/CHG-0026-user-binding-journey/plan.md) | M5.2 control MCP, grants, elicitation and bind/project/revoke |
| [CHG-0025](in-progress/CHG-0025-trusted-artifacts/plan.md) | **done** M5.1 trusted artifacts + local ToolHive; [closeout.md](in-progress/CHG-0025-trusted-artifacts/closeout.md) |
| [CHG-0024](in-progress/CHG-0024-personal-data-connectors/plan.md) | M5 stage 2: Google Workspace, Slack data and personal Telegram connectors behind ToolHub |
| [CHG-0023](done/CHG-0023-personal-assistant/plan.md) | M4 personal assistant: Telegram/Slack channels, voice, routines, memory/skills/backup |
| [CHG-0022](done/CHG-0022-credentials-connector-platform/plan.md) | M3 shipped-path inject/audit/registry wiring; just check 85.07%; docker-check prod a61ce4879a1d |
| [CHG-0021](done/CHG-0021-toolhub-stage3/plan.md) | M2 control-plane done; #20–#26 closed; live ToolHive/Hermes evidence is #73/#74 |
| [CHG-0020](done/CHG-0020-toolhub-stage2/plan.md) | M2 stage 2 in progress: authenticated projection endpoint, ToolHive adapter contract, bounded CLI and workload lifecycle |
| [CHG-0018](done/CHG-0018-communication-runtime-migration/plan.md) | Issue19: per-context execution selection, migration audit and real five-minute gateway lifecycle |
| [CHG-0019](done/CHG-0019-toolhub-foundation/plan.md) | M2 complete: immutable ToolHub definitions, workload classification and exact-owner bindings; runtime unchanged |
| [CHG-0017](done/CHG-0017-stream-approval-reconciliation/plan.md) | Issues 16–18 complete: repository and pinned Hermes Docker gates passed; GitHub issues closed |
| [CHG-0016](done/CHG-0016-scale-to-zero-runtimes/plan.md) | Initial host supervisor, warm lifecycle and opt-in routing implemented; durable migration pending |
| [CHG-0015](done/CHG-0015-migration-map/plan.md) | Current-to-target migration and deprecation map |
| [CHG-0015-durable](done/CHG-0015-durable-job-runtime-mappings/plan.md) | Durable job and runtime mappings |
| [CHG-0014](done/CHG-0014-threat-model/plan.md) | Threat-model personal deployment and future company boundary |
| [CHG-0013](done/CHG-0013-dependency-refresh/plan.md) | Apply reviewed GitHub Actions and Docker dependency updates |
| [CHG-0012](done/CHG-0012-hermes-api-contract/plan.md) | Validate pinned Hermes API contract for persistent sessions and runs |
| [CHG-0011](done/CHG-0011-stable-identifiers/plan.md) | Stable identity and ownership identifier contract |
| [CHG-0010](done/CHG-0010-direct-mcp-atlassian/plan.md) | Replace Atlassian Rovo with pinned direct mcp-atlassian |
| [CHG-0009](done/CHG-0009-scoped-homes-and-service-split/plan.md) | Scoped homes and communication-hub split ready for deployment acceptance |
| [CHG-0008](done/CHG-0008-telegram-service-self-service/plan.md) | Telegram connector catalog and self-service enablement implemented |
| [CHG-0007](done/CHG-0007-communication-gateway/plan.md) | Communication gateway implemented; local gates passed, live Telegram acceptance pending |
| [CHG-0006](done/CHG-0006-work-services/plan.md) | Work-service integrations implemented |
| [CHG-0005](done/CHG-0005-self-env/plan.md) | User self-env and independent Telegram paths implemented |
| [CHG-0004](done/CHG-0004-multitenant-knowledge/plan.md) | Organization/user scope overlay implemented |
| [CHG-0001-bootstrap](done/CHG-0001-bootstrap/plan.md) | Implemented; local gates passed |
| [CHG-0002](done/CHG-0002-standalone/plan.md) | Completed; local checks passed |
| [CHG-0003](done/CHG-0003-go-first-tooling/plan.md) | Go-first tooling in progress |
