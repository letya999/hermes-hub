# Specifications

The **Status** column is authoritative. Specs marked *superseded* or *answered*
stay in `active/` so existing links keep working — their headers carry the same
note. Never implement requirements from a superseded spec without reading the
record it points to. `archive/` holds only pre-renumbering originals.

| Specification | Status |
|---|---|
| [SPEC-0083](active/SPEC-0083-agent-skills-platform.md) | Frozen: agent skills platform — store/effective view, default-deny natives, security pipeline, tools, disclosure, resources, secrets, profiles, git installs, evolution, quality, acceptance (M24 #356–#368) |
| [SPEC-0081](active/SPEC-0081-shared-acquisition.md) | Frozen: shared source acquisition — boundary-keyed pool, conditional HTTP, cursors/gap visibility, per-event filters, hook intake (M23 #332) |
| [SPEC-0082](active/SPEC-0082-image-role-budgets.md) | Frozen: image role budgets — per-image size SLOs, forbidden binaries, hygiene, voice consolidation, hub-core split, browser sidecar, acceptance (M26 #371–#380) |
| [SPEC-0080](active/SPEC-0080-standing-assignments.md) | Frozen: standing assignments — durable owner intent, plan reconcile, fenced wakes, messenger control (M23 #324) |
| [SPEC-0079](active/SPEC-0079-user-scoped-site-hosting.md) | Frozen: user-scoped hosting — static/dynamic isolation, publication, quotas, artifact access and durable lifecycle; CHG-0170–0174 (M18 #147) |
| [SPEC-0078](active/SPEC-0078-per-user-dns-tls.md) | Frozen: per-user subdomains — provider-neutral DNS contract, four profiles, per-user wildcard via DNS-01 to sish, Broker credentials (M18 #146) |
| [SPEC-0077](active/SPEC-0077-outbound-exposure.md) | Frozen: outbound exposure primitive — named endpoints, per-binding tunnel workloads, Broker credentials, sish/cloudflared profiles, bounds and audit (M18 #145) |
| [SPEC-0076](active/SPEC-0076-ops-admin-delivery.md) | Frozen: usage export, space manifests, watch, image rollout pins, admin diagnostics, incident replay (M17) |
| [SPEC-0075](active/SPEC-0075-operational-protection.md) | Draft: M17 operational protection and code closeout |
| [SPEC-0074](active/SPEC-0074-goal-mode.md) | Draft: persistent /goal mode — dual engine, judge modes, spool continuation |
| [SPEC-0073](active/SPEC-0073-manual-compaction.md) | Draft: manual compaction — dual engine, upstream semantics, turn-boundary safety |
| [SPEC-0072](active/SPEC-0072-compression-policy-status.md) | Draft: automatic compaction policy, relay routing, hub sweep, honest status |
| [SPEC-0071](active/SPEC-0071-session-runtime-discipline.md) | Frozen: session & runtime discipline (renumbered from a duplicate SPEC-0069) |
| [SPEC-0070](active/SPEC-0070-model-provider-completion.md) | Frozen: model & provider layer completion (renumbered from a duplicate SPEC-0069) |
| [SPEC-0069](active/SPEC-0069-connector-recovery-and-system-guide.md) | Frozen: connector recovery, access, inventory and mandatory guide delivery |
| [SPEC-0068](active/SPEC-0068-dynamic-session-contract.md) | Frozen: bounded owner-only dynamic JSON sessions |
| [SPEC-0067](active/SPEC-0067-recipe-oauth.md) | Frozen: recipe-driven OAuth installation |
| [SPEC-0066](active/SPEC-0066-credential-broker-effective-contract.md) | Frozen: broker effective contract on request — extras and demote (imported from services/credential-broker) |
| [SPEC-0065](active/SPEC-0065-credential-broker-dynamic-contracts.md) | Frozen: broker dynamic contract registration via control API (imported from services/credential-broker) |
| [SPEC-0064](active/SPEC-0064-credential-broker-v1.md) | Frozen: credential broker MVP contract — requirements traced to executable tests (imported from services/credential-broker; hash-frozen in services/credential-broker/specs/baseline.json) |
| [SPEC-0063](active/SPEC-0063-cli-policy-completion.md) | Frozen: six CLI policy gaps, private package installation and atomic host catalog operation (renumbered from a duplicate SPEC-0048) |
| [SPEC-0062](active/SPEC-0062-toolhub-cli-transport.md) | Frozen: bounded CLI as a first-class ToolHub transport — catalog and user-owned, no shell, real isolation receipts (milestone 9; CHG-0077; renumbered from a duplicate SPEC-0047) |
| [SPEC-0061](active/SPEC-0061-media-generation-service.md) | hub-media generation sidecar (renumbered from a duplicate SPEC-0040) |
| [SPEC-0060](active/SPEC-0060-recipe-source-ledger.md) | Frozen: content-addressed connector recipes, ranked source ledgers, trust tiers, golden corpus |
| [SPEC-0059](active/SPEC-0059-watcher-probe-pipeline.md) | Frozen: watcher fetch/extract pipeline, isolated script/command probes, snapshotted recipes |
| [SPEC-0058](active/SPEC-0058-routine-destinations.md) | Frozen: routine destination discovery, binding, edits and history |
| [SPEC-0057](active/SPEC-0057-compact-routine-tools.md) | Frozen: five-tool routine interface amendment |
| [SPEC-0056](active/SPEC-0056-routine-lifecycle.md) | Frozen: owner routine lifecycle and interval windows |
| [SPEC-0055](active/SPEC-0055-routine-occurrence-recovery.md) | Active amendment: routine coalescing, fault isolation and bounded occurrence detail |
| [SPEC-0054](active/SPEC-0054-proactive-automation.md) | Frozen: verified follow-ups, watcher notification policy, control-plane tool calls, owner brief and event subscriptions (M12) |
| [SPEC-0053](active/SPEC-0053-execution-chain-reliability.md) | Frozen: M19 execution chain SLOs, stop-loss, tool surface, latency, recovery and visibility requirements |
| [SPEC-0052](active/SPEC-0052-telegram-operation-contracts.md) | Frozen: Telegram image, watcher, reminder, approval and exact-file acceptance; albums are a subsequent slice |
| [SPEC-0051](active/SPEC-0051-deployment-contracts.md) | Frozen: environment, provider and Docker deployment contracts |
| [SPEC-0050](active/SPEC-0050-m10-stabilization.md) | Frozen: M10 remediation amendment |
| [SPEC-0049](active/SPEC-0049-operability.md) | Frozen: mock, additive apply and BuildKit discipline |
| [SPEC-0048](active/SPEC-0048-proactive-routines.md) | Frozen: watchers, checks, deterministic digests and knowledge |
| [SPEC-0047](active/SPEC-0047-chat-file-transfer.md) | Frozen: images and file transfer |
| [SPEC-0046](active/SPEC-0046-tool-governance.md) | Frozen: tool-governance inventory, scoped rules, expiring grants and multi-plane enforcement (issue 139; CHG-0076) |
| [SPEC-0045](active/SPEC-0045-remote-mcp.md) | Frozen: owner-scoped remote MCP registration, admission, header credentials and projection (issues 83–85, 96; CHG-0075) |
| [SPEC-0044](active/SPEC-0044-communication-telegram-auth.md) | Frozen: Communication Hub QR login and owner-bound Broker transfer; intake amended by CHG-0137 (compiled-in, default-on for telegram stacks) |
| [SPEC-0043](active/SPEC-0043-prepared-bundle.md) | Frozen: 12-ID bundle discovery, blocked evidence and personal Telegram admission |
| [SPEC-0042](active/SPEC-0042-capability-isolation-and-proof.md) | Frozen: execution isolation, file/shell boundaries and T01-T22 acceptance; implementation in CHG-0065 |
| [SPEC-0041](active/SPEC-0041-default-deny-capabilities.md) | Frozen successor to SPEC-0040 defaults: complete inventory, explicit profiles/grants, routing and revocation; implementation in CHG-0065 |
| [SPEC-0040](active/SPEC-0040-capability-profile.md) | Frozen earlier capability matrix; defaults/enforcement superseded by SPEC-0041/0042 |
| [SPEC-0039](active/SPEC-0039-media-sidecars.md) | Standalone hub-stt/hub-tts services (separate images): OpenAI-compatible API, swappable engines, durable async jobs for long recordings |
| [SPEC-0038](active/SPEC-0038-voice-readiness.md) | `/voice` reports STT and TTS readiness separately; live voice evidence is a deployment gate |
| [SPEC-0037](active/SPEC-0037-artifact-delivery.md) | Run artifact handoff: bounded runtime fetch endpoint, spool staging, ordered channel delivery |
| [SPEC-0036](active/SPEC-0036-channel-rendering.md) | Per-channel Markdown rendering, lossless multi-part replies, source document for degraded output |
| [SPEC-0035](active/SPEC-0035-web-research.md) | Configured web search/extract providers, runtime-secret keys, bounded deep-research skill |
| [SPEC-0034](active/SPEC-0034-user-diagnostics.md) | User-scoped diagnostics projection: store-derived scope, bounded queries, redaction |
| [SPEC-0033](active/SPEC-0033-document-image-profile.md) | Workspace document and image profile; pdf, xlsx, pptx, and webp; configurable image provider, model, and delivery |
| [SPEC-0032](active/SPEC-0032-ssh-capability.md) | Opt-in user-scoped SSH capability with pinned host keys and stacked read/write/shell/tunnel grants |
| [SPEC-0031](active/SPEC-0031-automatic-diagnostics.md) | Automatic bounded multiuser diagnostics; supersedes SPEC-0030 export |
| [SPEC-0030](active/SPEC-0030-diagnostics.md) | Local multiuser diagnostics and bounded Docker logs; export superseded by SPEC-0031 |
| [SPEC-0029](active/SPEC-0029-build-disk-boundary.md) | Core/control image boundary and bounded rebuild storage |
| [SPEC-0028](active/SPEC-0028-repository-driven-prepared-connectors.md) | Generic exact-source catalog, Broker state, discovery and four-connector acceptance |
| [SPEC-0027](active/SPEC-0027-toolhive-vmcp-evaluation.md) | ToolHive local vMCP acceptance boundary and no-go result |
| [SPEC-0026](active/SPEC-0026-mcp-recipe-resolver.md) | Secret-free MCP Launch/Connection Recipe resolution over the existing M5.3 onboarding flow |
| [SPEC-0025](active/SPEC-0025-credential-broker-boundary.md) | Credential Broker monorepo placement and adapter gate; opt-in closed by the ADR-0022 amendment (Broker is the single credential authority) |
| [SPEC-0024](active/SPEC-0024-m53-hermes-onboarding.md) | M5.3 Hermes-mediated GitHub MCP onboarding and live acceptance contract |
| [SPEC-0023](active/SPEC-0023-toolhub-control-plane.md) | M5.2 multi-tenant ToolHub control plane: grants, control MCP, elicitation and lifecycle |
| [SPEC-0022](active/SPEC-0022-personal-data-connectors.md) | M5 stage 2 personal data connectors and independent capability contracts |
| [SPEC-0021](active/SPEC-0021-personal-assistant.md) | Personal assistant channels, voice, hub routines and context lifecycle |
| [SPEC-0020](active/SPEC-0020-credentials-and-connectors.md) | Encrypted credentials, inject-after-authorize, OAuth broker and audit ledger |
| [SPEC-0019](active/SPEC-0019-toolhub-stage3.md) | Dynamic ToolHub catalog, projection revision, enforcement receipt and explicit migration |
| [SPEC-0018](active/SPEC-0018-toolhub-stage2.md) | Dynamic ToolHub endpoint, private MCP backend adapter, bounded CLI and workload lifecycle |
| [SPEC-0017](active/SPEC-0017-toolhub-foundation.md) | Versioned ToolHub definitions, workload classes, scoped connections and effective bindings |
| [SPEC-0016](active/SPEC-0016-communication-execution-migration.md) | Per-context communication execution rollout and rollback; idle default superseded by 30-min warm TTL (SPEC-0053) |
| [SPEC-0015](active/SPEC-0015-run-controls.md) | Durable cancellation and approval ownership, dispatch and expiry |
| [SPEC-0014](active/SPEC-0014-job-runtime-mappings.md) | Durable job, session, run and runtime mappings |
| [SPEC-0013](active/SPEC-0013-routines-and-runtime-wake.md) | Hub-owned routines wake sleeping context runtimes |
| [SPEC-0012](active/SPEC-0012-scale-to-zero-runtime-lifecycle.md) | Scale-to-zero lifecycle for isolated context runtimes; idle default superseded by 30-min warm TTL (SPEC-0053) |
| [SPEC-0011](active/SPEC-0011-hermes-api-contract.md) | Pinned Hermes API contract for sessions and runs |
| [SPEC-0010](active/SPEC-0010-stable-identifiers.md) | Stable identity, context, runtime, conversation and connection identifiers |
| [SPEC-0009](active/SPEC-0009-scoped-homes-and-service-boundaries.md) | Implemented scoped homes and communication-hub boundary; migration remains explicit |
| [SPEC-0008](active/SPEC-0008-telegram-service-self-service.md) | Telegram connector catalog and self-service enablement |
| [SPEC-0007](active/SPEC-0007-communication-gateway.md) | Communication gateway and scoped Hermes jobs |
| [SPEC-0006](active/SPEC-0006-work-services.md) | Atlassian, GitLab CLI and Google access boundaries |
| [SPEC-0005](active/SPEC-0005-self-env-and-telegram-boundaries.md) | User self-env and independent Telegram paths |
| [SPEC-0004](active/SPEC-0004-organization-user-scope.md) | Organization/user scope overlay |
| [SPEC-0003](active/SPEC-0003-go-first-tooling.md) | Current Go-first tooling requirements |
| [SPEC-0002](active/SPEC-0002-standalone.md) | Baseline 0.2.0; items 9-10 superseded by SPEC-0003 |
| [SPEC-0001](archive/SPEC-0001-hub.md) | Historical 0.1.0, superseded |
