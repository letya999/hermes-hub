# CHG-0188 research: agent skills platform (M24)

Evidence for ADR-0084 / SPEC-0083. Three parts: current-state audit of the hub,
prior-art survey, and the verification spike against pinned Hermes `869228c`
(0.21.0). Commands and observed output are quoted; names only for space content.

## 1. Current state (this hub)

| Surface | File | Observed behavior |
|---|---|---|
| Skill registry | `internal/skills/skills.go` | `skills/registry.json` + `hermes/skills/<name>/SKILL.md` writer. Never consulted by the runtime render (`Config` in `internal/stack/render.go` reads none of it). Owner check at lines 98–103 is a no-op: the `invalid owner` error fires only when `!ValidID`, which the earlier `||` clause already short-circuits for most inputs; a foreign principal slips through when `reg.User` is empty. |
| Scanner | `internal/skills/skills.go:85` `ScanSkillText` | `envstore.LooksLikeEnv` + 5 fixed substrings (`org_actions`, `telegram_bot_token`, `slack_signing_secret`, `hub_toolhub`, `provider mutation`); `Install` then hardcodes `Scan: "pass"`. |
| Registry write | `internal/skills/skills.go:78` | `path + ".tmp"` fixed temp name — two concurrent saves collide on the same `.tmp` file. |
| Unmanaged toolsets | `internal/stack/render.go:99` | Hardcoded base list `terminal, file, web, skills, todo, cronjob, messaging, memory, session_search` always emitted; `tools:` entries only add/remove. |
| Native defaults | `internal/toolhub/api/governance_io.go:17` `SeedInventory` | Of 29 `native:*` sections only `terminal`, `code_execution` are `RuleDeny`; `skills`, `memory`, `browser`, `kanban`, `spotify`, `homeassistant`, etc. all `RuleAllow`. |
| Implicit sections | `internal/stack/governance.go:102` `implicitToolsetSections` | Unmanaged mode evaluates `hub:tools` + the same 9 native toolsets as implicit platform emissions. |
| Skills delivery | `internal/stack/render.go:135-154` | `skills.external_dirs = [/org/hermes/skills, /opt/hub/skills]`; `skills.disabled` lists only feature-gated bundled names. Precedence is upstream directory order, not hub policy. |
| Hub skills mount | `internal/stack/compose.go:191`, `internal/supervisor/ensure.go:474-479` | Per-space filtered copy `generated/skills` mounted ro at `/opt/hub/skills` (unmanaged). Supervisor materializes it with **fail-open** (`log.Printf` + continue). Managed mode instead mounts ro tmpfs over `/state/hermes/skills`. |
| Managed carve-outs | `internal/stack/hermes_capability.go` `nativeCarveouts`, `materializeHermesConfig` | Managed config rebuilds the toolset list from the pinned capability inventory; `skills` is present in the inventory as a normal toolset. |
| Space init | `internal/stack/space.go` `initScopeEnvironment` | Creates `hermes/`, `workspace/`, `archive/`, `connections/` — no `skills/` store dir. |
| Bundled seeding | `spaces/local/hermes/skills/` (live space, names only) | `.bundled_manifest` present; category dirs `apple, autonomous-ai-agents, creative, devops, email, media, note-taking, productivity, research, social-media, software-development, web` — the ~60 upstream bundled skills seeded into the user home. |
| Curator ran live | `spaces/local/hermes/skills/.curator_ledger.jsonl` (name only) | Upstream curator mutated skills inside a user space without owner approval — `.curator_state` and ledger exist. Rendered config contains no `curator:` key, so the upstream default `enabled: true` applied. |
| Stale copy | `spaces/local/hermes/skills/mcp-connector-onboarding` | A copy of the platform skill `config/skills/mcp-connector-onboarding` drifted into the user home — copies rot (directory name also mismatches its own frontmatter `hermes-hub-mcp-onboarding`). |
| Platform skills | `config/skills/` | Two dirs: `deep-research-embedded`, `mcp-connector-onboarding`; system guide is generated (`internal/systemguide`). |

## 2. Prior-art survey (harnesses)

References live under `../_research-harnesses/` and `../open-duet/`; summarized
positions, not re-verified behavior.

