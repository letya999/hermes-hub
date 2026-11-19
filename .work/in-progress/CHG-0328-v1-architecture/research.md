# CHG-0328 research: v0 evidence and prior art for M40

Gathered 2026-11-19. Prior-art facts come from primary sources fetched that
day; lines marked "secondary" were not read at the source.

## v0 evidence (repository, `dev` at dd5f4039)

- Binary closure (`go list -deps`): `cmd/runtime` 37 internal packages,
  `cmd/supervisor` 40, `cmd/communication` 44, `cmd/toolhub` 42,
  `cmd/hubctl` 58. All of runtime/supervisor/communication link
  `internal/toolhub`, `internal/toolhub/api`, `internal/stack`,
  `internal/hosting`, `internal/dns`, `internal/vault`; communication also
  links `internal/toolhub/automation`.
- Edges into the ToolHub server package: `internal/agenttools/tools.go`
  (`loadToolHub` opens the store via `HUB_TOOLHUB_STORE`),
  `internal/agenttools/tools_services.go` (`toolhub.CatalogEntry`),
  `internal/stack/manifest.go` (`migrateToolhubBindings` opens the store at
  render), `internal/stack/hosting.go` (`toolhub.GenericControllerConfig`,
  `toolhub.HostingProfile`).
- `internal/toolhub/api`: 24,013 lines in 104 non-test files; imports
  audit, credstore, identity, memory, netguard, skillstore.
- Fail-open paths: `internal/communication/gateway.go` defaults to
  `HermesRunner` (runs `hermes -z` with user state/workspace dirs) when
  `RuntimeURL` is empty; `Settings.CapabilityMode` has ~70 production
  branches in internal/stack and internal/supervisor; `internal/supervisor/env.go`
  ignored the `ReadSecrets` error.
- Path-bug directories (all empty, untracked): `.work/tmp;C` (2026-11-04),
  `services/credential-broker;C` (2026-10-17), `spaces/local/broker;C`
  (2026-10-23), `spaces/local/runtime;C` (2026-10-27),
  `C:UsersUserAppDataLocalTempfcstub` (2026-11-18). No tracked script
  produces them; they come from ad-hoc Git Bash commands (MSYS path-list
  conversion of `a:/b` volume flags, unquoted Windows paths).
- Latency: cold start ~60-80 s, warm lease ~0.6 s
  (docs/runtime-operations.md); SPEC-0053 S3 target cold start to admitted
  p95 <= 25 s.
- The dev stack already runs a bundled ZITADEL `identity-hub` and
  `hub-postgres` (ADR-0100 appendix A).

## Prior art

### Meta Muse safety architecture
- Two isolated security domains on one box: agent runtime in a
  `systemd-nspawn` cell; host services `hatch-safety` (classifiers),
  `privsep` (connector workers with per-cgroup credential allowlists),
  `hatch-authd` (credentials) and Sentinel (permissions and egress) over
  Unix sockets checked with SO_PEERCRED.
- The agent holds surrogate tokens only; Sentinel swaps in the real
  credential at the network boundary.
- Sentinel answers allow, deny or ask; grants bound to one connector or
  destination for once, session, task, time window or permanently.
  "Tainted egress": a process that touched user data loses auto-allow.
- Source: https://muse.ai/blog/security-and-safety-for-ai-agents-our-approach-with-muse

### Hermes Agent cron and kanban
- `cronjob_manage` tool; jobs in `~/.hermes/cron/jobs.json`; 60 s tick under
  a file lock; fresh session per run; cron runs cannot create cron jobs
  unless opted in.
- Execution ledger `claimed -> running -> completed | failed | unknown`;
  `unknown` is never re-run automatically; a missed fire catches up once.
- `[SILENT]` suppresses delivery; failures grouped into incidents that
  alert once, then after a cooldown.
- Kanban: SQLite board (triage, todo, ready, running, blocked, review, done,
  archived), atomic claims with TTL and heartbeat, typed blocks,
  append-only `task_events`, idempotency key, a worker must end with a
  terminal board call.
- Source: https://hermes-agent.nousresearch.com/docs/user-guide/features/cron ;
  https://hermes-agent.nousresearch.com/docs/user-guide/features/kanban

### OpenClaw automations
- Scheduler (one-shot, interval, cron), lifecycle hooks, plugin hooks,
  standing orders; heartbeats migrated into ordinary jobs; `activeHours`,
  `idleOnly`, `NO_REPLY`.
- Inferred "commitments" and the shared tasks ledger were removed in favour
  of explicit automations.
- Webhooks: dedicated bearer token, `Idempotency-Key` replays an admitted
  run, 200 means admitted (not completed), wake events can coalesce.
- Source: https://docs.openclaw.ai/automation ;
  https://docs.openclaw.ai/automation/cron-jobs/webhooks

