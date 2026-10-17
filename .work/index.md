# Change plans

Use todo / in-progress / done directories for active change folders. A folder contains
plan.md and state.yaml. Task progress belongs here, never in current architecture docs.
New milestones follow the [milestone playbook](milestone-playbook.md) and its [templates](templates/change-plan.md).

The table tracks current change records. Issue 122's broader default-deny design
is CHG-0065; its implementation is in progress. Issues 167-169 (task sessions,
per-task style, measured usage) shipped as CHG-0066/0067/0068 and closed
2026-10-06. Live deployment follow-ups
also remain in issues #73 (ToolHive/VPS), #74 (Hermes reconnect) and #166/#170/#172
(live channel acceptance evidence).

| Change | State |
|---|---|
| [CHG-0112](in-progress/CHG-0112-routine-lifecycle/plan.md) | Full routine lifecycle and interval windows; implementation and managed acceptance in progress |
| [CHG-0111](done/CHG-0111-durable-reminder-acceptance/plan.md) | Done: issue #247 reminder recovery, snooze, history isolation and real Telegram acceptance; just check passed |
| [CHG-0110](in-progress/CHG-0110-routine-recovery/plan.md) | Routine tick recovery, record isolation and bounded occurrence history; verification in progress |
| [CHG-0109](in-progress/CHG-0109-fast-local-rebuild/plan.md) | Fast local Go service rebuild, in progress |
| [CHG-0106](done/CHG-0106-live-replay-defects/plan.md) | Done: live-replay defect fixes — watcher probes, session leak routing, contract errors, retry, broker catalog; live verified |
| [CHG-0107](done/CHG-0107-dynamic-contracts/plan.md) | Done: dynamic credential contracts (POST /v1/contracts + ToolHub synthesis) + live-verified rotate/self-service on google_workspace_mcp |
| [CHG-0105](done/CHG-0105-broker-drift-self-heal/plan.md) | Done: live-smoke fixes — broker drift self-heal, agent tool guidance, stale artifact reuse; live replay verified |
| [CHG-0104](in-progress/CHG-0104-event-streams/plan.md) | M12 follow-up: stream event subscriptions (delivery=stream) |
| [CHG-0103](in-progress/CHG-0103-event-subscriptions/plan.md) | M12 stage 5: event subscriptions, sinks, watch_* surface, telegram event tools |
| [CHG-0102](in-progress/CHG-0102-owner-brief/plan.md) | M12 stage 4: opt-in owner brief — deterministic snapshot, in-slot model synthesis |
| [CHG-0101](in-progress/CHG-0101-tool-call-probes/plan.md) | M12 stage 3: ToolHub /v1/tool-call + mcp_tool probes |
| [CHG-0100](in-progress/CHG-0100-watcher-policy/plan.md) | M12 stage 2: quiet window, daily cap, agent escalation |
| [CHG-0099](in-progress/CHG-0099-followup-hardening/plan.md) | M12 stage 1: bounded check retry, verdict text, task: probe, reminder evidence |
| [CHG-0099](in-progress/CHG-0099-m11-session-ux-closeout/plan.md) | M11 closeout: /stop+/cancel, /archive+/archived+/use restore with forum close/reopen, live progress, e2e runner in image — issues #217/#218/#219/#194/#193 |
| [CHG-0098](in-progress/CHG-0098-forum-topic-sessions/plan.md) | Issue #220: /new mints a bot-owned forum topic per session; delete/auto-title mirror it; unit green, Docker + live pending |
| [CHG-0097](done/CHG-0097-single-stack-supervisor/plan.md) | Done: single-stack supervisor verified live — container supervisor wake/restart, Windows tasks removed, final image rolled out |
| [CHG-0092](done/CHG-0092-chain-stop-the-bleeding/plan.md) | Done: M19 stage 0 verified live — idle deadlines, pre-admission retry, lease-safe lifecycle, supervisor service, routine isolation |
| [CHG-0093](done/CHG-0093-tool-surface-integrity/plan.md) | Done: M19 stage 1 accepted with waived live gates — rights-only policy hash, binding migration, tool manifest readiness, context hygiene |
| [CHG-0094](done/CHG-0094-chain-latency/plan.md) | Done: M19 stage 2 accepted with waived SLO samples — early liveness, single progress message, event-driven spool, lazy browser |
| [CHG-0095](done/CHG-0095-recovery-observability/plan.md) | Done: M19 stage 3 verified live — job state machine, unconfirmed reconciler, canary, structured logs, trace |
| [CHG-0096](done/CHG-0096-control-plane-simplification/plan.md) | Done: M19 stage 4 decision — container direction shipped in CHG-0097; store and Hermes pin proposals remain deferred |
| [CHG-0090](in-progress/CHG-0090-deployment-contracts/plan.md) | Deployment contracts and regressions implemented; source and Docker gates passed; busy local runtime restart scheduled |
| [CHG-0091](in-progress/CHG-0091-telegram-operation-contracts/plan.md) | Design ready for review: researched ADR-0035/SPEC-0052, code map, migration and acceptance cases; implementation not started |
| [CHG-0089](done/CHG-0089-routine-control-env/plan.md) | Done: routine control environment forwarding; real Telegram reminder delivered once without agent execution |
| [CHG-0088](done/CHG-0088-telegram-approval-lifecycle/plan.md) | Done: Telegram approval cleanup, current-request listing guidance and terminal-result preservation; deployed to local dev |
| [CHG-0087](in-progress/CHG-0087-live-phase1/plan.md) | Live phase 1 capability checks and observed terminal/runtime isolation repairs |
| [CHG-0086](done/CHG-0086-inbound-documents/plan.md) | Done: Telegram inbound document save and extraction repair; source, Docker, and live Telegram checks passed |
| [CHG-0085](in-progress/CHG-0085-e2e-ci/plan.md) | Sequential E2E registry and CI latency reduction, in progress |
| [CHG-0084](done/CHG-0084-m10-dev-merge/plan.md) | Done: M10 merged into dev, shared media image build fixed, final checks and local Docker rebuild/smoke passed |
| [CHG-0083](done/CHG-0083-m10-stabilization/plan.md) | M10 A–C stabilization implemented; local unit/security/Linux/Docker acceptance passed; live channel acceptance separate |
| [CHG-0082](in-progress/CHG-0082-m10-phase-c/plan.md) | M10 Phase C — operability (mock channel, apply -f, image discipline) |
| [CHG-0081](in-progress/CHG-0081-m10-phase-b/plan.md) | M10 Phase B — proactive assistant (watchers, check follow-ups, digest, knowledge) |
| [CHG-0080](in-progress/CHG-0080-m10-phase-a/plan.md) | M10 Phase A — cheap wins |
| [CHG-0079](done/CHG-0079-cli-policy-completion/plan.md) | Done: six CLI policy/catalog gaps, full checks and local dev deployment verified |
| [CHG-0078](done/CHG-0078-cli-cells/plan.md) | Done: per-principal sandbox cells (sibling containers, net none, warm ladder, toolbox mounts, shared-catalog grants) — issues #239–#242, live model verified |
| [CHG-0077](done/CHG-0077-toolhub-cli/plan.md) | Done: milestone 9 ToolHub CLI connectors — SPEC-0047, admission verifier, catalog defs, prepare_source cli, digest-pinned artifacts |
| [CHG-0076](in-progress/CHG-0076-tool-governance/plan.md) | Tool governance — inventory, scoped policy, grants, enforcement planes |
| [CHG-0075](in-progress/CHG-0075-remote-mcp/plan.md) | Milestone 8 issues 83/84/85/96: owner-scoped remote MCP register/admit/project through prepare_source remote_url |
| [CHG-0074](in-progress/CHG-0074-telegram-auth-plugin/plan.md) | Opt-in in-process Telegram QR login deployed locally; live account acceptance pending owner scan |
| [CHG-0074](in-progress/CHG-0074-host-hygiene/plan.md) | Host hygiene batch (issues 226→222→224→223→221+225→171→46) |
| [CHG-0073](in-progress/CHG-0073-prepared-bundle-telegram/plan.md) | Issues 39/115: 12-ID prepared bundle discovery, selected personal Telegram source and protected onboarding; verification in progress |
| [CHG-0072](in-progress/CHG-0072-managed-hotfixes/plan.md) | Managed-mode hotfixes from live Telegram verification |
| [CHG-0071](in-progress/CHG-0071-managed-voice-media-settings/plan.md) | Managed compression, settings tools, image gen, STT/TTS, second user |
| [CHG-0070](in-progress/CHG-0070-managed-cutover/plan.md) | Managed capability cutover for the local space |
| [CHG-0069](in-progress/CHG-0069-session-ux-and-quota/plan.md) | Session UX (new/list/switch/delete/archive/auto-title), forum topics, command inventory, provider quota in /usage — committed, live evidence pending |
| [CHG-0068](done/CHG-0068-usage-report/plan.md) | Issue 169 closed: measured `/usage` live-verified end-to-end 2026-10-06 |
| [CHG-0067](done/CHG-0067-task-style/plan.md) | Issue 168 closed: per-task `/style` admission-snapshotted; instructions channel live-verified |
| [CHG-0066](done/CHG-0066-task-sessions/plan.md) | Issue 167 closed: durable tasks + DM topics live on the deployed bot |
| [CHG-0065](in-progress/CHG-0065-capability-enforcement/plan.md) | Issue 122: accepted ADR-0031/0032, frozen SPEC-0041/0042; P0 probes and P1 grant hardening in progress |
| [CHG-0064](in-progress/CHG-0064-capability-profile/plan.md) | Issue 122 earlier matrix/name-reservation scope; broader enforcement continues in CHG-0065 |
| [CHG-0063](done/CHG-0063-media-generation-service/plan.md) | Done: hub-media sidecar for media generation, verified live |
| [CHG-0062](in-progress/CHG-0062-media-sidecars/plan.md) | Issue 170 redo: standalone hub-stt/hub-tts services (separate images), swappable engines, async long-audio jobs |
| [CHG-0061](in-progress/CHG-0061-voice-readiness/plan.md) | Issue 170: separate STT/TTS readiness in `/voice`, honest unavailable state |
| [CHG-0060](in-progress/CHG-0060-artifact-delivery/plan.md) | Issue 172: run artifact handoff and chat delivery — committed, live smoke pending |
| [CHG-0059](in-progress/CHG-0059-channel-rendering/plan.md) | Issue 166: per-channel Markdown rendering and lossless multi-part replies — committed, live smoke pending |
| [CHG-0058](in-progress/CHG-0058-web-search-research/plan.md) | Issue 123: web search and bounded deep research — merged to dev |
| [CHG-0057](in-progress/CHG-0057-datalens-connector/plan.md) | Prepared DataLens connector (issue 120) |
| [CHG-0056](done/CHG-0056-observability-connectors/plan.md) | Done: prepared observability connectors grafana + prometheus (issue 43) |
| [CHG-0055](in-progress/CHG-0055-sql-connectors/plan.md) | Prepared SQL connectors txttsql (primary) and dbhub (fallback) |
| [CHG-0054](done/CHG-0054-diagnostics-hardening/plan.md) | Diagnostics hardening: ingest redaction, failure isolation, throttles, per-principal caps — shipped (PR #158) |
| [CHG-0053](done/CHG-0053-user-diagnostics/plan.md) | Issue 125: user-scoped diagnostics control op — shipped (PR #155, #156) |
| [CHG-0052](in-progress/CHG-0052-document-image-tools/plan.md) | Workspace document and image capability; Gemini image calls use chat completions, images-endpoint models stay on /images; Fal and a rebuilt Hermes image still open |
| [CHG-0051](in-progress/CHG-0051-config-gated-empty-tools/plan.md) | Config-gated empty tools/list (mcp-atlassian) |
| [CHG-0050](done/CHG-0050-artifact-image-restore/plan.md) | Done: restore pruned artifact images at admission |
| [CHG-0049](done/CHG-0049-install-progress-notices/plan.md) | Done: install progress on the form and in Telegram |
| [CHG-0048](done/CHG-0048-network-transport-and-proxy-discovery/plan.md) | Done: network-transport fallback and proxy-env discovery |
| [CHG-0047](in-progress/CHG-0047-ssh-capability/plan.md) | Issue 42: opt-in user-scoped SSH capability (read/write/shell/tunnel) |
| [CHG-0047](in-progress/CHG-0047-mcp-admission-and-wake/plan.md) | Credential-gated MCP admission, sibling ToolHub tokens, and lost first-message delivery |
| [CHG-0046](in-progress/CHG-0046-automatic-diagnostics/plan.md) | Automatic bounded diagnostics across users and services |
| [CHG-0045](in-progress/CHG-0045-diagnostics/plan.md) | Bounded multiuser Docker diagnostics and Telegram/job correlation |
| [CHG-0044](in-progress/CHG-0044-build-disk-control/plan.md) | Separate core/control images and reclaim stale rebuild state |
| [CHG-0043](in-progress/CHG-0043-async-prepare/plan.md) | Asynchronous prepare_source — implemented |
| [CHG-0042](in-progress/CHG-0042-toolhub-build-resilience/plan.md) | ToolHub build resilience — implemented |
| [CHG-0041](done/CHG-0041-browser-profiles/plan.md) | Done: harden isolated user browser profiles (issue #45) |
| [CHG-0040](done/CHG-0040-shared-toolhub-control-plane/plan.md) | Done: shared ToolHub/Broker control plane — infra flag, shared network, multi-token endpoint |
| [CHG-0039](done/CHG-0039-parallel-context-workers/plan.md) | Done: parallel per-context gateway workers — no cross-user head-of-line blocking |
| [CHG-0039](in-progress/CHG-0039-oauth-handoff/plan.md) | OAuth handoff for prepared MCP connections |
| [CHG-0038](in-progress/CHG-0038-multiowner-telegram-runtime/plan.md) | Multi-owner Telegram runtime connectivity |
| [CHG-0037](in-progress/CHG-0037-toolhub-closeout/plan.md) | ToolHub closeout: Broker readiness, isolation, registry discovery, Hermes reconnect, prepared bundle and production acceptance |
| [CHG-0036](done/CHG-0036-repository-driven-toolhub/plan.md) | Done: repository-driven ToolHub — prepared data, discovery and four exact-source live scenarios verified |
| [CHG-0035](in-progress/CHG-0035-toolhive-vmcp-audit/plan.md) | ToolHive local vMCP evaluation: no-go evidence recorded |
| [CHG-0034](in-progress/CHG-0034-noncredential-env-filter/plan.md) | Non-credential env names excluded from connection recipe; gitlab-pat broker contract |
| [CHG-0033](in-progress/CHG-0033-node-bin-aliases/plan.md) | Node bin aliases to one file resolve as a single MCP entrypoint |
| [CHG-0032](done/CHG-0032-broker-direct-form/plan.md) | Done: Credential Broker direct form and expired-link regeneration |
| [CHG-0031](in-progress/CHG-0031-docker-reclaim/plan.md) | Docker reclaim: stale hub tags, dangling and artifact-build orphans removed by devcheck docker-clean |
| [CHG-0031](in-progress/CHG-0031-docker-disk-diagnosis/plan.md) | Docker build and disk diagnosis |
| [CHG-0030](in-progress/CHG-0030-compose-consolidation/plan.md) | Compose consolidation refactor |
| [CHG-0029](in-progress/CHG-0029-github-mcp-install/plan.md) | Safe generic GitHub MCP source review; cache mounts ignored as metadata, secret mounts remain denied |
| [CHG-0028](in-progress/CHG-0028-credential-broker-merge/plan.md) | Перенос Credential Broker и opt-in интеграция с Hermes Hub |
| [CHG-0027](in-progress/CHG-0027-m53-e2e/plan.md) | M5.3 Hermes-mediated MCP onboarding |
| [CHG-0027](done/CHG-0027-recipe-catalog-discovery/plan.md) | Done: complete recipe catalog discovery |
| [CHG-0026](in-progress/CHG-0026-user-binding-journey/plan.md) | M5.2 control MCP, grants, elicitation and bind/project/revoke |
| [CHG-0026](done/CHG-0026-recipe-resolver/plan.md) | Done: recipe resolver |
| [CHG-0025](done/CHG-0025-trusted-artifacts/plan.md) | **done** M5.1 trusted artifacts + local ToolHive; [closeout.md](done/CHG-0025-trusted-artifacts/closeout.md) |
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
