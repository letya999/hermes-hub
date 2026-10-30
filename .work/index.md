# Change plans

Use `in-progress/` and `done/` for change folders. A folder contains `plan.md`
and `state.yaml`. Task progress belongs here, never in current architecture docs.
New milestones follow the [milestone playbook](milestone-playbook.md) and its
[templates](templates/change-plan.md).

Numbering: `CHG-<NNNN>` is unique and sequential. A handful of historical
collisions (0015, 0026, 0027, 0031, 0039, 0047, 0074, 0105, 0106, 0127, 0148, 0149, 0150, 0151, 0152, 0153) predate
the docs-check gate and stay for reference stability — rows below disambiguate
by directory name. Milestone numbers are not chronological either (M19/M12/M8
labels appear out of order, and the connector platform was called M13 while its
merge was M14): trust the CHG number and directory, not the milestone name.

## In progress

| Change | State |
|---|---|
| [CHG-0234](in-progress/CHG-0234-conversation-parallel-lanes/plan.md) | Parallel session lanes per context: conversation-keyed inFlight, fairness per context, per-context cap below upstream Hermes cap; design proposed, implementation not started |
| [CHG-0231](in-progress/CHG-0231-mcp-onboarding-repair/plan.md) | MCP onboarding: immutable reviewed contract revisions, digest gate, failed form cleanup and explicit bind conflicts; validating |
| [CHG-0232](in-progress/CHG-0232-runtime-credential-diagnostics/plan.md) | Caller runtime skill ref leases and allowlisted spawn diagnostics; validating |
| [CHG-0229](in-progress/CHG-0229-i18n-gap-fixes/plan.md) | i18n follow-up to CHG-0191: model lang pin per run, localized skill-draft card + assignment replies, Slack slash-command dispatch, stale catalog keys; implemented, validating |
| [CHG-0226](in-progress/CHG-0226-storage-migration-acceptance/plan.md) | Storage reliability MVP: Явная миграция и итоговая приёмка ограниченного хранения; design accepted, implementation not started |
| [CHG-0225](in-progress/CHG-0225-runtime-revision-reconcile/plan.md) | Storage reliability MVP: Долговечная сверка проекций и понятные причины runtime-сбоев; design accepted, implementation not started |
| [CHG-0224](in-progress/CHG-0224-toolhub-state-confirmation/plan.md) | Storage reliability MVP: Сохранность ToolHub и доверенное подтверждение пользователя; design accepted, implementation not started |
| [CHG-0223](in-progress/CHG-0223-storage-budget-maintenance/plan.md) | Storage reliability MVP: Полный учёт объёма и безопасное операторское обслуживание; design accepted, implementation not started |
| [CHG-0222](in-progress/CHG-0222-spool-durable-handoff/plan.md) | Storage reliability MVP: Один receipt для результата, доставки и обработки source batch; design accepted, implementation not started |
| [CHG-0221](in-progress/CHG-0221-bounded-control-audit/plan.md) | Storage reliability MVP: Ограниченный обязательный audit и честные исходы действий; design accepted, implementation not started |
| [CHG-0220](in-progress/CHG-0220-broker-object-lifecycle/plan.md) | Storage reliability MVP: Управляемые версии secret objects и безопасный GC; design accepted, implementation not started |
| [CHG-0219](in-progress/CHG-0219-broker-sqlcipher-store/plan.md) | Storage reliability MVP: Таблицы состояния и постраничный audit Broker; design accepted, implementation not started |
| [CHG-0218](in-progress/CHG-0218-broker-sqlcipher-proof/plan.md) | Storage reliability MVP: Gate сборки и аварийной атомарности SQLCipher; design accepted, implementation not started |
| [CHG-0217](in-progress/CHG-0217-diagnostic-log-durability/plan.md) | Storage reliability MVP: Ротация, курсоры и ограниченный trace диагностики; design accepted, implementation not started |
| [CHG-0216](in-progress/CHG-0216-event-state-isolation/plan.md) | Storage reliability MVP: Изоляция курсоров и безопасная очистка event records; design accepted, implementation not started |
| [CHG-0215](in-progress/CHG-0215-broker-recovery-head/plan.md) | Storage reliability MVP: Надёжная текущая голова recovery брокера; design accepted, implementation not started |
| [CHG-0214](in-progress/CHG-0214-standing-assignment-mvp/plan.md) | M23 #324/#332: non-consuming cursor source, filtered batch wakes, specialist progress and runtime fences |
| [CHG-0213](in-progress/CHG-0213-org-knowledge-brain/plan.md) | M20 stage 10 (optional): org shared brain — read-only OpenKnowledge over org material |
| [CHG-0212](in-progress/CHG-0212-memory-migration-eval/plan.md) | M20 stage 9: Honcho/native export to cards, memory-eval fixture, `memory vacuum` |
| [CHG-0211](in-progress/CHG-0211-knowledge-ui-edge/plan.md) | M20 stage 8: loopback `ui_port` + SSH tunnel docs; M18 edge hook |
| [CHG-0210](in-progress/CHG-0210-openknowledge-mcp/plan.md) | M20 stage 7: OK MCP read/write capability split (`knowledge_ui` / `knowledge_ui_write`) |
| [CHG-0209](in-progress/CHG-0209-openknowledge-service/plan.md) | M20 stage 6: pinned single-replica OpenKnowledge service per space |
| [CHG-0208](in-progress/CHG-0208-memory-card-contract/plan.md) | M20 stage 5: `memory_card` fact/truth/merge contract + memory-zone write guards |
| [CHG-0207](in-progress/CHG-0207-memory-search/plan.md) | M20 stage 4: `memory_search` BM25+link-graph, scope confinement, maintenance pass |
| [CHG-0206](in-progress/CHG-0206-memory-night-rollup/plan.md) | M20 stage 3: hub-owned nightly rollup — summaries, evidence facts, Compiled Truth, CORE edits |
| [CHG-0205](in-progress/CHG-0205-daily-leaf-ingest/plan.md) | M20 stage 2: verbatim daily leaf ingest from the communication plane |
| [CHG-0204](in-progress/CHG-0204-vault-layout/plan.md) | M20 stage 1: `spaces/<id>/vault/` git substrate, `memory_vault` feature, CORE injection |
| [CHG-0199](in-progress/CHG-0199-m24-live-acceptance/plan.md) | M24 #368 S13: live acceptance — standalone controller + real cli-exec git legs (partial: Telegram/managed legs blocked on credentials) |
| [CHG-0189](in-progress/CHG-0189-watch-contract/plan.md) | M23 #324/#332 follow-up: tool-contract discipline rule, watch_subscribe discoverability (prompt/descriptions/semantic refusal), per-source stream cadence (min_interval_seconds 5..3600), stream cursor persistence, watch_resume + catalog 5.3.0, docs-coverage test |
| [CHG-0174](in-progress/CHG-0174-hosting-acceptance/plan.md) | M18 #147 H5: hosting acceptance/current docs — A1–A11 code/runtime/browser/sish evidence; offline gates green, native acceptance pending |
| [CHG-0173](in-progress/CHG-0173-hosting-dynamic/plan.md) | M18 #147 H4: one admitted stateless go-http profile, secure runtime/relay, restart/finalizers; implemented, offline gates green, native acceptance pending |
| [CHG-0172](in-progress/CHG-0172-hosting-static-delivery/plan.md) | M18 #147 H3: static agent/stack/exposure delivery and independent recovery; implemented, offline gates green, native acceptance pending |
| [CHG-0171](in-progress/CHG-0171-hosting-web-access/plan.md) | M18 #147 H2: static serving, artifact bootstrap/session gate, HTTP bounds/audit; implemented, offline gates green, native acceptance pending |
| [CHG-0170](in-progress/CHG-0170-hosting-store/plan.md) | M18 #147 H1: owner-bound slots, confined immutable snapshots, quota and crash recovery; implemented, offline gates green, native acceptance pending |
| [CHG-0168](in-progress/CHG-0168-dns-tls-impl/plan.md) | M18 #146: SPEC-0078 implementation — dns contract, regru/DO/GCP clients, ACME DNS-01 issuer + sish push, hubctl/doctor |
| [CHG-0167](in-progress/CHG-0167-dns-tls-addressing/plan.md) | M18 #146: per-user subdomains design pass — research + ADR-0080 accepted (provider-neutral DNS, per-user wildcard via DNS-01 to sish) |
| [CHG-0166](in-progress/CHG-0166-outbound-exposure-impl/plan.md) | M18 #145: SPEC-0077 implementation — reviewed config, sish provider, manager, agenttools/stack wiring |
| [CHG-0177](in-progress/CHG-0177-exposure-primitive/plan.md) | M18 #145: outbound exposure design pass — research + ADR-0079 accepted (sish reference, cloudflared alternate) |
| [CHG-0176](in-progress/CHG-0176-auth-first-onboarding/plan.md) | M18 #293: auth-first onboarding completion — retry without rebuild, interactive marker |
| [CHG-0162](in-progress/CHG-0162-goal-mode-engines/plan.md) | #311 persistent /goal mode — dual engine, judge modes (design proposed, implementation not started) |
| [CHG-0156](in-progress/CHG-0156-goal-bootstrap/plan.md) | M16 #213: goal-bootstrap; live acceptance pending |
| [CHG-0151](in-progress/CHG-0151-archive-bounds/plan.md) | M16 #151: archive-bounds; live acceptance pending |

