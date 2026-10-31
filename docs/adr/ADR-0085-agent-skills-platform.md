---
description: Agent skills are a hub-owned capability — agentskills.io format, per-owner stores (spaces/<user>/skills, <org>/skills, config/skills), read-only effective view, draft-first writes, grant-only authority, default-deny natives, brokered secrets, draft-first evolution, pinned git installs.
last_verified: 2026-10-28
---
# ADR-0085: Agent skills platform — hub-owned stores, effective view, drafts, grants, curated evolution

Status: Proposed 2026-10-27. Implements GitHub milestone 25 "M24 — Agent
Skills Platform" (#356–#368). Spec:
[SPEC-0084](../../specs/active/SPEC-0084-agent-skills-platform.md). Evidence:
`.work/in-progress/CHG-0200-skills-platform-design/research.md` (pin spike §3).
Supersedes: the skills clause of
[ADR-0031](ADR-0031-default-deny-capability-policy.md)/SPEC-0041 CP-07
("Skills" row) and the skills sentence of SPEC-0042 (both appended, not
rewritten). Issue #356 named this record ADR-0083/SPEC-0082; the M26
lean-images chain took those numbers first (research.md §5).

## Context

Skills today are four disconnected surfaces (research.md §1):

- A decorative registry (`internal/skills`) that the runtime never consults,
  with a no-op owner check, a 5-substring scanner and a `.tmp` write race.
- A writable `hermes/skills` home where the upstream curator already mutated
  skills unsupervised (`.curator_ledger.jsonl` in a live space) and ~60
  bundled skills are seeded per user (`.bundled_manifest`).
- `skills.external_dirs` rendered from org + `/opt/hub/skills` where
  precedence is upstream directory order — a user skill shadows a platform
  one (`_defer_to_external` confirms the same for bundled).
- Native toolsets mostly default-allow (`SeedInventory`), including the
  upstream `skills` toolset that lets the model write skills directly.

The pin spike (research.md §3) verified the upstream controls this design
uses: `disabled_toolsets` subtracts leaf tools on every platform; the skills
index exists only while the `skills` toolset does; `curator.enabled` is a
real gate; `write_approval`/`guard_agent_created` exist at the pin;
`proactive_prune_min_result_chars=8000` prunes large tool results;
`.no-bundled-skills` still seeds `hermes-agent`, so the marker alone is not
the boundary.

## Decisions

- **D1 Format.** Agentskills.io: `<name>/SKILL.md`, name = directory,
  `description` ≤ 1024 chars, body < 500 lines (~5k tokens), one-level
  `references/`/`templates/`/`scripts/`/`assets/`/`evals/`. Lenient parse for
  foreign skills (warn-and-load), strict for hub-authored drafts.
- **D2 Compat normalization.** Foreign frontmatter normalizes into one
  Manifest: Hermes `platforms`, `metadata.hermes.{requires_toolsets,
  requires_tools,fallback_for_*,config}`, `required_environment_variables`;
  openclaw `metadata.openclaw.requires`; Codex `agents/openai.yaml`
  (`dependencies.tools`, `policy.allow_implicit_invocation`); Claude Code
  `disable-model-invocation`, `user-invocable`, `argument-hint`,
  `$ARGUMENTS`. Never supported: `!command` load-time execution, code-engine
  frontmatter — load never executes.
- **D3 Ownership.** The hub is the single owner and writer of skills. The
  upstream `skills` toolset (`skills_list`/`skill_view`/`skill_manage`) is
  always in `agent.disabled_toolsets`; `curator.enabled: false` is always
  rendered (the spike shows the curator is not tied to the toolset and must
  be disabled by config). Defense in depth: `skills.write_approval: true`,
  `skills.guard_agent_created: true` may also be rendered.
- **D4 Storage.** `config/skills/` (platform), `<org>/skills/`, and
  `spaces/<user>/skills/` are the only skill sources — no copies, references
  only. Per owner: `<name>/` published skills, `.drafts/<name>/<id>/`,
  `.revisions/<name>/<hash>/`, `.archive/<name>/`, `.state/` (index, usage,
  settings), `.sources/` (git mirrors). The runtime consumes a built
  read-only effective view, never the stores.
