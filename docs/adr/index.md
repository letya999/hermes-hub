---
description: Architectural decisions and clearly marked proposals; append new records when changing direction.
last_verified: "2026-11-20"
---
# Decisions

| Record | Decision |
|---|---|
| [ADR-0112](ADR-0112-v1-secrets.md) | Proposed - M40.1 (CHG-0334), owner review pending; delivery M40.13: credential-broker the only secret owner, refs in runtimes and workers, boundary injection by cred-proxy or egress-gateway |
| [ADR-0111](ADR-0111-v1-state-machines.md) | Proposed - M40.1 (CHG-0334), owner review pending; delivery M40.2 and each service: one `Transition` in shared/fsm, history in the same transaction, deadline and sweeper per non-final state, `uncertain` never retried, no stored flag beside a state |
| [ADR-0110](ADR-0110-v1-storage.md) | Proposed - M40.1 (CHG-0334), owner review pending; delivery M40.2..M40.15: one database per service, SQLite default and Postgres by connection string, migrations under a lock, every row tenant-scoped, no `.json`/`.jsonl` state; amends ADR-0091 |
| [ADR-0109](ADR-0109-action-modes-owner-decisions.md) | Proposed - aligned 2026-11-20 by CHG-0334, owner review pending; delivery M40.7: effects read/write-local/write-external/critical, one allow/ask/deny evaluator, run origin owner/unprompted, OwnerDecision replacing five confirmation mechanisms; action modes moved to M41 |
| [ADR-0108](ADR-0108-proactive-engine.md) | Proposed - aligned 2026-11-20 by CHG-0334, owner review pending; delivery M40.10: proactive-engine owns Routine/Occurrence/Assignment/Goal/Watch, deterministic occurrence ids, jobs only through task-engine; listeners, ledgers and attention moved to M41; supersedes ADR-0041, partly supersedes ADR-0075/0082/0095 |
| [ADR-0107](ADR-0107-execution-spawn-task-runtime.md) | Proposed - aligned 2026-11-20 by CHG-0334, owner review pending; delivery M40.3/M40.4/M40.6: spawn-controller single container authority with warm pool, task-engine one job ledger, harness-runtime session slots and own workspace only; supersedes ADR-0040, partly supersedes ADR-0090 |
| [ADR-0106](ADR-0106-identity-layers-run-tokens.md) | Proposed - rewritten 2026-11-20 by CHG-0334, owner review pending; delivery M40.3/M40.5/M40.14: identity-hub owns principals, orgs, roles, channel links and run tokens; layers live in passports; one run token per run with `aud`; no signed policy bundles |
| [ADR-0105](ADR-0105-v1-topology.md) | Proposed - rewritten 2026-11-20 by CHG-0334, owner review pending; delivery M40.2..M40.13: v1 service model - one binary per service, core plus optional services with native fallback, zones as network and secret rules, profiles, launch backends, `trusted` flag; supersedes ADR-0046/0095, partly supersedes ADR-0026/0094 |
| [ADR-0104](ADR-0104-external-firecracker-runtime.md) | Accepted — delivered M9 (CHG-0322): external Firecracker microVM backend behind an Uno-style control plane; relays stay host-side, state rides a per-box share disk |
| [ADR-0103](ADR-0103-optional-sandbox-runtime.md) | Accepted — delivered M9 (CHG-0320): optional runc/runsc/Kata selection, private relay discovery and no downgrade; Kata qualification remains node-specific |
| [ADR-0102](ADR-0102-kubernetes-managed-network-boundary.md) | Accepted — delivered M9 (CHG-0319): managed Kubernetes preflight with applied-policy receipt and separate relay Pod network boundary |
| [ADR-0101](ADR-0101-optional-kubernetes-runtime.md) | Accepted — delivered M9 (CHG-0319): optional Kubernetes runtime backend, retained per-space PVCs, UID-fenced ownership; Docker remains default |
| [ADR-0100](ADR-0100-verified-principal-boundary.md) | Accepted — delivered M9 (CHG-0313): verified-principal boundary and provider-independent identity (M9 #52) |
| [ADR-0099](ADR-0099-connector-slot-fsm.md) | Accepted: one live onboarding per owner/definition, four-stage lifecycle plus a single gate slot, derived you_must/delivery contract, channel-confirmed delivery |
| [ADR-0098](ADR-0098-durable-connector-state.md) | Accepted: canonical onboarding, Broker event reconciliation, versioned durable notices and incremental SQLite registry commits |
| [ADR-0097](ADR-0097-m32-bounded-delivery.md) | Accepted — delivered M32: m32 bounded delivery; 2026-11-05 amendment replaces foundation stage with verified direct merge to dev; one automation ownership switch |
| [ADR-0096](ADR-0096-authored-passport-authority.md) | Accepted — delivered M32: six authored passports as sole space configuration; independent tool selection and grants; no legacy config readers |
| [ADR-0095](ADR-0095-toolhub-owned-memory-automation.md) | Superseded by ADR-0105/ADR-0108 (v1 direction, proposed; v0 code implements it until M40.9/M40.10) - delivered M32: ToolHub owns per-user memory, routines, assignments and dreaming; communication-hub retains channel transport and delivery |
| [ADR-0094](ADR-0094-consolidated-model-tool-surface.md) | Partly superseded by ADR-0105 (ToolHub as implementation owner of every family; catalog vocabulary kept) — accepted direction: catalog r5 — 20 first-party families + pinned platform under logical names, via/executor two-level routing, model\|internal\|operator audiences, fail-closed requires (M31) |
| [ADR-0093](ADR-0093-imperative-passports-capability-grants.md) | Accepted — delivered M30: passports are the imperative sole input to build/spawn/admission; agent capability grants (PDP/PEP) instead of tool ids; effect classes live/respawn/recreate/rebuild; amends ADR-0092 D1–D3, D6 (M30) |
| [ADR-0092](ADR-0092-six-passport-space-contract.md) | Accepted — delivered M29/M30, amended by ADR-0093/0094/0096: six effective per-space passports, versioned platform catalogs/templates, grant ceilings and verified migration (M29) |
| [ADR-0091](ADR-0091-root-services-sqlite-storage.md) | Accepted - delivered (CHG-0215–0226): SQLite substrate for ToolHub/Communication (pure-Go driver, per-space DBs, shared migration contract); amends ADR-0087 alternatives + ADR-0088 D1 exclusion; answers ADR-0038 D12b; amended by ADR-0110 (v1 one database per service, proposed) |
| [ADR-0090](ADR-0090-conversation-parallel-lanes.md) | Partly superseded by ADR-0107 (lane owner moves to task-engine; lane key and fairness kept) — accepted: parallel sessions inside one context — lane key (principal, context, conversation), fairness per context, per-context cap below upstream; amends ADR-0025 (CHG-0234) |
| [ADR-0089](ADR-0089-live-tool-inventory.md) | Accepted: live caller-scoped tool inventory — `tools_list` on the store path plus managed-profile `invoke`; no prompt catalog, deferred hydration or vMCP boundary (CHG-0233) |
| [ADR-0088](ADR-0088-bounded-control-journals.md) | Accepted — delivered (CHG-0215–0226): bounded file journals/receipts, typed cursors, safe audit and diagnostics |
| [ADR-0087](ADR-0087-broker-bounded-sqlcipher-store.md) | Accepted direction: Broker-only SQLCipher, strict recovery head and object lifecycle; native/fault gate before migration (CHG-0215–0226) |
| [ADR-0086](ADR-0086-memory-vault-openknowledge.md) | Accepted — delivered M20: per-space markdown memory vault (leaves/cards/summaries/CORE) with deterministic nightly rollup, plus OpenKnowledge as opt-in knowledge surface (M20, renumbered from a duplicate ADR-0073) |
| [ADR-0085](ADR-0085-agent-skills-platform.md) | Accepted — delivered M24: agent skills platform — agentskills.io format, hub-owned per-owner stores, read-only effective view, draft-first writes, grant-only authority, native default-deny, brokered secrets, curated self-evolution, pinned git installs (M24 #356–#368) |
| [ADR-0084](ADR-0084-channel-agnostic-user-locale.md) | Accepted: channel-agnostic user locale — canonical EN catalog with generated ru/kk/es, workspace preferences.json, evidence ladder + /language picker, Delivery.Lang, HUB_USER_LANG, LLM language hints |
| [ADR-0083](ADR-0083-lean-images-capability-packs.md) | Accepted — images delivered M26, capability packs open in M27: lean role images — hub-core/control/runtime/browser split, browser + voice extracted to per-space images, pruned upstream tree, declared capabilities pulled on demand (M26 #371–#380, M27 #383–#390) |
| [ADR-0082](ADR-0082-standing-assignments.md) | Partly superseded by ADR-0108 (state machine and materialization; D4 re-validation and D5 acquisition kept) — accepted: standing assignments — durable owner-intent record reconciling to routine artifacts, shared acquisition inside verified boundaries, deterministic filters gating model wakes (M23 #324/#332) |
| [ADR-0081](ADR-0081-user-scoped-site-hosting.md) | Accepted: user-scoped hosting over existing controller/ToolHub and sish/DNS; static-first, isolated dynamic profile, revocable artifact links; no Haloy fork (M18 #147, renumbered from a duplicate ADR-0079) |
| [ADR-0080](ADR-0080-per-user-dns-tls.md) | Accepted: per-user subdomains — provider-neutral DNS record contract over regru/DO/delegated GCP+DO profiles, hub-issued per-user wildcard via DNS-01 pushed to sish (renumbered from a duplicate ADR-0078) |
| [ADR-0079](ADR-0079-outbound-exposure.md) | Accepted: named public endpoints over opt-in per-binding tunnel workloads — sish reference provider, cloudflared managed alternate; Broker credentials, name-only agent surface (renumbered from a duplicate ADR-0077) |
| [ADR-0078](ADR-0078-auth-first-admission-contract.md) | Accepted — implemented CHG-0176: built-evidence reuse on auth-first credential retry; `interactive` recipe marker classifies unsupported interactive auth (renumbered from a duplicate ADR-0076) |
| [ADR-0077](ADR-0077-ops-admin-delivery.md) | Accepted: ops/admin plane — file-read usage export, versioned space manifests, polling watch, rollout pins, admin token scope, isolated replay |
| [ADR-0076](ADR-0076-operational-protection.md) | Accepted — M17 delivered: M17 hardening, owned resources, budgets, containment and personal supervision |
| [ADR-0075](ADR-0075-dual-engine-context-control.md) | Partly superseded by ADR-0108 (goal mode becomes an Assignment; compaction kept) — accepted — implemented M17 (CHG-0162): grant-selected dual engines for manual/automatic compaction and goal mode; sidecar transport to pinned upstream primitives |
| [ADR-0074](ADR-0074-session-runtime-discipline.md) | Accepted — delivered M16/M17: session & runtime discipline: group scopes, knowledge lifecycle, stable session identity with runaway token ceiling, memory boundary, todo contract, idle-proof, goal bootstrap, archive bounds (renumbered from a duplicate ADR-0072) |
| [ADR-0073](ADR-0073-model-provider-boundaries.md) | Accepted — delivered M15: model & provider layer boundaries (renumbered from a duplicate ADR-0072) |

| [ADR-0072](ADR-0072-connector-recovery-and-system-guide.md) | Accepted: owner-bound encrypted recovery, truthful inventory and mandatory reference guidance |
| [ADR-0071](ADR-0071-dynamic-session-contract.md) | Accepted: bounded owner-only dynamic JSON session contracts |
| [ADR-0070](ADR-0070-recipe-oauth.md) | Accepted: recipe-driven OAuth and lease-local token file views |
| [ADR-0069](ADR-0069-broker-effective-contract.md) | Accepted: imported from services/credential-broker ADR-0010: effective contract on request — extras and demote |
| [ADR-0068](ADR-0068-broker-allow-shared.md) | Accepted: imported broker ADR-0009: allow_shared in dynamic contracts |
| [ADR-0067](ADR-0067-broker-dynamic-contract-registration.md) | Accepted: imported broker ADR-0008: dynamic contract registration via control API |
| [ADR-0066](ADR-0066-broker-consumer-id.md) | Accepted: imported broker ADR-0007: consumer_id discriminates multiple credentials per connection |
| [ADR-0065](ADR-0065-broker-memory-bank.md) | Accepted: imported broker ADR-0006: medium memory-bank adaptation |
| [ADR-0064](ADR-0064-broker-bounded-oauth.md) | Accepted: imported broker ADR-0005: bounded OAuth, no runtime platforms |
| [ADR-0063](ADR-0063-broker-single-process-journal.md) | Accepted: imported broker ADR-0004: single-process encrypted journal |
| [ADR-0062](ADR-0062-broker-shared-by-delivery.md) | Accepted: imported broker ADR-0003: shared credentials allowed by delivery method |
| [ADR-0061](ADR-0061-broker-browser-approval-hub.md) | Accepted: imported broker ADR-0002: browser confirmation via Communication Hub |
| [ADR-0060](ADR-0060-broker-not-secret-store.md) | Accepted: imported broker ADR-0001: the broker is not a canonical secret store |
| [ADR-0059](ADR-0059-browser-workloads-as-connectors.md) | Accepted: browsers as ordinary connectors — web egress, per-connection profile isolation, one-time owner viewer links, staged toolsets, bounded shm budget; agent-browser stays blocked |
| [ADR-0058](ADR-0058-team-connections.md) | Accepted: operator-published Team records, team-owned connections with member bindings, shareable credential/connection modes, not_permitted member lifecycle, sender-attributed audit |
| [ADR-0057](ADR-0057-ci-built-connector-images.md) | Accepted: catalog tiers T0/T1/T2, CI-built digest-pinned images with zero-build admission, deterministic `connector import` under --confirm, staged toolset projection, bounded prefetch |
| [ADR-0056](ADR-0056-ownership-finalizers.md) | Accepted: ownership labels on spawned resources, persisted finalizer ladder with verified-zero receipts, one reconciler, revoke-wins commit, bounded store compaction |
| [ADR-0055](ADR-0055-unified-connector-surface.md) | Accepted: nine `connector_*` tools over one durable operation journal; typed codes, no prose markers or link material in agent results; same facade on MCP, hubctl and control HTTP |
| [ADR-0054](ADR-0054-human-action-surface.md) | Accepted: one-time `?t=` human-action links on an opt-in public surface, origin-channel delivery, device flow and paste-redirect OAuth, transactional credential save |
| [ADR-0053](ADR-0053-recipe-source-ledger.md) | Accepted: content-addressed secret-free recipes, ranked source ledgers, fail-closed same-rank ambiguity, digest-referenced onboardings |
| [ADR-0052](ADR-0052-retired.md) | Retired: number reserved, never issued — the browser-connectors decision drafted under this slot was renumbered to ADR-0059 |
| [ADR-0051](ADR-0051-deterministic-work-isolation.md) | Accepted: local writer ownership, durable result handoff and isolated script containers (renumbered from a duplicate ADR-0033) |
| [ADR-0050](ADR-0050-knowledge-in-runtime-state.md) | Accepted: knowledge in runtime state, staged via apply --state, relocated offline via hubctl spool migrate |
| [ADR-0049](ADR-0049-broker-only-credential-intake.md) | Accepted: credential intake only via Broker or runtime self-env; legacy gateway surface removed |
| [ADR-0048](ADR-0048-routines-package.md) | Accepted: internal/routines leaf owns routine semantics and engine behind a Deps seam |
| [ADR-0047](ADR-0047-channel-contract.md) | Accepted: 12-method Channel contract, Caps flags, deps-injected adapters, registry dispatch and conformance suite |
| [ADR-0046](ADR-0046-comm-plane-zone-map.md) | Superseded by ADR-0105 (channel-hub and ingress-gateway; v0 code implements it until M40.8) - accepted: comm-plane zone map - commodel/channel/routines/commops/serviceauth owners, fixed import direction |
| [ADR-0045](ADR-0045-watcher-probe-pipeline.md) | Accepted: watcher probe = fetch + deterministic extract pipeline; isolated script/command probes; snapshotted recipes |
| [ADR-0044](ADR-0044-routine-destinations.md) | Accepted: verified task destinations and per-run origin for routine delivery |
| [ADR-0043](ADR-0043-compact-routine-tools.md) | Accepted: five public routine tools; lifecycle operations retained internally |
| [ADR-0042](ADR-0042-routine-lifecycle.md) | Accepted direction: routine lifecycle, revision fences and interval windows |
| [ADR-0041](ADR-0041-proactive-automation.md) | Superseded by ADR-0108 (v0 code implements it until M40.10) - accepted: proactive automation on the occurrence pipeline - bounded follow-up retry, quiet/cap/escalation policy, control-plane tool calls, owner brief, event subscriptions (M12) |
| [ADR-0040](ADR-0040-single-stack-supervisor.md) | Superseded by ADR-0107 (v0 code implements it until M40.3) - accepted direction: one Compose supervisor using the existing control image; container deployment verified live in CHG-0097 |
| [ADR-0039](ADR-0039-hermes-pin-raise.md) | Proposed: conditional Hermes pin raise — review trigger is the upstream MCP breaker merge or next pin bump (D12c) |
| [ADR-0038](ADR-0038-embedded-store.md) | Answered: embedded single-writer store — measured evidence kept files for supervisor/spool; SQLite direction adopted separately via ADR-0091 (D12b) |
| [ADR-0037](ADR-0037-supervisor-in-linux-container.md) | Superseded by ADR-0040: containerized supervisor via the ToolHub socket proxy (D12a) — answered by ADR-0040's simpler accepted direction |
| [ADR-0036](ADR-0036-execution-chain-reliability.md) | Accepted: one run budget with idle deadlines, internal uncertain reconciliation, lease-safe lifecycle, rights-only policy hash, capability readiness, event-driven wakeups, correlation IDs (M19) |
| [ADR-0035](ADR-0035-telegram-operation-contracts.md) | Accepted: byte-based images, explicit MCP env, routine creation receipts, current approval UI and exact file handoff |
| [ADR-0034](ADR-0034-cli-package-policy.md) | Accepted: private package home, exact shipped CLI trust and atomic host catalog binding |
| [ADR-0033](ADR-0033-bounded-cli-transport.md) | Accepted: bounded CLI as a ToolHub transport with executor-container isolation receipts |
| [ADR-0032](ADR-0032-capability-execution-isolation.md) | Accepted: isolate control state, Hermes, scoped file executors and scratch-only code workloads |
| [ADR-0031](ADR-0031-default-deny-capability-policy.md) | Accepted: zero-capability baseline, explicit profiles, canonical identities and one ToolHub policy boundary |
| [ADR-0030](ADR-0030-cliproxy-image-routes.md) | Accepted: cLIProxy image ids use two calls: images endpoint, or Gemini chat completions |
| [ADR-0029](ADR-0029-bounded-pdf-office-webp.md) | Accepted: bounded pdf, xlsx, pptx, and webp stay in the hub process; Word and a user zip tool stay out |
| [ADR-0028](ADR-0028-configurable-image-capability.md) | Accepted: image capability selects provider, model, and delivery; CLIProxy is the default, Fal stays external |
| [ADR-0027](ADR-0027-workspace-document-image-profile.md) | Accepted: hub document and image tools; separate credentials, workspace artifacts, native vision and image_gen omitted |
| [ADR-0026](ADR-0026-shared-control-plane.md) | Partly superseded by ADR-0105 (infra render split replaced by per-service binaries) - accepted: one shared ToolHub/broker/controller on `hermes-hub-runtime`; multi-token endpoint, `infra` render split |
| [ADR-0025](ADR-0025-context-parallel-gateway-workers.md) | Accepted: bounded worker pool; FIFO claim skips busy context keys, serial per context — amended by ADR-0090 (parallel lanes inside a context) |
| [ADR-0024](ADR-0024-toolhive-local-vmcp.md) | Accepted: defer ToolHive local vMCP as the stable endpoint after failed dynamic-rebind evidence |
| [ADR-0023](ADR-0023-optional-mcp-images.md) | Accepted: optional Google/Atlassian/Telegram Python MCP trees are dedicated images, not the hub runtime |
| [ADR-0022](ADR-0022-credential-broker-boundary.md) | Accepted: keep Credential Broker as a separate opt-in Go module/process with explicit Hub adapters |
| [ADR-0021](ADR-0021-toolhub-control-plane.md) | Accepted: control MCP, operator grants and loopback credential elicitation |
| [ADR-0020](ADR-0020-local-account-preparation.md) | Accepted: owner-selected PC, fixed read-only ToolHive controller and protected local account setup |
| [ADR-0019](ADR-0019-official-workspace-telegram-sdk.md) | Accepted: official Workspace MCP and owner-approved Python Telegram account workload |
| [ADR-0018](ADR-0018-personal-provider-api.md) | Accepted: stateless official provider HTTPS calls behind ToolHub authorization |
| [ADR-0017](ADR-0017-personal-assistant-channels.md) | Accepted: telegram/Slack communication channels, voice envelope and context lifecycle |
| [ADR-0016](ADR-0016-encrypted-credentials-and-oauth.md) | Accepted: ciphertext store behind ToolHub locators, OAuth broker and audit |
| [ADR-0015](ADR-0015-durable-job-runtime-mappings.md) | Accepted: durable job, session, run and generation mappings in local stores — amended by ADR-0091 (part of state moved to SQLite) |
| [ADR-0014](ADR-0014-scale-to-zero-context-runtimes.md) | Accepted: scale-to-zero warm Hermes runtime per active context; Hub owns routine schedules (five-minute idle superseded by 30-min warm TTL in ADR-0036) |
| [ADR-0013](ADR-0013-toolhub-security-boundary.md) | Accepted: go ToolHub authorizes; ToolHive executes and aggregates internally |
| [ADR-0012](ADR-0012-hermes-api-contract.md) | Accepted: pinned Hermes authenticated API server as the runtime-adapter contract target |
| [ADR-0011](ADR-0011-stable-identity-identifiers.md) | Accepted: stable opaque IDs with a compatibility mapping for current spaces and jobs |
| [ADR-0010](ADR-0010-direct-mcp-atlassian.md) | Accepted: install pinned direct `mcp-atlassian` stdio server instead of Atlassian Rovo |
| [ADR-0009](ADR-0009-scoped-homes-and-communication-hub.md) | Accepted: peer organization/user homes and separate communication-hub |
| [ADR-0008](ADR-0008-telegram-service-self-service.md) | Accepted: telegram connector catalog and self-service enablement |
| [ADR-0007](ADR-0007-communication-gateway.md) | Accepted: channel-neutral gateway with isolated on-demand Hermes jobs — worker/file clauses amended by ADR-0025/0090/0091 |
| [ADR-0006](ADR-0006-work-service-boundaries.md) | Accepted: provider-native boundaries for GitLab and Google — Atlassian clause superseded by ADR-0010 (direct mcp-atlassian, no Rovo) |
| [ADR-0005](ADR-0005-self-env-and-telegram-boundaries.md) | Accepted: separate Telegram paths and user self-env overlay — secret intake fenced by ADR-0021/0049 (Broker-only) |
| [ADR-0004](ADR-0004-organization-user-scope.md) | Accepted: organization policy overlay with isolated user runtimes |
| [ADR-0003](ADR-0003-go-first-tooling.md) | Accepted: just and Go for project-owned tooling and runtime supervision |
| [ADR-0002](ADR-0002-standalone-user-spaces.md) | Accepted: standalone Hermes, spaces/user and Docker environments — Jest orchestrator clause superseded by ADR-0003 |
| [ADR-0001](ADR-0001-isolated-hermes-deployments.md) | Partly superseded: Historical initial design; partly superseded by ADR-0002 |