| [CHG-0147](in-progress/CHG-0147-m14-acceptance/plan.md) | Milestone 14 acceptance and connector lifecycle repair |
| [CHG-0144](in-progress/CHG-0144-recipe-oauth/plan.md) | Recipe-driven OAuth for prepared and runtime MCP installation |
| [CHG-0143](in-progress/CHG-0143-oauth-viewer-link-flows/plan.md) | OAuth resend rotation, broker grant-epoch fix, viewer-link relay — retroactive record for the uncommitted branch diff |
| [CHG-0141](in-progress/CHG-0141-broker-multi-consumer/plan.md) | Broker: multi-consumer credentials per connection — implemented; module `just check` on a Linux runner is the remaining gate |
| [CHG-0140](in-progress/CHG-0140-broker-dynamic-contracts/plan.md) | Broker: dynamic contract registration — hub integration done as CHG-0107; module `just check` gate remains |
| [CHG-0139](in-progress/CHG-0139-m15-model-provider-layer/plan.md) | M15 model & provider layer (renumbered from a duplicate CHG-0105) |
| [CHG-0136](in-progress/CHG-0136-docs-restructure/plan.md) | Documentation restructure and enforcement (this change) |
| [CHG-0117](in-progress/CHG-0117-probe-source-spectrum/plan.md) | Probe source spectrum and delivery modes; validation phase |
| [CHG-0110](in-progress/CHG-0110-routine-recovery/plan.md) | Routine tick recovery, record isolation and bounded occurrence history; live acceptance pending |
| [CHG-0101](in-progress/CHG-0101-tool-call-probes/plan.md) | M12 stage 3: ToolHub /v1/tool-call + mcp_tool probes |
| [CHG-0100](in-progress/CHG-0100-watcher-policy/plan.md) | M12 stage 2: quiet window, daily cap, agent escalation |
| [CHG-0099](in-progress/CHG-0099-followup-hardening/plan.md) | M12 stage 1: bounded check retry, verdict text, task: probe, reminder evidence |
| [CHG-0098](in-progress/CHG-0098-forum-topic-sessions/plan.md) | Issue #220: /new mints a bot-owned forum topic per session; Docker + live pending |
| [CHG-0091](in-progress/CHG-0091-telegram-operation-contracts/plan.md) | Telegram operation contracts repair (draft SPEC-0052, proposed ADR-0035) |
| [CHG-0065](in-progress/CHG-0065-capability-enforcement/plan.md) | Issue 122: default-deny capability enforcement (SPEC-0041/0042, ADR-0031/0032) |

## Abandoned / superseded

| Change | State |
|---|---|
| [CHG-0037](done/CHG-0037-toolhub-closeout/plan.md) | Abandoned: ToolHub closeout umbrella superseded by later per-issue CHGs |
| [CHG-0031](done/CHG-0031-docker-disk-diagnosis/plan.md) | Abandoned: read-only diagnosis superseded by CHG-0031-docker-reclaim findings |

## Done — M26 Lean Role Images