- **D5 Precedence.** platform (`hermes-hub-*` names locked) > org > user >
  catalog source `hermes`. Drafts are never active. Collisions are loud:
  rejected at write time; a late higher-tier addition hides the lower skill
  and records a notice for its owner.
- **D6 Draft-first.** Every create/patch/import/install/learned change lands
  as a draft; publishing is a human action (user scope: the owner; org scope:
  org admin/owner). The model never publishes; a draft cannot publish itself.
- **D7 Authority.** Skill text, `allowed-tools`, `deps`, declared env vars
  and evidence grant nothing. Only ToolHub grants do. Scanner findings inform
  the approver; they never widen what a skill can do.
- **D8 Native capabilities.** Every `native:*` section defaults to deny and
  renders only with a workspace wish plus an active grant; denied sections
  appear in `agent.disabled_toolsets`. Bundled upstream skills are not seeded
  — they become a read-only catalog source `hermes` pinned to the image,
  delivered through hub tools when granted.
- **D9 Delivery.** Progressive disclosure per the agentskills.io client
  guide: tier-0 catalog in the hub-composed `soul-effective` (budgeted, falls
  back to names + `skills_search`), tier-1 `skill` activation, tier-2
  `skill_view` resources. The catalog is session-pinned (one snapshot
  revision) for prompt-cache stability; mid-session changes arrive as a
  one-time note. Explicit `/name args` and `$name` resolve harness-side.
  Because compaction prunes large tool results at ~8k chars (spike #4), the
  catalog tells the model to re-load a skill that left context.
- **D10 Secrets.** Never in skill files. Declared requirements
  (`required_environment_variables`, `requires.env`, mcp tool deps) resolve
  through the credential broker: values are leased only into the cell
  executing that skill's script, for that call, scoped (principal, skill) —
  never into the Hermes process env, argv, logs or audit.
- **D11 Self-evolution.** Hub-owned background review (bounded excerpt,
  minimal prompt, one change per review, must `skill_view` before patching),
  foreground `/learn`, daily curation (stale/archive/merge). Output is
  draft-only with provenance + taint; tainted or script-bearing drafts can
  never auto-publish; archive not delete; platform/org skills are never
  mutated — suggestions are filed to their owners.
- **D12 Git installs.** GitHub/GitLab (incl. configured self-hosted) HTTPS
  URLs only; pinned commit SHA + tree hash; hub-side bare mirror with
  `origin` for fetch/diff/provenance; git runs only in an isolated cell
  (host-only egress, hooks/protocols/fsmonitor off, no submodules/LFS); no
  `.git` and no `.origin.json` in the agent-visible tree; installs and
  updates arrive as scanned drafts, never auto-applied.
- **D13 Enforcement by mount, not by config flags.** The marker
  `.no-bundled-skills` reduces upstream seeding to `hermes-agent` but does
  not stop it (spike #6), and `disabled` lists cannot touch essential skills.
  The boundary is therefore: a read-only effective view mounted at `/skills`
  plus an empty read-only `/state/hermes/skills` (upstream tolerates it —
  every skills-sync write suppresses `OSError`), `skills: {}` rendered in
  both modes, and supervisor spawn failing closed when the view cannot be
  built. Config flags (`external_dirs`, `disabled`, curator, toolset) are
  removed or pinned off entirely.

## Alternatives considered

- **Keep `internal/skills` + writable home and bolt on approval.** Rejected:
  the registry is decorative, precedence stays upstream's, the curator and
  seeding still run, and no per-owner store exists.
- **SQLite index.** Rejected: no DB dependency is allowed (AGENTS.md); JSON
  index + flock + atomic rename covers per-owner write rates.
- **Let Hermes keep the `skills` toolset and policy-gate `skill_manage`.**
  Rejected: the toolset also drives the system-prompt index, bundled seeding
  and the curator — too many escape paths; the hub owns all writes instead.
- **Copy platform/org skills into user stores.** Rejected: copies rot (the
  stale `mcp-connector-onboarding` in `spaces/local` is evidence); the
  effective view is built from references.
- **Skill text as authority (`allowed-tools`, `deps` auto-install).**
  Rejected: SPEC-0041's "text grants nothing" stays; wishes + ToolHub grants
  remain the only two keys.

## Amendments

- **D10 mechanism (CHG-0195).** "Through the credential broker" resolves to a
  (principal, skill)-bound store at `skills/.state/credentials/<skill>.json`
  — the broker module is forbidden to be a canonical secret store, so the
  owner store holds the values (0600, names `^[A-Z][A-Z0-9_]*$`, never
  `self-env.json`). The *cell lease* is `ScratchExecRequest.env` on the
  stdin channel into `docker run -i … hubctl exec-scratch` — the same
  property class as broker materializers, with the sandbox-side filter
  (env-shaped names, no hub/broker prefixes, no shadowing of the sanitized
  env, ≤16) as the declarative delivery contract. ToolHub resolves the
  lease from the ro `/skills-store` mount intersected with the effective
  index's declared names; ineligible skills and absent stores lease
  nothing, and revocation is a file delete.
- **R10 mechanism (CHG-0197).** Git execution lands in the bounded
  `/cli-exec` cell as the immutable plan `skillgit@1.0.0` — `git` stays out
  of `user_commands`; the controller admits exact tuples per allowed host
  (github.com, gitlab.com, `gitlab_host`). The cell's single rw workspace
  is the owner mirror `skills/.sources/<id>.git` behind the `skill-source`
  scope; egress is exactly the repo host; fetch is blobless
  (`--depth=1 --filter=blob:none`) and export is `git archive` into the
  bound dir — no working tree ever exists. Credentials ride `GIT_TOKEN`
  env plus a `credential.helper` shell function inside the cell — broker
  `grant` names lease it gateway-side, the operator path exports it
  directly; the token never reaches argv, the remote URL, `.git/config`,
  logs or `.origin.json`. Discovery prefers names-only `ls-tree` so a
  multi-skill repository returns candidates instead of importing blobs
  blindly. Discovery honors a repo subtree, caps at 2000 directories and
  depth 6, skips `.git`/`node_modules`, and ranks convention dirs
  (`.agents/skills`, `skills`, `.claude/skills`) first. Provenance is the
  hub-owned sidecar `.origin.json`
  (`{source_id,url,ref,commit,path,tree_sha,scan,fetched_at}`) kept out of
  draft trees — agent-authored content cannot forge origin.
- **R12 mechanism (CHG-0198).** Draft lint is a sibling pass of the scanner:
  `scan.Lint` emits advisory `Finding`s (name charset/dir match, imperative
  description ≤1024 with ~250 advisory, no "This skill" prefix, body ≤500
  lines, ≤60 files, no incident-log shape, references one level deep and
  existing) — severity never reaches critical, so lint informs the approval
  card without blocking it; dead links and spec violations stay hard
  draft-validation errors. Usage telemetry is a hub-owned journal at
  `.state/usage/<skill>.json` (`stats` counters plus a bounded event tail);
  `skill()` activation and `skill_view` file reads journal `use`/`view`
  server-side — the agent has no write path. `skills_list` projects the
  counters as `stats`; `hubctl skill stats` prints them. Eval fixtures ship
  inside the skill (`evals/triggers.json`, `evals/evals.json` — evidence
  required per case); `hubctl skill eval` materializes an isolated
  temporary space and scores validation accuracy through an injectable
  runner (production: `HUB_SKILL_EVAL_URL` returning run receipts whose
  `skill` tool calls mark activation); an unavailable model is reported
  `model_unavailable`, never fabricated. Platform skills lint clean and
  ship trigger suites; deep-research bounds remain enforced in
  `WebResearchBounds` ceilings, not skill prose. R11 (self-evolving
  skills) is deferred post-MVP — the journal file format is forward-
  compatible with its outcome kinds.

## Invariants

- No writable skill path exists in any runtime mode; spawn fails if the
  effective view cannot be built.
- The model cannot publish; publication is a human confirmation outside the
  model loop.
- Skill content never grants; secrets never appear in skill files, env,
  argv, logs, audit or tool results.
- Upstream Hermes is not patched: everything is achieved through rendered
  config, mounts and hub tools.
- Platform > org > user precedence is decided by the hub; collisions are
  loud, never silent.
- Diagnostic output allowlists safe fields; scan/audit records carry rule
  ids and locations, never skill bodies or secret values.