- **openclaw** (`openclaw-openclaw/docs/tools/skills.md`, `skill-workshop.md`,
  `self-learning.md`, `skills-config.md`): revisioned store with content-hash
  revisions + session selection; per-skill advisory locks; prompt-budgeted
  catalog (`maxSkillsPromptChars`) with names-then-search fallback; install
  policy hook; `origin.json` + lockfile for git installs; `/learn` foreground
  capture; one-change-per-review self-learning with bounded excerpts; taint
  on web-sourced drafts; `$name` prompt references. Adopt: revisions+locks,
  budgeted catalog, draft cards, origin lockfile, excerpt bounds.
- **goclaw-nextlevelbuilder** (`07-bootstrap-skills-memory.md`,
  `14-skills-runtime.md`, `15-core-skills-system.md`, `16-skill-publishing.md`,
  `21-agent-evolution-and-skill-management.md`): inline-vs-search thresholds
  (~20 skills / ~3.5k tokens); versioned store + advisory lock; `deps:`
  grammar (`pip:`/`npm:`/`system:`) with per-category allowlist; skill_evolve
  suggestion-only mode + postscript consent; 25-rule/6-category guard;
  usage metrics. Adopt: thresholds, deps grammar, guard taxonomy, metrics.
- **goclaw-roelfdiedericks** (`docs/skills.md`): security auditor pass on
  agent-written skills. Adopt: output-side scanning (also SkillJack).
- **Codex** (`codex/codex-rs/ext/skills/`): catalog prompt generation,
  `agents/openai.yaml` loader (`dependencies.tools`, `policy.allow_implicit_invocation`),
  dynamic selector. Adopt: dependency normalization, implicit-invocation flag.
- **opencode** (`packages/core/src/skill.ts`): `skill` tool shape
  (`activate_skill`), permission check. Adopt: single activation tool name.
- **eve** (`docs/skills.mdx`, `guides/dynamic-capabilities.md`,
  `concepts/security-model.md`): deferred skills + search; per-tenant dynamic
  skills; remote self-modification via draft PR only. Adopt: deferred loading.
- **nanoclaw** (`docs/architecture.md`): agent requests, host decides —
  supervisor proposes diffs, user approves. Adopt: proposal/approve split.
- **iva-agent** (`docs/plugins.md`, `docs/extending.md`): owner rules,
  custom dir untouched by releases. Adopt: store separation.
- **open-duet** (`backend/app/services/agent_skills.py`): atomic directory
  writes, symlink refusal, secret `_WARNINGS` + `redact_secrets` with visible
  warning. Adopt: atomic swap, redaction-on-import.
- **OpenDots** (`docs/SETUP.md`): learned skills require publish. Adopt:
  draft-first evolution.
- **Hermes upstream** (pinned source, see §3): `skills_list`/`skill_view`/
  `skill_manage` shapes, per-action field rejection, `write_approval`
  pending/diff/approve flow, curator with ledger+backups, slash stacking,
  `external_dirs` read-only to curator, `ESSENTIAL_SKILLS` guard.
- **Claude Code** (docs): `disable-model-invocation`, `user-invocable`,
  `argument-hint`, `$ARGUMENTS`. Adopt: compat fields + explicit invocation.
- **skill-conductor** (`../skill-conductor/`): local eval tooling; reused
  conceptually for M24.12 evals.

## 3. Verification spike — pinned Hermes `869228c` (0.21.0)

Fetched with the same pin as `docker/Dockerfile:62`:

```bash
git init ../.m24-tmp/hermes-src && git remote add origin https://github.com/nousresearch/hermes-agent.git
git fetch --depth 1 origin 869228cab4a8276d3b4c78da9d9939670c47bd0f && git checkout FETCH_HEAD
# HEAD: feat(delegation): surface process accounting as top-level process_notes ...
```

(kept outside the repo tree: `devcheck docs` walks every `.md` under `.work`,
so a 255 MB upstream source copy inside `.work/tmp` breaks the docs gate;
outside the repo it is also uncommittable by construction.)

Static verification of each assumption the design relies on (live runtime
legs run in M24.13 on the dev stack; Docker work was out of this session's
scope):