| Change | State |
|---|---|
| [CHG-0227](done/CHG-0227-managed-browser-delivery/plan.md) | Done 2026-10-29: M26 follow-up — managed browser delivery end-to-end (commit 39db841): BrowserGrant through settings→env→supervisor→materialize→attestation, NO_PROXY covers browser-<user> (egress relay refused the MCP handshake), stale-policy respawn tolerates missing container + skills dir provisioned, sidecar binds managed/<env>/workspace so playwright artifacts reach delivery, stageMediaArtifact accepts upstream <hermes>/cache media; ToolHub profile republished to hub-agent-tools@5.7.0/policy rev12; live: 31 MCP tools + screenshot lands in runtime workspace; milestone M26 closed 6/6 |
| [CHG-0190](done/CHG-0190-browser-sidecar-idle/plan.md) | Done 2026-10-28: M26 follow-up — browser sidecar lifecycle (ADR-0083 D8/D9, SPEC-0083): compose cpu/mem/pids ceiling, spawn auto-start (`docker start` before readiness, fail-closed + reclaim), sweep idle-sleep after `--browser-idle-ttl` 15m with live/compose-runtime and foreign-project guards; restores scale-to-zero economics for ~100-user fleets |
| [CHG-0187](done/CHG-0187-m26-acceptance/plan.md) | Done 2026-10-28: M26 #380 S5: evidence record + docs (operations images section, architecture component map, integrations rows); just check pass coverage 86.14%; live acceptance deferred — no Docker per owner instruction, image-size/probe/live-browser evidence rides the CI image+containers jobs |
| [CHG-0186](done/CHG-0186-browser-sidecar/plan.md) | Done 2026-10-28: M26 #378 S4: hub-browser sidecar — browser stage + prod/dev-browser targets, hub-runtime browser subcommand (Xvfb/fluxbox/x11vnc-passwdfile/noVNC/chromium-CDP + pw-mcp 8931 persistent & 8932 isolated + meet NodeServer), runtime drops the display stack, browser MCP over streamable HTTP, CDP internal-only + noVNC loopback, spawned runtimes dual-home onto the space net and lose /state/browser + --shm-size |
| [CHG-0185](done/CHG-0185-hub-core-split/plan.md) | Done 2026-10-28: M26 #377 S3: role images — core stage (Go binaries only), control = core + docker/thv, compose per-service role commons, runtime keeps its own build tag, dev-rebuild maps daemons to -dev-core, shm_size scoped to runtime |
| [CHG-0184](done/CHG-0184-voice-consolidation/plan.md) | Done 2026-10-28: M26 #375 S2: voice consolidation — STTCommand/TTSCommand removed (service URLs only), dedicated stt/tts images on hashed lockfiles, whisper/espeak and command envs out of hub images |
| [CHG-0183](done/CHG-0183-image-hygiene/plan.md) | Done 2026-10-28: M26 #373 S1: hygiene — python_build stage (UV_COMPILE_BYTECODE, hashed aiohttp layer), upstream tree pruned to editable modules with .hermes-rev marker, Debian novnc dropped (libnode gone), e2e dev-only, setuid strips |
| [CHG-0182](done/CHG-0182-image-budget-gate/plan.md) | Done 2026-10-28: M26 #371 S0: image budget gate — devcheck image-budget (size ceiling + forbidden-artifact probe + fail-closed), CI image job gates all four roles, baseline audit frozen in research.md |

## Done — M24 agent skills platform

| Change | State |
|---|---|
| [CHG-0230](done/CHG-0230-skills-provisioning-gap/plan.md) | Done 2026-10-30: Skills platform provisioning gaps — skills/skills_manage added to toolhub wish vocabulary + default workspace template; governance inventory reconcile merges SeedInventory into persisted docs (hub:skills* no longer permanently "unknown"→deny); governanceDocPath infra-deterministic; pv-rotation migrates stray bindings + capability profiles to the pv active bindings sit at; live: both managed runtimes project all 5 skill tools (62/50 tool lists); just check pass coverage 86.31% |
| [CHG-0228](done/CHG-0228-skills-view-lifecycle/plan.md) | Done 2026-10-30: Skills platform view lifecycle — /v1/skill-apply rebuilds the ro effective view in place after publish/rollback (no respawn); Build sweeps skills absent from all sources and files dropped from new revisions; `hubctl skill eval --id` scores draft trees; skills_list is the honest inventory (eligible/missing/stats, tier-0 counts only ineligible); credential `ref:` reuses configured self-env keys through the same allowlist; live: publish→usable, xlsx install→publish→update, eval accuracy 1.0 via real model relay |
| [CHG-0198](done/CHG-0198-skill-quality/plan.md) | Done 2026-10-28: M24 #367 S12 — quality: scan.Lint advisory findings on every draft (name charset/dir, imperative desc ≤1024/~250, no This-skill prefix, body ≤500, ≤60 files, refs 1-deep+existing, no incident-log) wired through scanTree; .state/usage journal (bounded counters+events, hub-written on skill()/skill_view only) + skills_list stats + hubctl skill stats; skilleval package (triggers.json 60/40 deterministic split + runs/threshold, evals.json evidence-required cases, isolated temp-space Eval, HTTPRunner via HUB_SKILL_EVAL_URL, model_unavailable honest); hubctl skill eval → .work/tmp report; trigger suites shipped for the 3 platform skills + lint-clean descriptions; catalog hub-agent-tools@5.5.0; coverage 86.12% |
| [CHG-0197](done/CHG-0197-skill-git-install/plan.md) | Done 2026-10-28: M24 #365 S10 — git installs: gitsource package (ParseSource https-only github/gitlab/gitlab_host, SourceID sha256[:16], Sources/RemoveSource registry); ExecCell GitOps over /cli-exec — hardened argv (hooksPath=/dev/null, protocol.file=never, no submodules/LFS), blobless fetch+ls-remote pin, origin-remote deepen for force-push ancestry, archive→Tree rejecting symlinks/gitlinks/../, token via GIT_TOKEN env + credential.helper only; skill-source cell scope binds <16-hex>.git mirror rw after ContainedPath+NoSymlinkPath+dir checks; skillgit@1.0.0 admitted as exact tuples per git host (git stays out of user_commands); comm-hub /v1/skill-git install/update/check_updates/sources/remove_source, grant→broker ResolveEnv→GIT_TOKEN; skill_manage install|update|check_updates + url/ref/path/grant, candidates passthrough, same approval card; discovery subdir-scoped ≤6 deep ≤2000 dirs, convention dirs first; .origin.json {source_id,url,ref,commit,path,tree_sha,scan,fetched_at} hub-private sidecar; hubctl skill install --url/update --name|--all/sources list|remove --id via toolhub.auth + GIT_TOKEN; catalog hub-agent-tools@5.4.0; coverage 86.14% |
| [CHG-0196](done/CHG-0196-provisioning-profiles/plan.md) | Done 2026-10-28: M24 #364 S9 — provisioning profiles: extends chains with org-per-level override over policies/profiles + legacy *-default.yaml fallback as "default"; OrgMember {role, profile} + OrganizationSettings.default_profile (scalar marshal preserved); init --profile stamps agent/workspace, layout dirs, profile.lock (hashes+version); hubctl org sync-members idempotent dry-run-first bulk provisioning; effective SOUL contract→org→profile→user→catalog (persona-prefix assertions relaxed to containment); skills.yaml pins order first + ineligible→render error; grants.yaml → draft CapabilityPolicy revision via hubctl profile grants, authority only through capability --kind policy --confirm inside the org ceiling; hubctl profile diff|apply 3-way via lock (untouched update, customized kept+reported); org assignment beats forged profile.lock; coverage 86.13% |
| [CHG-0195](done/CHG-0195-skill-secrets/plan.md) | Done 2026-10-28: M24 #363 S8 — secrets/settings: requires.credentials string/object normalization + compat aliases, reserved HUB_/BROKER_ names rejected; .state/credentials + .state/settings private atomic store bound to (principal, skill); hard-missing hides skill, soft-missing surfaces setup-needed + protected form URL in skill(); forms persist via gateway store mount, declared-name filtered; code_exec skill arg → declared ∩ stored env leased over stdin into exec-scratch (≤16, no reserved/sanitized shadowing, one call only); skill_manage configure writes declared settings only; catalog hub-agent-tools@5.3.0; coverage 86.14% |
| [CHG-0194](done/CHG-0194-skill-resources/plan.md) | Done 2026-10-28: M24 #362 S7 — resources+scripts: skill_view bounded (32 KiB, SKILL.md+references/templates/examples only); one-level markdown-link lint; load.exec scanner rule; metadata.deps + uv/npx pins + PEP 723 → validated deps, DepsMissing render stamp; skills: ro root for file/document/image executors (OpenRoots wires HUB_SKILLS_EFFECTIVE); principal cells bind generated/skills-effective ro at /skills, effective view carries no exec bits — scripts run only from scratch copies; skill() reports readiness incl. instructions_only; catalog hub-agent-tools@5.2.0; coverage 86.13% |
| [CHG-0193](done/CHG-0193-skill-disclosure/plan.md) | Done 2026-10-28: M24 #361 S6 — progressive disclosure: readiness-stamped effective index (toolsets/connectors/platform → Eligible/Hidden/Missing, revision re-hashed over visibility); tier-0 <available_skills> in soul-effective (escaped, 200-char cap, >20 → names+search hint); lexical skills_search (name/desc/tags/body, RU+EN, exact-name first); harness /name args + $name refs with $ARGUMENTS/$0..$n, ≤5 refs, dedupe + one-time <skills_catalog_updated> per session; gateway mounts /skills-effective ro; coverage 86.12% |
| [CHG-0192](done/CHG-0192-skill-tools/plan.md) | Done 2026-10-28: M24 #360 S5 — five governed skill tools (skill/skills_search/skills_list/skill_view/skill_manage) in managed+unmanaged parity; draft-only writes with base_revision fencing; nonce approval cards → runtime /v1/skill-apply sole mutator + rollback; org drafts gated on HUB_ORGANIZATION_ROLE owner/admin, hubctl skill promote; catalog hub-agent-tools@5.1.0; coverage 86.09% |
| [CHG-0203](done/CHG-0203-skill-security/plan.md) | Done 2026-10-27: M24 #359 S4 — deterministic scan pipeline: line rules (secret/injection/exfil/load.exec), taint, publish gate, audit; commit bcfa667 (closeout bookkeeping completed 2026-10-28) |
| [CHG-0202](done/CHG-0202-native-default-deny/plan.md) | Done 2026-10-27: M24 #358 S3 — native Hermes capabilities default-deny: inventory deny, no implicit base list, skills/curator sealed, .no-bundled-skills; commit 6eb440e (closeout bookkeeping completed 2026-10-28) |
| [CHG-0201](done/CHG-0201-unified-skill-store/plan.md) | Done 2026-10-27: M24 #357 S2 — unified skillstore: drafts/revisions/archives/quotas, legacy migration, effective read-only view at /skills; commit 6ada0ef (closeout bookkeeping completed 2026-10-28) |
| [CHG-0200](done/CHG-0200-skills-platform-design/plan.md) | Done 2026-10-27: M24 #356 S1 — skills platform design: ADR-0085 + SPEC-0084 + Hermes pin spike verified; commit b3375d1 (closeout bookkeeping completed 2026-10-28) |


