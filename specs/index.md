# Specifications

The **Status** column is authoritative. Specs marked *superseded* or *answered*
stay in `active/` so existing links keep working — their headers carry the same
note. Never implement requirements from a superseded spec without reading the
record it points to. `archive/` holds only pre-renumbering originals.

| Specification | Status |
|---|---|
| [SPEC-0102](active/SPEC-0102-v1-proactive-modes-eval.md) | Active: M40 proactive-engine, action modes and eval thresholds (ADR-0108/0109; CHG-0344..0349, 0352) |
| [SPEC-0101](active/SPEC-0101-v1-execution-slos.md) | Active: M40 execution and provisioning SLOs — spawn-controller, instant spaces, task-engine, runtimes, capacity (ADR-0107; CHG-0333..0336, 0351, 0353) |
| [SPEC-0100](active/SPEC-0100-v1-contracts.md) | Active: M40 v1 contracts and master exit-criteria trace — topology, contract pattern, dependency gate, single owners, fail-closed config, run tokens, security baseline (ADR-0105/0106; CHG-0329..0332) |
| [SPEC-0099](active/SPEC-0099-m32-automation-slices.md) | Delivered: #521–#525 engine separation, inactive host preparation, single owner cutover, dreaming; refines SPEC-0094 R3/R4 |
| [SPEC-0098](active/SPEC-0098-m32-memory-slices.md) | Delivered: #513–#520 vault, exact-run, capture, memory APIs, confirmed edits, OpenKnowledge; refines SPEC-0094 R1/R2 |
| [SPEC-0097](active/SPEC-0097-m32-config-slices.md) | Delivered: #508–#512 passport authority, scopes, selection, activation and recovery; refines SPEC-0095 |
| [SPEC-0096](active/SPEC-0096-m32-foundation.md) | Superseded → SPEC-0097/0098/0099: unexecuted plan removed by ADR-0097 amendment; current integration flow in CHG-0281 parallel-work.md |
| [SPEC-0095](active/SPEC-0095-authored-passports-rights.md) | Delivered: six authored passport inputs, no legacy config path, independent tools and grants, publisher authorization and cutover |
| [SPEC-0094](active/SPEC-0094-toolhub-memory-automation.md) | Delivered: ToolHub-owned per-user memory, routines, assignments, watches and dreaming; authenticated capture and exact-run authorization |
| [SPEC-0093](active/SPEC-0093-consolidated-model-tool-surface.md) | Delivered: M31 catalog r5 — toolset dictionary, via/executor routing, audiences, fail-closed requires, envelope, verification suite (ADR-0094) |
| [SPEC-0092](active/SPEC-0092-imperative-passports-capability-grants.md) | Delivered: M30 normative v2 passport shapes, rights vocabulary + PDP/PEP, apply/reconcile effect classes, per-service cliproxy sessions, live acceptance (ADR-0093); supersedes SPEC-0091 body-shape/vocabulary clauses |
| [SPEC-0091](active/SPEC-0091-six-passport-space-contract.md) | Delivered: M29 six-passport MVP, catalogs/templates, brokered model sessions, ToolHub/grants, prompt, migration and live acceptance (ADR-0092); partly superseded → SPEC-0092 |
| [SPEC-0090](active/SPEC-0090-root-services-sqlite.md) | Delivered: SQLite storage for ToolHub/Communication + shared migration contract for all three services (ADR-0091, CHG-0215–0226 — delivered 2026-11-15); supersedes SPEC-0088 substrate clauses for the two services |
| [SPEC-0089](active/SPEC-0089-conversation-parallel-lanes.md) | Active: parallel sessions inside one context — lane per conversation, fairness per context, per-context cap below upstream (ADR-0090, CHG-0234); supersedes SPEC-0012 rule 4 |
| [SPEC-0088](active/SPEC-0088-bounded-control-journals.md) | Active: bounded root-plane journals, receipts, diagnostics and maintenance; partially landed via CHG-0219/0220/0237/0239; remaining slices delivered 2026-11-15 (live-acceptance legs recorded in CHG state); substrate for ToolHub/Communication superseded by SPEC-0090 |
| [SPEC-0087](active/SPEC-0087-broker-bounded-storage.md) | Delivered: Broker recovery, SQLCipher proof, normalized state/audit and provider lifecycle; live-acceptance legs recorded in CHG state |
| [SPEC-0086](active/SPEC-0086-standing-assignment-mvp.md) | Delivered: executable standing-assignment MVP — cursor replay, filtered specialist wakes, durable progress and acceptance (M23 #324/#332) |
| [SPEC-0085](active/SPEC-0085-memory-vault-openknowledge.md) | Delivered: M20 memory vault (leaves, cards, summaries, CORE, rollup, search, write contract) and opt-in OpenKnowledge surface (renumbered from a duplicate SPEC-0070) |
| [SPEC-0084](active/SPEC-0084-agent-skills-platform.md) | Delivered: agent skills platform — store/effective view, default-deny natives, security pipeline, tools, disclosure, resources, secrets, profiles, git installs, evolution, quality, acceptance (M24 #356–#368) |
| [SPEC-0083](active/SPEC-0083-browser-sidecar-lifecycle.md) | Active: browser sidecar lifecycle — resource ceiling, spawn auto-start, idle sleep after grace (ADR-0083 D8/D9, CHG-0190) |
| [SPEC-0081](active/SPEC-0081-shared-acquisition.md) | Active: shared source acquisition — boundary-keyed pool, conditional HTTP, cursors/gap visibility, per-event filters, hook intake (M23 #332) |
| [SPEC-0082](active/SPEC-0082-image-role-budgets.md) | Active: image role budgets — per-image size SLOs, forbidden binaries, hygiene, voice consolidation, hub-core split, browser sidecar, acceptance (M26 #371–#380) |
| [SPEC-0080](active/SPEC-0080-standing-assignments.md) | Active: standing assignments — durable owner intent, plan reconcile, fenced wakes, messenger control (M23 #324) |
| [SPEC-0079](active/SPEC-0079-user-scoped-site-hosting.md) | Delivered: user-scoped hosting — static/dynamic isolation, publication, quotas, artifact access and durable lifecycle; CHG-0170–0174 (M18 #147) |
| [SPEC-0078](active/SPEC-0078-per-user-dns-tls.md) | Delivered: per-user subdomains — provider-neutral DNS contract, four profiles, per-user wildcard via DNS-01 to sish, Broker credentials (M18 #146) |
| [SPEC-0077](active/SPEC-0077-outbound-exposure.md) | Delivered: outbound exposure primitive — named endpoints, per-binding tunnel workloads, Broker credentials, sish/cloudflared profiles, bounds and audit (M18 #145) |
| [SPEC-0076](active/SPEC-0076-ops-admin-delivery.md) | Delivered: usage export, space manifests, watch, image rollout pins, admin diagnostics, incident replay (M17) |
| [SPEC-0075](active/SPEC-0075-operational-protection.md) | Draft: M17 operational protection and code closeout |
| [SPEC-0074](active/SPEC-0074-goal-mode.md) | Draft: persistent /goal mode — dual engine, judge modes, spool continuation |
| [SPEC-0073](active/SPEC-0073-manual-compaction.md) | Draft: manual compaction — dual engine, upstream semantics, turn-boundary safety |
| [SPEC-0072](active/SPEC-0072-compression-policy-status.md) | Draft: automatic compaction policy, relay routing, hub sweep, honest status |
| [SPEC-0071](active/SPEC-0071-session-runtime-discipline.md) | Active: session & runtime discipline (renumbered from a duplicate SPEC-0069) |
| [SPEC-0070](active/SPEC-0070-model-provider-completion.md) | Active: model & provider layer completion (renumbered from a duplicate SPEC-0069) |

| [SPEC-0069](active/SPEC-0069-connector-recovery-and-system-guide.md) | Active: connector recovery, access, inventory and mandatory guide delivery |
| [SPEC-0068](active/SPEC-0068-dynamic-session-contract.md) | Active: bounded owner-only dynamic JSON sessions |
| [SPEC-0067](active/SPEC-0067-recipe-oauth.md) | Active: recipe-driven OAuth installation |
| [SPEC-0066](active/SPEC-0066-credential-broker-effective-contract.md) | Active: broker effective contract on request — extras and demote (imported from services/credential-broker) |
| [SPEC-0065](active/SPEC-0065-credential-broker-dynamic-contracts.md) | Active: broker dynamic contract registration via control API (imported from services/credential-broker) |
| [SPEC-0064](active/SPEC-0064-credential-broker-v1.md) | Active: credential broker MVP contract — requirements traced to executable tests (imported from services/credential-broker; hash-frozen in services/credential-broker/specs/baseline.json) |
| [SPEC-0063](active/SPEC-0063-cli-policy-completion.md) | Active: six CLI policy gaps, private package installation and atomic host catalog operation (renumbered from a duplicate SPEC-0048) |
| [SPEC-0062](active/SPEC-0062-toolhub-cli-transport.md) | Active: bounded CLI as a first-class ToolHub transport — catalog and user-owned, no shell, real isolation receipts (milestone 9; CHG-0077; renumbered from a duplicate SPEC-0047) |
| [SPEC-0061](active/SPEC-0061-media-generation-service.md) | Delivered: hub-media generation sidecar (renumbered from a duplicate SPEC-0040) |
| [SPEC-0060](active/SPEC-0060-recipe-source-ledger.md) | Active: content-addressed connector recipes, ranked source ledgers, trust tiers, golden corpus |
| [SPEC-0059](active/SPEC-0059-watcher-probe-pipeline.md) | Active: watcher fetch/extract pipeline, isolated script/command probes, snapshotted recipes |
| [SPEC-0058](active/SPEC-0058-routine-destinations.md) | Active: routine destination discovery, binding, edits and history |
| [SPEC-0057](active/SPEC-0057-compact-routine-tools.md) | Active: five-tool routine interface amendment |
| [SPEC-0056](active/SPEC-0056-routine-lifecycle.md) | Active: owner routine lifecycle and interval windows |
| [SPEC-0055](active/SPEC-0055-routine-occurrence-recovery.md) | Active: routine coalescing, fault isolation and bounded occurrence detail |
| [SPEC-0054](active/SPEC-0054-proactive-automation.md) | Active: verified follow-ups, watcher notification policy, control-plane tool calls, owner brief and event subscriptions (M12) |
| [SPEC-0053](active/SPEC-0053-execution-chain-reliability.md) | Active: M19 execution chain SLOs, stop-loss, tool surface, latency, recovery and visibility requirements |
| [SPEC-0052](active/SPEC-0052-telegram-operation-contracts.md) | Active: Telegram image, watcher, reminder, approval and exact-file acceptance; albums are a subsequent slice |
| [SPEC-0051](active/SPEC-0051-deployment-contracts.md) | Active: environment, provider and Docker deployment contracts |
| [SPEC-0050](active/SPEC-0050-m10-stabilization.md) | Active: M10 remediation amendment |
| [SPEC-0049](active/SPEC-0049-operability.md) | Active: mock, additive apply and BuildKit discipline |
| [SPEC-0048](active/SPEC-0048-proactive-routines.md) | Active: watchers, checks, deterministic digests and knowledge |
| [SPEC-0047](active/SPEC-0047-chat-file-transfer.md) | Active: images and file transfer |
| [SPEC-0046](active/SPEC-0046-tool-governance.md) | Active: tool-governance inventory, scoped rules, expiring grants and multi-plane enforcement (issue 139; CHG-0076) |
| [SPEC-0045](active/SPEC-0045-remote-mcp.md) | Active: owner-scoped remote MCP registration, admission, header credentials and projection (issues 83–85, 96; CHG-0075) |
| [SPEC-0044](active/SPEC-0044-communication-telegram-auth.md) | Active: Communication Hub QR login and owner-bound Broker transfer; intake amended by CHG-0137 (compiled-in, default-on for telegram stacks) |
| [SPEC-0043](active/SPEC-0043-prepared-bundle.md) | Active: 12-ID bundle discovery, blocked evidence and personal Telegram admission |
| [SPEC-0042](active/SPEC-0042-capability-isolation-and-proof.md) | Active: execution isolation, file/shell boundaries and T01-T22 acceptance; implementation in CHG-0065 |
| [SPEC-0041](active/SPEC-0041-default-deny-capabilities.md) | Active: complete inventory, explicit profiles/grants, routing and revocation; implementation in CHG-0065 |
| [SPEC-0040](active/SPEC-0040-capability-profile.md) | Active: Frozen earlier capability matrix; defaults/enforcement superseded by SPEC-0041/0042 |
| [SPEC-0039](active/SPEC-0039-media-sidecars.md) | Delivered: standalone hub-stt/hub-tts services (separate images): OpenAI-compatible API, swappable engines, durable async jobs for long recordings |
| [SPEC-0038](active/SPEC-0038-voice-readiness.md) | Delivered: `/voice` reports STT and TTS readiness separately; live voice evidence is a deployment gate |
| [SPEC-0037](active/SPEC-0037-artifact-delivery.md) | Delivered: run artifact handoff: bounded runtime fetch endpoint, spool staging, ordered channel delivery |
| [SPEC-0036](active/SPEC-0036-channel-rendering.md) | Delivered: per-channel Markdown rendering, lossless multi-part replies, source document for degraded output |
| [SPEC-0035](active/SPEC-0035-web-research.md) | Delivered: configured web search/extract providers, runtime-secret keys, bounded deep-research skill |
| [SPEC-0034](active/SPEC-0034-user-diagnostics.md) | Delivered: user-scoped diagnostics projection: store-derived scope, bounded queries, redaction |
| [SPEC-0033](active/SPEC-0033-document-image-profile.md) | Delivered: workspace document and image profile; pdf, xlsx, pptx, and webp; configurable image provider, model, and delivery |
| [SPEC-0032](active/SPEC-0032-ssh-capability.md) | Delivered: opt-in user-scoped SSH capability with pinned host keys and stacked read/write/shell/tunnel grants |
| [SPEC-0031](active/SPEC-0031-automatic-diagnostics.md) | Delivered: automatic bounded multiuser diagnostics; supersedes SPEC-0030 export |
| [SPEC-0030](active/SPEC-0030-diagnostics.md) | Partly superseded → SPEC-0031: local multiuser diagnostics and bounded Docker logs; export superseded by SPEC-0031 |
| [SPEC-0029](active/SPEC-0029-build-disk-boundary.md) | Delivered: core/control image boundary and bounded rebuild storage |
| [SPEC-0028](active/SPEC-0028-repository-driven-prepared-connectors.md) | Delivered: generic exact-source catalog, Broker state, discovery and four-connector acceptance |
| [SPEC-0027](active/SPEC-0027-toolhive-vmcp-evaluation.md) | Delivered: toolHive local vMCP acceptance boundary and no-go result |
| [SPEC-0026](active/SPEC-0026-mcp-recipe-resolver.md) | Delivered: secret-free MCP Launch/Connection Recipe resolution over the existing M5.3 onboarding flow |
| [SPEC-0025](active/SPEC-0025-credential-broker-boundary.md) | Delivered: credential Broker monorepo placement and adapter gate; opt-in closed by the ADR-0022 amendment (Broker is the single credential authority) |
| [SPEC-0024](active/SPEC-0024-m53-hermes-onboarding.md) | Delivered: m5.3 Hermes-mediated GitHub MCP onboarding and live acceptance contract |
| [SPEC-0023](active/SPEC-0023-toolhub-control-plane.md) | Delivered: m5.2 multi-tenant ToolHub control plane: grants, control MCP, elicitation and lifecycle |
| [SPEC-0022](active/SPEC-0022-personal-data-connectors.md) | Delivered: m5 stage 2 personal data connectors and independent capability contracts |
| [SPEC-0021](active/SPEC-0021-personal-assistant.md) | Delivered: personal assistant channels, voice, hub routines and context lifecycle |
| [SPEC-0020](active/SPEC-0020-credentials-and-connectors.md) | Delivered: encrypted credentials, inject-after-authorize, OAuth broker and audit ledger |
| [SPEC-0019](active/SPEC-0019-toolhub-stage3.md) | Delivered: dynamic ToolHub catalog, projection revision, enforcement receipt and explicit migration |
| [SPEC-0018](active/SPEC-0018-toolhub-stage2.md) | Delivered: dynamic ToolHub endpoint, private MCP backend adapter, bounded CLI and workload lifecycle |
| [SPEC-0017](active/SPEC-0017-toolhub-foundation.md) | Delivered: versioned ToolHub definitions, workload classes, scoped connections and effective bindings |
| [SPEC-0016](active/SPEC-0016-communication-execution-migration.md) | Delivered: per-context communication execution rollout and rollback; idle default superseded by 30-min warm TTL (SPEC-0053) |
| [SPEC-0015](active/SPEC-0015-run-controls.md) | Delivered: durable cancellation and approval ownership, dispatch and expiry |
| [SPEC-0014](active/SPEC-0014-job-runtime-mappings.md) | Delivered: durable job, session, run and runtime mappings |
| [SPEC-0013](active/SPEC-0013-routines-and-runtime-wake.md) | Delivered: hub-owned routines wake sleeping context runtimes |
| [SPEC-0012](active/SPEC-0012-scale-to-zero-runtime-lifecycle.md) | Partly superseded → SPEC-0089/SPEC-0053: scale-to-zero lifecycle for isolated context runtimes; idle default → 30-min warm TTL (SPEC-0053); rule 4 → per-conversation lanes (SPEC-0089) |
| [SPEC-0011](active/SPEC-0011-hermes-api-contract.md) | Delivered: pinned Hermes API contract for sessions and runs |
| [SPEC-0010](active/SPEC-0010-stable-identifiers.md) | Delivered: stable identity, context, runtime, conversation and connection identifiers |
| [SPEC-0009](active/SPEC-0009-scoped-homes-and-service-boundaries.md) | Delivered: Implemented scoped homes and communication-hub boundary; migration remains explicit |
| [SPEC-0008](active/SPEC-0008-telegram-service-self-service.md) | Delivered: telegram connector catalog and self-service enablement |
| [SPEC-0007](active/SPEC-0007-communication-gateway.md) | Delivered: communication gateway and scoped Hermes jobs |
| [SPEC-0006](active/SPEC-0006-work-services.md) | Delivered: atlassian, GitLab CLI and Google access boundaries |
| [SPEC-0005](active/SPEC-0005-self-env-and-telegram-boundaries.md) | Delivered: user self-env and independent Telegram paths |
| [SPEC-0004](active/SPEC-0004-organization-user-scope.md) | Delivered: organization/user scope overlay |
| [SPEC-0003](active/SPEC-0003-go-first-tooling.md) | Delivered: current Go-first tooling requirements |
| [SPEC-0002](active/SPEC-0002-standalone.md) | Partly superseded → SPEC-0003: baseline 0.2.0; items 9-10 superseded by SPEC-0003 |
| [SPEC-0001](archive/SPEC-0001-hub.md) | Superseded → SPEC-0002: historical 0.1.0 baseline |