| # | Assumption | Result at 869228c | Evidence |
|---|---|---|---|
| 1 | Disabling `skills` toolset removes `skills_list`/`skill_view`/`skill_manage` and the skills index on every platform | **Confirmed** | `toolsets.py:98-102` maps `"skills"` → exactly `["skills_list","skill_view","skill_manage"]`; `agent/system_prompt.py:301` builds the index only `if any(name in agent.valid_tool_names for name in ['skills_list','skill_view','skill_manage'])` — no tools, no index. `agent.disabled_toolsets` is end-of-pipeline subtraction on every platform (`toolsets.py` comment, upstream #17309), so it also covers platforms missing from `platform_toolsets`. |
| 2 | Curator does not run when disabled; `.curator_state` untouched | **Confirmed with caveat** | `agent/curator.py:143` gates on `curator.enabled` (default `True`, `config_defaults.py:1360`); it is invoked best-effort at session start (`cli.py:3713`, `gateway/run.py`) and is **not** tied to the skills toolset — the render must emit `curator.enabled: false` explicitly. Writes are `.curator_ledger.jsonl` + `.curator_backups/` + `.curator_state`. |
| 3 | `skills.write_approval` / `guard_agent_created` present | **Confirmed** | `config_defaults.py:1349` `"write_approval": False` (stages `skill_manage` mutations through `/skills pending|diff|approve|reject`); `:1340` `"guard_agent_created": False`; `:1353` `"ledger": True`. Both gates usable as defense in depth behind the hub's own draft flow. |
| 4 | Compaction risks loaded skill bodies | **Confirmed risk** | `agent_init.py:1473` `proactive_prune_min_result_chars` default **8000**; `agent/context_compressor.py` `_demote_tool_result_at` replaces tool results with a 1-line `[tool] ... (N chars)` summary. A loaded skill body >8k chars is pruned mid-session → design needs the "re-load via `skill`" catalog instruction (S6.7) and session-scoped activation dedupe. |
| 5 | Empty read-only `/state/hermes/skills` mount tolerated | **Confirmed** | `sync_skills` is invoked via `_best_effort` (`gateway/run.py:4845-4849`); every write inside `tools/skills_sync.py` (`_install_new_skill`, `_update_existing_skill`, `_seed_category_descriptions`, `_write_manifest`) catches/`suppress(OSError)` — ro fs degrades to no-op, no startup error. |
| 6 | `.no-bundled-skills` prevents seeding | **Refuted (partial)** | `tools/skills_sync.py:367-377`: the marker switches seeding to `ESSENTIAL_SKILLS` only; `agent/skill_utils.py:270` `ESSENTIAL_SKILLS = frozenset({"hermes-agent"})` — one skill still lands whenever the dir is writable. The marker is necessary but **not sufficient**: the real guarantee is the ro mount / `skills: {}` render. `hermes-agent` is also exempt from `skills.disabled` (`skill_utils.py:285`). |

Additional observations used by the design:

- `skills.external_dirs` (`agent/skill_utils.py:333`, `config_defaults.py:1320`)
  is a flat list; lookup order is local dir then external dirs
  (`tools/skill_manager_tool.py:203`); external skills are read-only to the
  curator (`tools/skill_usage.py:256`) and refused by `skill_manage`
  (`tools/skill_manager_guards.py:180`). Hub precedence must therefore be
  decided outside Hermes — external_dirs ordering is not a policy surface.
- `_defer_to_external` (`tools/skills_sync.py:100,393`) makes bundled skills
  yield to same-named external ones — a same-named user skill also shadows a
  platform skill today (the precedence problem D5 fixes).
- `skill_manage` refuses `!command`-style load-time execution already at the
  pin; compat fields `disable-model-invocation`/`user-invocable`/
  `argument-hint`/`$ARGUMENTS` exist upstream (slash stacking in
  `gateway/slash_commands.py`).

## 4. Evidence for the problem statement

- `.curator_ledger.jsonl` exists in a live space → curator mutated skills
  unsupervised (problem statement confirmed locally).
- `.bundled_manifest` + 12 category dirs → ~60 bundled skills seeded per user.
- `internal/skills` registry never read by render → decorative authority.
- No `skills:` store per owner; org skills only via `hermes/skills` external dir.

## 5. Numbering note

Milestone text and issue #356 assumed ADR-0083/SPEC-0082/CHG-0182+ for the
skills platform. Those numbers were taken by the M26 lean-images chain
(ADR-0083, SPEC-0082, CHG-0182..0187) before M24 started. M24 therefore uses
**ADR-0084, SPEC-0083, CHG-0188+**; issue bodies referencing the old numbers
map onto these.