## Done — M23 proactive assignments

| Change | State |
|---|---|
| [CHG-0188](done/CHG-0188-m23-live-wake/plan.md) | Done 2026-10-27: M23 #324/#332 live-wake reliability — stale-policy runtime replaced by Ensure, pins restamped, runtime 409 -> terminal failed; failure-notice dedupe on confirmed delivery (pending+delivered markers, release on notice death); filter scripts outside spool lock + fail-closed, results/messages envelopes + numeric ids, sink=agent wake budget/quiet/daily-limit fences, WakeDay rollover; assignmentCall real HTTP, assignment_* on direct MCP + ToolHub catalog 5.2.0 with unique capability_ids (routine_get/images_inspect collisions fixed); holds fetched outside s.mu; singleflight in acquire pool; live: catalog 5.2.0 admitted 39/39, hook -> watch change -> telegram-sent, cancel tombstones; coverage 86.10% |
| [CHG-0181](done/CHG-0181-m23-live-acceptance/plan.md) | Done 2026-10-27: M23 stage 4 — live acceptance on dev stack: multi-source apply (http+hook), occurrence→probe→change→dispatched→telegram-sent, hook intake 401/404/202, pause quiet / resume catch-up / restart durable / cancel tombstones + re-apply 409, cross-principal 404; live bugs fixed (actor/scope/org stamps, idle-hook no_change); slack leg not run (no creds); coverage 86.08% |
| [CHG-0180](done/CHG-0180-assignment-control-surface/plan.md) | Done 2026-10-27: M23 #324 stage 3 — REST /v1/assignments CRUD+lifecycle+dry-run+complete, assignment_* agent tools (expected_revision CAS, no authorize arg), /assignments + /assignment chat commands incl. owner-only authorize |
| [CHG-0179](done/CHG-0179-acquisition-pipeline/plan.md) | Done 2026-10-27: M23 #332 stage 2 — per-sweep acquisition pool keyed (principal,url), durable Retry-After backoff, cursor+gap surfacing on EventState, per-event filter keep/drop/urgent, durable hook inbox + /v1/hooks intake |
| [CHG-0178](done/CHG-0178-assignment-plane/plan.md) | Done 2026-10-27: M23 #324 stage 1 — durable commodel.Assignment, idempotent create by intent hash, plan reconcile → deterministic schedules with receipts + -rN regeneration, lifecycle propagation + revision-fenced wakes, narrow-only grants |

## Done — M17 ops, admin & delivery