### IronClaw (NEAR AI)
- Routines engine for cron and events/webhooks plus heartbeats; WASM tool
  sandbox with explicit opt-in to HTTP, secrets or tool calls.
- Pipeline: WASM -> allowlist -> leak scan -> credential injection ->
  execute -> leak scan; the host injects secrets, the sandbox never sees them.
- Source: https://github.com/nearai/ironclaw

### iva goals
- Not found as a goals model. smixs/iva has tasks, reminders, daily brief,
  nightly memory rollup and a Watch that stays quiet during live dialog and
  logs dropped items with a reason.
- Source: https://github.com/smixs/iva

### open-duet routines
- Anil-matcha/open-duet: routines by schedule, webhook or manual; results in
  a "Today" view with approvals and questions; modes Ask first, Accept edits,
  Approve for me, Plan only; "Routines cannot approve actions on their own".
- Source: https://github.com/Anil-matcha/open-duet

### ProactiveBench (arXiv 2410.12361)
- Event stream, agent predicts a task to offer or stays silent; 233 real
  test events. Metrics: recall, precision, accuracy, F1, false alarm
  (= 1 - precision). Reward model agrees with humans at ~92%.
- Best F1 66.47% (Qwen2-7B-Proactive, false alarm 50.22%); GPT-4o F1 64.60%.
  State of the art over-triggers.
- Source: https://arxiv.org/abs/2410.12361

### SentinelBench (arXiv 2606.05342)
- 100 monitoring tasks in 10 synthetic web apps, including 20 no-op tasks
  where the condition never fires (premature "done" fails).
- Metrics: success, reaction time, cost, premature termination.
  `wait_for(condition, timeout)` beat `sleep` loops (0.75 vs 0.68 success;
  9.7x cheaper at 40 minutes).
- Source: https://arxiv.org/html/2606.05342

### Magentic-One (arXiv 2411.04468)
- Task ledger: given/verified facts, facts to look up, facts to derive,
  educated guesses, plan. Progress ledger per step: request satisfied?
  looping? progress? next speaker? instruction?
- Stall counter; threshold 2 triggers reflect-and-replan.
- Source: https://arxiv.org/html/2411.04468v1

### CaMeL (arXiv 2503.18813)
- Privileged LLM sees only the trusted query and writes the plan; quarantined
  LLM parses untrusted data into a schema with no tools.
- Values carry capabilities (sources, readers); policies allow or deny
  flows; violations ask the user. 77% utility with provable security vs 84%
  undefended on AgentDojo.
- Source: https://arxiv.org/html/2503.18813

### Approval modes in coding agents
- Codex: sandbox `read-only`, `workspace-write`, `danger-full-access`;
  approvals `on-request`, `never`, `granular`; `untrusted` retired.
- Claude Code: `default`, `acceptEdits`, `plan`, `auto`, `dontAsk`,
  `bypassPermissions`; rules deny > ask > allow.
- Gemini CLI: `default`, `auto_edit`, `plan`, `yolo` (command line only).
- Sources: https://learn.chatgpt.com/docs/agent-approvals-security ;
  https://code.claude.com/docs/en/permissions ;
  https://geminicli.com/docs/reference/configuration

### Attention and interruption
- Horvitz (CHI '99): act / ask / stay silent thresholds on p(goal|evidence),
  shifted by the user's focus and the cost of deferral.
- Iqbal and Bailey (secondary, via survey): delivering at task breakpoints
  reduces frustration and reaction time.
- Sources: https://www.erichorvitz.com/chi99horvitz.pdf ;
  https://arxiv.org/pdf/1711.10171

## How the findings shaped the decisions

| Finding | Decision |
|---|---|
| Muse surrogate tokens, IronClaw host injection | ADR-0105 egress zone; ADR-0106 run tokens carry no secrets |
| Hermes cron `unknown` never re-run; OpenClaw 200 = admitted | ADR-0107 D6 `uncertain` and admitted vs running |
| OpenClaw coalescing, Hermes `no_agent`, SentinelBench `wait_for` | ADR-0108 D2/D4 deterministic stages before any model |
| OpenClaw dropped inferred commitments | ADR-0108 D6: agent paths create drafts only |
| Magentic-One ledgers, stall threshold 2 | ADR-0108 D5 |
| CaMeL quarantined LLM | ADR-0108 D2 and triage stage: content is data |
| Horvitz thresholds, breakpoint delivery, `[SILENT]` | ADR-0108 D8 interrupt / digest / log |
| Mode vocabularies, open-duet "routines cannot approve" | ADR-0109 D3, D7 |
| ProactiveBench false alarm ~50%, no published pass bar | SPEC-0102 thresholds relative to a recorded baseline plus absolute floors |