| Change | State |
|---|---|
| [CHG-0165](done/CHG-0165-m17-ops-hardening/plan.md) | Done 2026-10-26: M17 ops hardening — undeliverable-notice ack, detection/reconcile sweep split, graceful Serve drain, bounded spool retention (72h + count/byte caps + gzip archive) + usage/signal journal bounds + holds-poll backoff, onboarding runbook; live-verified on dev hub; metric watchers → #354; coverage 86.59% |
| [CHG-0164](done/CHG-0164-m17-code-closeout/plan.md) | Done 2026-10-26: M17 closeout — #326 watchdog/owned lifecycle, #327 budgets/fair admission, #328 event-driven personal supervisor, #329 hold-gate fix, #142 SSH CA minting; live Docker canaries pass; all M17 issues closed; coverage 86.24% |
| [CHG-0153](done/CHG-0153-incident-replay/plan.md) | Issue #206: `hubctl replay export` sanitized bundle (digest-verified prompt for sensitive jobs) + `replay run` on an isolated spool with channel forced to mock + divergence line (SPEC-0076 R6) |
| [CHG-0152](done/CHG-0152-admin-diagnostics/plan.md) | Issue #141: `scopes:["admin"]` on enrolled bearers, serialized + paced `/v1/admin/onboardings|diagnose|inspect-source`, audit-tagged; capability fields never leave (SPEC-0076 R5) |
| [CHG-0151](done/CHG-0151-image-rollout/plan.md) | Issue #198: `generated/image-pins.json`, smoke-gated promote/revert/canary, spawn-time resolution, forged-pin read rejection (SPEC-0076 R4) |
| [CHG-0150](done/CHG-0150-job-watch/plan.md) | Issue #197b: `hubctl watch` — timestamped job/runtime transitions from spool mapping or supervisor (SPEC-0076 R3) |
| [CHG-0149](done/CHG-0149-space-manifest/plan.md) | Issue #197a: `apply -f` `space:` block converges agent.yaml/workspace.yaml — validate-before-write, idempotent, secrets rejected (SPEC-0076 R2) |
| [CHG-0148](done/CHG-0148-usage-export/plan.md) | Issue #195: `hubctl usage` — per-scope/per-actor window aggregation of `usage.jsonl`, torn-tail tolerant (SPEC-0076 R1) |

## Done — M13+ platform and fixes

| Change | State |
|---|---|
| [CHG-0233](done/CHG-0233-live-tool-inventory/plan.md) | Done 2026-10-30: live caller-scoped tool inventory — `tools_list` gateway tool (recomputed per call from ListProjectedTools + governance filter; compact names by default, desc/schema only on query/name, bounded, no secret fields) + `invoke` admitted under managed connectors bundle (personal/personal+team; `""` keeps strict control_operations, `none` denies); ChannelContract line + ADR-0089 + SPEC-0023 + tools.md/rbac.md; regression tests cover isolation, mid-session enable/disable, managed end-to-end invoke over real streamable HTTP; all gates green except unrelated Windows file-lock flake in internal/mediasvc (passes standalone); rebind+respawn of managed spaces and live acceptance still pending deployment |
| [CHG-0191](done/CHG-0191-i18n-user-locale/plan.md) | Done 2026-10-27: channel-agnostic user locale — en default + ru/kk/es, workspace prefs + detection ladder + /language picker, all deterministic surfaces localized (chat, HTML, broker pages, operator tooling), model prompts EN + lang hint, per-language TTS voices, contract i18n schema; just check green 86.32%, broker check 89.89%; live channel acceptance not run |
| [CHG-0175](done/CHG-0175-coverage-profile-safety/plan.md) | Done 2026-10-26: M18 #147 verification prerequisite — malformed coverage locations rejected, per-package profiles isolated, statement-free packages skipped; covergo green 86.03% |
| [CHG-0169](done/CHG-0169-site-hosting-design/plan.md) | M18 #147: research including pinned Haloy review; ADR-0081 accepted, SPEC-0079 frozen, implementation CHG-0170–0174 allocated; design only |
| [CHG-0163](done/CHG-0163-recovery-budget/plan.md) | Done 2026-10-25: stop stale observations after budget notice; gateway deployed; just check passed, coverage 86.55% |
| [CHG-0161](done/CHG-0161-manual-compact-engines/plan.md) | Done 2026-10-25: #309 manual /compact|/compress — dual engine behind grants; live: hub summary 19928→2398 via relay, native archive_and_compact, preview/queue/busy-refusal paths verified end-to-end |
| [CHG-0160](done/CHG-0160-context-control-foundation/plan.md) | Done 2026-10-25: #310 compaction policy + sidecar + journal/status + between-jobs sweep — live accepted on dev stack (3 real folds, lease/cooldown refusals journaled) |
| [CHG-0159](done/CHG-0159-group-modes/plan.md) | Done 2026-10-25: #207 follow-up per-group topics/mention modes — live accepted on real Telegram (topics → per-topic sessions in sender home, mention → getMe-default tag into dedicated space) |
| [CHG-0158](done/CHG-0158-knowledge-lifecycle/plan.md) | Done 2026-10-25: M16 #249 knowledge-lifecycle — live accepted, issue closed |
| [CHG-0152](done/CHG-0152-todo-contract/plan.md) | Done 2026-10-25: M16 #183 todo-contract — live accepted, issue closed |
| [CHG-0157](done/CHG-0157-idle-proof/plan.md) | Done 2026-10-24: M16 #214 idle-proof — live accepted, issue closed |
| [CHG-0155](done/CHG-0155-group-scopes/plan.md) | Done 2026-10-24: M16 #207 group-scopes — live accepted, issue closed; follow-up CHG-0159 added per-chat modes |
| [CHG-0154](done/CHG-0154-context-discipline/plan.md) | Done 2026-10-24: M16 #196 context-discipline — live accepted, issue closed |
| [CHG-0153](done/CHG-0153-memory-boundary/plan.md) | Done 2026-10-24: M16 #184 memory-boundary — live accepted, issue closed |
| [CHG-0150](done/CHG-0150-m15-completion/plan.md) | Done 2026-10-24: M15 completion — live acceptance on dev stack for #131/#182/#185/#150 (E2E-021 PASS, real agent+provider, mock transport); environment-limited items documented; dedicated-service shared-alias DNS collision recorded as follow-up (renumbered from duplicate CHG-0145) |
| [CHG-0149](done/CHG-0149-connector-recovery-and-guide/plan.md) | Done 2026-10-24: anchored Broker recovery with independent key, durable reconciliation retries, gosec gate fixed cross-platform, internal key/token rotation, live inventory consistency across surfaces, system guide delivered and verified against a real model |
| [CHG-0148](done/CHG-0148-connector-discovery-onboarding/plan.md) | Done 2026-10-22: discovery, exact selection and 13 source-to-form/confirmation checks |
| [CHG-0146](done/CHG-0146-oauth-rotation-state-loss/plan.md) | OAuth re-rotation dropped the callback token (new binding → empty broker state); wizard-link agent copy; broker revoke retires grants and logs lease deny reasons |
| [CHG-0145](done/CHG-0145-single-link-onboarding/plan.md) | Single-link onboarding wizard: one stable /connect/<id>?k= URL drives credentials, broker hop, confirm and provider OAuth; comm-hub edits one message in place; `just check` green, dev images live |
| [CHG-0142](done/CHG-0142-credential-broker-mvp/plan.md) | Credential Broker module import MVP (imported 2026-10-20 from services/credential-broker .work/done CHG-001; number taken after 0141) |
| [CHG-0138](done/CHG-0138-m11-session-ux-closeout/plan.md) | M11 closeout: /stop+/cancel, archive UX, live progress, e2e runner — issues #217/#218/#219/#194/#193; Telegram live acceptance still pending owner |
| [CHG-0137](done/CHG-0137-qr-intake-default/plan.md) | QR login compiled into the one gateway, default-on for telegram stacks; live-verified 2026-10-19 |
| [CHG-0104](done/CHG-0104-event-streams/plan.md) | M12 follow-up: stream event subscriptions (delivery=stream); live-verified 2026-10-19 |
| [CHG-0135](done/CHG-0135-m13-p8-browser-connectors/plan.md) | M13 P8 browser workloads as connectors (ADR-0059) |
| [CHG-0134](done/CHG-0134-m13-p7-team-connectors/plan.md) | M13 P7 explicit team connectors (ADR-0058) |
| [CHG-0133](done/CHG-0133-m13-p6-broker-authority/plan.md) | M13 P6 Credential Broker as single credential authority (ADR-0049) |
| [CHG-0132](done/CHG-0132-m13-p5-catalog-tiers/plan.md) | M13 P5 catalog tiers + CI-built images + install budgets (ADR-0057) |
| [CHG-0131](done/CHG-0131-m13-p4-ownership-finalizers/plan.md) | M13 P4 ownership labels + finalizers + reconciler (ADR-0056) |
| [CHG-0130](done/CHG-0130-m13-p3-connector-surface/plan.md) | M13 P3 unified connector_* tools + operation journal (ADR-0055) |
| [CHG-0129](done/CHG-0129-m13-p2-human-action-surface/plan.md) | M13 P2 channel-agnostic human action surface — one-time links (ADR-0054) |
| [CHG-0128](done/CHG-0128-m13-p1-recipe-model/plan.md) | M13 P1 recipe model, trust ladders, source ledger (ADR-0053) |
| [CHG-0127](done/CHG-0127-m13-p0-connector-path/plan.md) | M13 P0 stabilize the connector path (issue #281) |
| [CHG-0127](done/CHG-0127-watcher-failure-visibility/plan.md) | Issue #248 live-run fixes: probe-failure alerts, vacuous-value rejection, quiet escalation, drift noise; live re-acceptance passed (number collides with m13-p0) |
| [CHG-0116](done/CHG-0116-watcher-probe-pipeline/plan.md) | Watcher fetch/extract probe pipeline (SPEC-0059, ADR-0045) |
| [CHG-0109](done/CHG-0109-fast-local-rebuild/plan.md) | Fast local Go service rebuild; live acceptance passed |
| [CHG-0108](done/CHG-0108-full-credential-coverage/plan.md) | Full credential coverage — file/shared/http_header connectors (broker ADR-0009/0010) |
| [CHG-0107](done/CHG-0107-dynamic-contracts/plan.md) | Dynamic credential contracts (POST /v1/contracts + ToolHub synthesis), live-verified rotate/self-service |
| [CHG-0106](done/CHG-0106-m13-auth-mcpb/plan.md) | M13 auth completion + full MCPB support (number collides with live-replay-defects) |
| [CHG-0106](done/CHG-0106-live-replay-defects/plan.md) | Live-replay defect fixes — watcher probes, session leak routing, contract errors, retry, broker catalog |
| [CHG-0105](done/CHG-0105-m13-closeout/plan.md) | M13 ToolHub connector platform closeout (number collides with broker-drift-self-heal) |
| [CHG-0105](done/CHG-0105-broker-drift-self-heal/plan.md) | Broker drift self-heal, agent tool guidance, stale artifact reuse; live replay verified |

## Done — comm-plane separation (M14 refactor)

| Change | State |
|---|---|
| [CHG-0126](done/CHG-0126-spool-knowledge-migration/plan.md) | hubctl spool migrate — offline knowledge copy spool→state (ADR-0050) |
| [CHG-0125](done/CHG-0125-domain-model-relocation/plan.md) | commodel/channel/routines packages, engine relocation, conformance + goldens (ADR-0046/0048) |
| [CHG-0124](done/CHG-0124-serviceauth/plan.md) | internal/serviceauth extraction |
| [CHG-0123](done/CHG-0123-commops-extraction/plan.md) | Canary/health ops extraction to commops |
| [CHG-0122](done/CHG-0122-channel-registry/plan.md) | Channel registry — polymorphic delivery/authorization dispatch (ADR-0047) |
| [CHG-0121](done/CHG-0121-knowledge-to-runtime/plan.md) | Knowledge moves from communication spool to runtime state |
| [CHG-0120](done/CHG-0120-broker-only-credential-intake/plan.md) | Legacy credential surface leaves communication (ADR-0049) |
| [CHG-0119](done/CHG-0119-quota-to-model-relay/plan.md) | Quota lookup moves from communication to model-relay |
| [CHG-0118](done/CHG-0118-comm-p0-routine-isolation/plan.md) | Routine tick isolation and boundary hardening |

## Done — routines, watchers, M12

| Change | State |
|---|---|
| [CHG-0115](done/CHG-0115-routine-snooze-audit/plan.md) | Routine snooze acceptance correction |
| [CHG-0114](done/CHG-0114-routine-destinations/plan.md) | Routine delivery destinations (SPEC-0058, ADR-0044) |
| [CHG-0113](done/CHG-0113-compact-routine-tools/plan.md) | Five public routine tools; deployed MCP lifecycle verified (ADR-0043) |
| [CHG-0112](done/CHG-0112-routine-lifecycle/plan.md) | Full routine lifecycle and interval windows; real Telegram verified (ADR-0042) |
| [CHG-0111](done/CHG-0111-durable-reminder-acceptance/plan.md) | Issue #247 reminder recovery, snooze, history isolation, real Telegram acceptance |
| [CHG-0103](done/CHG-0103-event-subscriptions/plan.md) | M12 stage 5: event subscriptions, sinks, watch_* surface; live verified, issue #257 closed |
| [CHG-0102](done/CHG-0102-owner-brief/plan.md) | M12 stage 4: opt-in owner brief; live acceptance passed |

## Done — M19 execution chain and supervisor

| Change | State |
|---|---|
| [CHG-0097](done/CHG-0097-single-stack-supervisor/plan.md) | Single-stack supervisor verified live — container supervisor wake/restart (ADR-0040) |
| [CHG-0096](done/CHG-0096-control-plane-simplification/plan.md) | Control-plane simplification proposals (M19 stage 4) |
| [CHG-0095](done/CHG-0095-recovery-observability/plan.md) | Recovery and visibility (M19 stage 3); verified live |
| [CHG-0094](done/CHG-0094-chain-latency/plan.md) | M19 stage 2: early liveness, single progress, event-driven spool, lazy browser |
| [CHG-0093](done/CHG-0093-tool-surface-integrity/plan.md) | M19 stage 1: rights-only policy hash, binding migration, tool manifest readiness |
| [CHG-0092](done/CHG-0092-chain-stop-the-bleeding/plan.md) | M19 stage 0: idle deadlines, pre-admission retry, lease-safe lifecycle; verified live |

## Done — M10 stabilization and M11 operations

| Change | State |
|---|---|
| [CHG-0090](done/CHG-0090-deployment-contracts/plan.md) | Deployment contracts (SPEC-0051) |
| [CHG-0089](done/CHG-0089-routine-control-env/plan.md) | Routine control environment in legacy hub MCP |
| [CHG-0088](done/CHG-0088-telegram-approval-lifecycle/plan.md) | Telegram approval lifecycle and read-only workspace listing |
| [CHG-0087](done/CHG-0087-live-phase1/plan.md) | Live phase 1 capability checks |
| [CHG-0086](done/CHG-0086-inbound-documents/plan.md) | Inbound Telegram documents |
| [CHG-0085](done/CHG-0085-e2e-ci/plan.md) | Sequential capability acceptance and CI latency |
| [CHG-0084](done/CHG-0084-m10-dev-merge/plan.md) | M10 merge into dev, shared image build discipline |
| [CHG-0083](done/CHG-0083-m10-stabilization/plan.md) | M10 phases A–C stabilization (SPEC-0050) |
| [CHG-0082](done/CHG-0082-m10-phase-c/plan.md) | M10 Phase C — operability: mock channel, apply -f, image discipline |
| [CHG-0081](done/CHG-0081-m10-phase-b/plan.md) | M10 Phase B — proactive assistant: watchers, follow-ups, digest, knowledge |
| [CHG-0080](done/CHG-0080-m10-phase-a/plan.md) | M10 Phase A — cheap wins |

## Done — CLI transport and tool governance (M8/M9)

| Change | State |
|---|---|
| [CHG-0079](done/CHG-0079-cli-policy-completion/plan.md) | Six CLI policy/catalog gaps (SPEC-0063) |
| [CHG-0078](done/CHG-0078-cli-cells/plan.md) | Per-principal sandbox cells, issues #239–#242, live verified |
| [CHG-0077](done/CHG-0077-toolhub-cli/plan.md) | ToolHub CLI connectors (SPEC-0062) |
| [CHG-0076](done/CHG-0076-tool-governance/plan.md) | Tool governance — inventory, scoped policy, grants, enforcement (SPEC-0046) |
| [CHG-0075](done/CHG-0075-remote-mcp/plan.md) | Owner-scoped remote MCP register/admit/project (SPEC-0045) |
| [CHG-0074](done/CHG-0074-telegram-auth-plugin/plan.md) | Opt-in Telegram QR login plugin (SPEC-0044; number collides with host-hygiene) |
| [CHG-0074](done/CHG-0074-host-hygiene/plan.md) | Host hygiene batch (issues 226→222→224→223→221+225→171→46) |
| [CHG-0073](done/CHG-0073-prepared-bundle-telegram/plan.md) | Prepared bundle and personal Telegram (SPEC-0043; live provider acceptance recorded as limitation) |

## Done — managed stack, media, diagnostics, connectors (M5–M9 mid)

| Change | State |
|---|---|
| [CHG-0072](done/CHG-0072-managed-hotfixes/plan.md) | Managed-mode hotfixes from live Telegram verification |
| [CHG-0071](done/CHG-0071-managed-voice-media-settings/plan.md) | Managed compression, settings tools, image gen, STT/TTS, second user |
| [CHG-0070](done/CHG-0070-managed-cutover/plan.md) | Managed capability cutover for the local space |
| [CHG-0069](done/CHG-0069-session-ux-and-quota/plan.md) | Session UX and provider quota |
| [CHG-0068](done/CHG-0068-usage-report/plan.md) | Real task/session usage report (issue 169) |
| [CHG-0067](done/CHG-0067-task-style/plan.md) | Per-task communication style (issue 168) |
| [CHG-0066](done/CHG-0066-task-sessions/plan.md) | Task sessions and Telegram DM topics (issue 167) |
| [CHG-0064](done/CHG-0064-capability-profile/plan.md) | Standard user-scoped capability profile (SPEC-0040) |
| [CHG-0063](done/CHG-0063-media-generation-service/plan.md) | hub-media generation sidecar (SPEC-0061); verified live |
| [CHG-0062](done/CHG-0062-media-sidecars/plan.md) | Standalone hub-stt/hub-tts services (SPEC-0039); verified on local stack |
| [CHG-0061](done/CHG-0061-voice-readiness/plan.md) | /voice reports STT and TTS readiness separately (SPEC-0038) |
| [CHG-0060](done/CHG-0060-artifact-delivery/plan.md) | Run artifact handoff (SPEC-0037) |
| [CHG-0059](done/CHG-0059-channel-rendering/plan.md) | Per-channel rendering and long replies (SPEC-0036, issue 166) |
| [CHG-0058](done/CHG-0058-web-search-research/plan.md) | Web search and bounded deep research (SPEC-0035, issue 123) |
| [CHG-0057](done/CHG-0057-datalens-connector/plan.md) | Prepared DataLens connector (issue 120) |
| [CHG-0056](done/CHG-0056-observability-connectors/plan.md) | Prepared observability connectors: grafana + prometheus (issue #43) |
| [CHG-0055](done/CHG-0055-sql-connectors/plan.md) | Prepared SQL connectors txttsql and dbhub |
| [CHG-0054](done/CHG-0054-diagnostics-hardening/plan.md) | Diagnostics hardening |
| [CHG-0053](done/CHG-0053-user-diagnostics/plan.md) | User-scoped diagnostics (SPEC-0034) |
| [CHG-0052](done/CHG-0052-document-image-tools/plan.md) | Workspace document and image capability (SPEC-0033) |
| [CHG-0051](done/CHG-0051-config-gated-empty-tools/plan.md) | Config-gated empty tools/list (mcp-atlassian) |
| [CHG-0050](done/CHG-0050-artifact-image-restore/plan.md) | Restore pruned artifact images at admission |
| [CHG-0049](done/CHG-0049-install-progress-notices/plan.md) | Install progress on the form and in Telegram |
| [CHG-0048](done/CHG-0048-network-transport-and-proxy-discovery/plan.md) | Network-transport fallback + proxy-env discovery |
| [CHG-0047](done/CHG-0047-ssh-capability/plan.md) | Opt-in SSH capability (SPEC-0032; number collides with mcp-admission-and-wake) |
| [CHG-0047](done/CHG-0047-mcp-admission-and-wake/plan.md) | MCP admission, sibling tokens, lost first message; verified |
| [CHG-0046](done/CHG-0046-automatic-diagnostics/plan.md) | Automatic bounded diagnostics (SPEC-0031) |
| [CHG-0045](done/CHG-0045-diagnostics/plan.md) | Local multiuser diagnostics (SPEC-0030) |
| [CHG-0044](done/CHG-0044-build-disk-control/plan.md) | Build disk control and core/control image split (SPEC-0029) |
| [CHG-0043](done/CHG-0043-async-prepare/plan.md) | Async connector prepare |
| [CHG-0042](done/CHG-0042-toolhub-build-resilience/plan.md) | ToolHub build resilience |

## Done — M5 connector platform era

| Change | State |
|---|---|
| [CHG-0041](done/CHG-0041-browser-profiles/plan.md) | Isolated user browser profiles (issue #45) |
| [CHG-0040](done/CHG-0040-shared-toolhub-control-plane/plan.md) | Shared control plane, scoped compute (ADR-0026) |
| [CHG-0039](done/CHG-0039-parallel-context-workers/plan.md) | Parallel per-context gateway workers (ADR-0025; number collides with oauth-handoff) |
| [CHG-0039](done/CHG-0039-oauth-handoff/plan.md) | OAuth handoff for prepared MCP connections |
| [CHG-0038](done/CHG-0038-multiowner-telegram-runtime/plan.md) | Multi-owner Telegram runtimes |
| [CHG-0036](done/CHG-0036-repository-driven-toolhub/plan.md) | Repository-driven ToolHub — prepared data, discovery, four exact-source live scenarios |
| [CHG-0035](done/CHG-0035-toolhive-vmcp-audit/plan.md) | ToolHive local vMCP evaluation — no-go evidence (SPEC-0027, ADR-0024) |
| [CHG-0034](done/CHG-0034-noncredential-env-filter/plan.md) | Non-credential env names excluded from connection recipe; gitlab-pat broker contract |
| [CHG-0033](done/CHG-0033-node-bin-aliases/plan.md) | Node bin aliases resolve as a single MCP entrypoint |
| [CHG-0032](done/CHG-0032-broker-direct-form/plan.md) | Credential Broker direct form and expired-link regeneration |
| [CHG-0031](done/CHG-0031-docker-reclaim/plan.md) | Docker reclaim: stale tags, dangling and artifact-build orphans via devcheck docker-clean |
| [CHG-0030](done/CHG-0030-compose-consolidation/plan.md) | Compose consolidation refactor |
| [CHG-0029](done/CHG-0029-github-mcp-install/plan.md) | Generic GitHub MCP install; live end-to-end verified |
| [CHG-0028](done/CHG-0028-credential-broker-merge/plan.md) | Credential Broker merge and opt-in integration (ADR-0022, SPEC-0025) |
| [CHG-0027](done/CHG-0027-m53-e2e/plan.md) | M5.3 Hermes-mediated MCP onboarding; live Notion e2e verified (number collides with recipe-catalog-discovery) |
| [CHG-0027](done/CHG-0027-recipe-catalog-discovery/plan.md) | Complete recipe catalog discovery |
| [CHG-0026](done/CHG-0026-user-binding-journey/plan.md) | M5.2 control MCP, grants, elicitation, bind/project/revoke (number collides with recipe-resolver) |
| [CHG-0026](done/CHG-0026-recipe-resolver/plan.md) | Recipe resolver (SPEC-0026) |
| [CHG-0025](done/CHG-0025-trusted-artifacts/plan.md) | M5.1 trusted artifacts + local ToolHive; [closeout](done/CHG-0025-trusted-artifacts/closeout.md) |
| [CHG-0024](done/CHG-0024-personal-data-connectors/plan.md) | M5 stage 2: Google Workspace, Slack, personal Telegram behind ToolHub (SPEC-0022) |

## Done — foundations (pre-M5)

| Change | State |
|---|---|
| [CHG-0023](done/CHG-0023-personal-assistant/plan.md) | M4 personal assistant: Telegram/Slack channels, voice, routines, memory/skills/backup |
| [CHG-0022](done/CHG-0022-credentials-connector-platform/plan.md) | M3 shipped-path inject/audit/registry wiring |
| [CHG-0021](done/CHG-0021-toolhub-stage3/plan.md) | M2 control plane; issues #20–#26 closed |
| [CHG-0020](done/CHG-0020-toolhub-stage2/plan.md) | M2 stage 2: authenticated projection, ToolHive adapter, bounded CLI |
| [CHG-0019](done/CHG-0019-toolhub-foundation/plan.md) | M2: immutable ToolHub definitions, workload classes, exact-owner bindings |
| [CHG-0018](done/CHG-0018-communication-runtime-migration/plan.md) | Issue #19: per-context execution selection and five-minute gateway lifecycle |
| [CHG-0017](done/CHG-0017-stream-approval-reconciliation/plan.md) | Issues #16–#18: stream, approval, reconciliation |
| [CHG-0016](done/CHG-0016-scale-to-zero-runtimes/plan.md) | Initial host supervisor, warm lifecycle, opt-in routing |
| [CHG-0015](done/CHG-0015-migration-map/plan.md) | Current-to-target migration and deprecation map (number collides with durable-job-runtime-mappings) |
| [CHG-0015](done/CHG-0015-durable-job-runtime-mappings/plan.md) | Durable job and runtime mappings (SPEC-0014) |
| [CHG-0014](done/CHG-0014-threat-model/plan.md) | Threat model for personal deployment and future company boundary |
| [CHG-0013](done/CHG-0013-dependency-refresh/plan.md) | Reviewed GitHub Actions and Docker dependency updates |
| [CHG-0012](done/CHG-0012-hermes-api-contract/plan.md) | Validate pinned Hermes API contract (ADR-0012, SPEC-0011) |
| [CHG-0011](done/CHG-0011-stable-identifiers/plan.md) | Stable identity and ownership identifier contract (ADR-0011) |
| [CHG-0010](done/CHG-0010-direct-mcp-atlassian/plan.md) | Pinned direct mcp-atlassian replaces Rovo (ADR-0010) |
| [CHG-0009](done/CHG-0009-scoped-homes-and-service-split/plan.md) | Scoped homes and communication-hub split (ADR-0009, SPEC-0009) |
| [CHG-0008](done/CHG-0008-telegram-service-self-service/plan.md) | Telegram connector catalog and self-service (ADR-0008) |
| [CHG-0007](done/CHG-0007-communication-gateway/plan.md) | Communication gateway (ADR-0007, SPEC-0007) |
| [CHG-0006](done/CHG-0006-work-services/plan.md) | Work-service integrations (SPEC-0006) |
| [CHG-0005](done/CHG-0005-self-env/plan.md) | User self-env and independent Telegram paths (SPEC-0005) |
| [CHG-0004](done/CHG-0004-multitenant-knowledge/plan.md) | Organization/user scope overlay (SPEC-0004) |
| [CHG-0003](done/CHG-0003-go-first-tooling/plan.md) | Go-first tooling (ADR-0003, SPEC-0003) |
| [CHG-0002](done/CHG-0002-standalone/plan.md) | Standalone general-purpose scope (SPEC-0002) |
| [CHG-0001](done/CHG-0001-bootstrap/plan.md) | Initial bootstrap |

Open live follow-ups stay as GitHub issues, not stale CHG folders: #73
(ToolHive/VPS), #74 (Hermes reconnect), #166/#170/#172 (live channel
acceptance evidence).
