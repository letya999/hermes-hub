# SPEC-0084: Agent skills platform (M24)

Frozen: 2026-10-27. Implements GitHub milestone 25 "M24 — Agent Skills
Platform", issues #356–#368 (M24.1–M24.13). Deferred 2026-10-28: #366
(M24.11 self-evolution, R11) is out of the MVP scope — removed from the
GitHub milestone; R11/A11 stay here for the follow-up milestone. Decision:
[ADR-0085](../../docs/adr/ADR-0085-agent-skills-platform.md). Supersedes the
SPEC-0041 CP-07 "Skills" row and the skills clause of SPEC-0042 (notes
appended to both). Issue bodies name this record SPEC-0082/ADR-0083; M26
took those numbers first — this is the same document under free numbers.

## Requirements

### R1 — decision record (M24.1, CHG-0200, #356)

- R1.1: ADR-0085 and SPEC-0084 exist with index rows; `devcheck docs` passes.
- R1.2: Every ADR decision D1–D13 maps to ≥1 requirement here and ≥1 M24 issue.
- R1.3: The pin-verification spike results are recorded with commands and
  observed output (no secrets, no personal data).

### R2 — unified skill store and effective view (M24.2, #357)

- R2.1: `spaces/<user>/skills/`, `<org>/skills/`, `config/skills/` are the only
  skill sources; layout `<name>/SKILL.md` + `references/ templates/ scripts/
  assets/ evals/`; `.drafts/`, `.revisions/`, `.archive/`, `.state/` per owner.
- R2.2: Strict frontmatter parse for drafts; lenient for imports (warn on
  name/dir mismatch, skip missing description/unparsable YAML); compat fields
  per D2 normalize into one Manifest.
- R2.3: Revision hash over portable paths + content + size + exec bit;
  identical save is a no-op; stale base revision is a conflict.
- R2.4: Per-skill `flock` + atomic directory swap (temp sibling + rename).
- R2.5: Effective view `generated/skills-effective/` + `index.json` (name,
  source, revision, description, readiness inputs); precedence per D5;
  reserved `hermes-hub-*` names locked; collisions rejected at write or hide
  the lower skill with an owner notice.
- R2.6: Effective view mounts read-only at `/skills`; `/state/hermes/skills`
  is an empty ro mount in both modes; `/opt/hub/skills` and
  `skills.external_dirs` are removed; supervisor fails the spawn when the
  view cannot be built.
- R2.7: Init creates `skills/`; org init creates `<org>/skills/`; migration
  moves user-authored `hermes/skills/*` to `skills/` (published), curator-/
  agent-created to drafts, drops bundled copies and stale hub copies, moves
  `<org>/hermes/skills` to `<org>/skills`, imports old `skills/registry.json`.
- R2.8: `hubctl skill list/show/rollback/archive/restore` on the new store;
  backup/restore/purge include `skills/`.
- R2.9: Symlinks and path traversal in the store are rejected; principals
  cannot cross stores.

### R3 — native default-deny + hermes catalog source (M24.3, #358)

- R3.1: Every `native:*` `SeedInventory` section defaults to `RuleDeny`;
  governance migration preserves only explicitly selected sections.
- R3.2: The implicit unmanaged toolset base list is removed; toolsets render
  only from `tools:` wishes that pass governance.
- R3.3: `skills` is always in `agent.disabled_toolsets`;
  `curator.enabled: false`, `skills: {}` and `.no-bundled-skills` render in
  both modes.
- R3.4: Upstream bundled skills are exported into the image as catalog
  source `hermes` (read-only, versioned with the pin), delivered only via
  `skill-source:hermes/<path>` wishes + grants.
- R3.5: A missing `platform_toolsets` entry must not fall back to a
  composite that re-emits denied tools; composites cannot re-add a denied
  leaf; skill/prompt text cannot enable a toolset.

### R4 — security pipeline (M24.4, #359)

- R4.1: Deterministic scanner with (file, line, rule id, severity) findings:
  secrets, download-and-execute, exfil hints, destructive shell, privilege
  escalation, hidden unicode/bidi/zero-width, long base64, instruction
  hijack, hub-specific names (`HUB_TOOLHUB*`, broker paths, `org_actions`).
- R4.2: Stricter profile for `scripts/`: shebang/interpreter allowlist,
  network calls flagged, binaries rejected outside size-capped `assets/`.
- R4.3: `draft.json` carries author, session id, source refs, `tainted` +
  taint sources, scan report, base revision.
- R4.4: Publish gate: critical finding refuses with named file/line/rule;
  tainted or script-bearing drafts can never auto-publish; approver sees
  findings + provenance.
- R4.5: Quotas: max skills per owner, 256 files / 1 MiB per file / 8 MiB per
  bundle, SKILL.md size, drafts per day.
- R4.6: Audit events for draft/publish/reject/rollback/archive and scan
  verdicts contain no skill content; secret-looking text is redacted on
  import with a visible warning.

### R5 — skill tools on ToolHub (M24.5, #360)

- R5.1: Five tools — `skill`, `skills_search`, `skills_list`, `skill_view`,
  `skill_manage` — identical in managed and unmanaged mode, on ToolHub with
  per-call admission and audit; principal comes from the authenticated
  envelope only.
- R5.2: Sections `hub:skills` (read) and `hub:skills_manage` (mutate) are
  separately grantable; `skill.name` input is an enum of eligible names.
- R5.3: `skill_manage` actions `create/patch/edit/write_file/remove_file/
  delete/archive/restore/propose_to_org` validate per-action fields
  (foreign fields rejected), require `base_revision` on patch/edit, and
  always produce drafts — never a live change.
- R5.4: Draft approval cards (summary, diff, findings, provenance) with
  Publish/Reject via the existing protected confirmation link; undo
  restores the previous revision; replay/cross-principal use fails.
- R5.5: `propose_to_org` creates an org draft publishable only by org
  admin/owner; `hubctl skill drafts|approve|reject|rollback|archive|
  restore|promote` mirror the flow.

### R6 — progressive disclosure and discovery (M24.6, #361)

- R6.1: Readiness filters the catalog: platform fit, required
  toolsets/tools, connector and credential dependencies, grants;
  `disable-model-invocation` / `allow_implicit_invocation:false` hides a
  skill from implicit catalog (explicit invocation still works).
- R6.2: Tier-0 `<available_skills>` block in `soul-effective` with
  name/source/description + spec behavioral instruction; inline when
  ≤ ~20 skills / ~3.5k tokens, else pinned-first + names + search hint;
  omitted entirely when no eligible skills.
- R6.3: Session-pinned catalog revision; mid-session changes arrive as one
  note; prefix is byte-stable within a session.
- R6.4: Lexical search (name/description 2×, tags, exact-name first,
  bounded body prefix, `coverage` reported, RU+EN tokenization).
- R6.5: `/name args` and `$name` (≤5, stop at first non-skill) resolve
  harness-side with `$ARGUMENTS`/`$0..$n` substitution; unknown/hidden
  references produce a visible error; activation dedupes per session;
  content is wrapped in `<skill_content>` with the re-load instruction.
- R6.6: Catalog descriptions cannot inject markup (escaped, length-capped).

### R7 — resources and script execution (M24.7, #362)

- R7.1: `skill_view` returns bounded text from `references/`/`templates/`/
  `examples/`; resource listing in `skill()` output; one-level references
  enforced by lint.
- R7.2: The effective `/skills` mounts read-only into CLI/code cells;
  scripts run from a scratch copy, never in the ro tree.
- R7.3: `skills:` read-only logical root for hub file/document/image
  executors; copying to workspace is explicit.
- R7.4: Dependency declarations (PEP 723, `uv run`/`npx` pins,
  `metadata.deps` `pip:`/`npm:`/`system:`) are validated against an
  allowlist; injection-shaped entries are dropped; resolution uses only
  operator-allowed registries/toolbox layers with actionable errors.
- R7.5: Scripts inherit only the caller's grants; without an execution
  grant a skill is "instructions only" and `skill()` reports readiness;
  `native:terminal` remains the documented relaxed path, off by default.
- R7.6: Nothing executes at load: no `!command`, no install-on-load,
  no hooks.

### R8 — secrets and settings via the credential broker (M24.8, #363)

- R8.1: Requirements normalize to `requires.credentials[]` /
  `requires.connectors[]` from Hermes/openclaw/Codex fields; a skill cannot
  declare hub/broker secret names.
- R8.2: `skill()` surfaces "setup needed" with a protected form link;
  values store as broker skill-secrets bound to (principal, skill) or as
  references to existing connector credentials.
- R8.3: Declared names lease into the env of the cell running that skill's
  script, for that call only — never into Hermes env, tool results, argv,
  logs or audit; a declared broker delivery method follows the broker's
  declarative-contract + negative-test rule.
- R8.4: Non-secret `metadata.hermes.config` settings store per user in
  `skills/.state/settings` and surface in `skill()` output.
- R8.5: Missing required credential renders "setup needed" or hides the
  skill when configured as a hard dependency.

### R9 — provisioning profiles (M24.9, #364)

- R9.1: Profiles live at `policies/profiles/<name>/` and `<org>/profiles/
  <name>/`: `profile.yaml` (description, extends, version), `agent.yaml`,
  `workspace.yaml`, `grants.yaml`, `SOUL.md`, `skills.yaml`, `layout.yaml`;
  `extends` chains resolve with org override; today's `*-default.yaml`
  become profile `default`.
- R9.2: `OrganizationSettings.default_profile`; members accept legacy
  `alice: member` or `alice: {role, profile}`.
- R9.3: `hubctl init [--profile]` stamps the profile, creates `skills/`,
  writes `profile.lock` (file hashes, version); `hubctl org sync-members`
  creates member spaces idempotently (dry-run first, never overwrites).
- R9.4: Effective SOUL layers: platform channel contract → org SOUL →
  profile SOUL → user SOUL (+ skill catalog).
- R9.5: `grants.yaml` materializes through the existing confirmed operator
  grant path only — never model-issued; it cannot exceed the org ceiling.
- R9.6: `skills.yaml` pins standard skills by reference (`name[@revision]`,
  source platform/org/hermes); pinned skills order first; referencing an
  ineligible skill is a render-time error.
- R9.7: `hubctl profile apply|diff` does 3-way updates via `profile.lock`:
  untouched files update, customized files are kept and reported.

### R10 — git installs (M24.10, #365)

- R10.1: Sources: `github.com/<owner>/<repo>[/tree/<ref>/<path>]`,
  `gitlab.com/<g>/<p>[/-/tree/<ref>/<path>]`, configured self-hosted GitLab;
  HTTPS only — no ssh/git/file schemes, local paths, userinfo, query
  strings or cross-host redirects; default branch resolves to a SHA.
- R10.2: Hub-side bare mirror `skills/.sources/<id>.git` per owner with
  `origin`; `fetch --depth=1 --filter=blob:none origin <sha>`; export via
  tree walk/archive — no working tree, no `.git` in the skill dir.
- R10.3: Git runs only inside an isolated cell: egress allowlisted to the
  git host, ro root, no hub state, `core.hooksPath=/dev/null`,
  `protocol.file.allow=never`, no submodules/LFS, no checkout.
- R10.4: Private access uses broker-leased tokens via `GIT_ASKPASS`/
  credential-helper env — never in URL, `.git/config`, argv, logs, origin
  record or disk outside the broker.
- R10.5: `SKILL.md` discovery under `path` (depth ≤6, ≤2000 dirs, skip
  `.git`/`node_modules`, incl. `.agents/skills`, `skills/`, `.claude/skills`
  conventions); multi-skill repos return candidates — no silent pick.
- R10.6: Install/update produce scanned drafts with `.origin.json`
  (source id, origin URL, ref, commit, path, tree hash, fetched_at, scan
  verdict) excluded from the agent view; `update` fetches the tracked ref,
  diffs, flags force-push; `check_updates` reports without importing.
- R10.7: `hubctl skill sources list|remove`; removal deletes the mirror and
  drops update tracking with a warning.

### R11 — self-evolving skills (M24.11, #366) — DEFERRED post-MVP

- R11.1: Usage events (activation, resource reads, run outcome, corrections)
  are recorded by ToolHub — unforgeable, no public write endpoint;
  per-skill counters: uses, helped, failed, last_used.
- R11.2: Foreground learning contract in the prompt: search first, prefer
  patch over create, "lessons not logs".
- R11.3: Background review triggers after ~10 model iterations since last
  review and 30s idle; excerpt ≤64 events / 32 KiB with large results
  replaced by omission notices; minimal prompt (skill tools only); low
  reasoning, ≤2k output tokens, ≤2 min; at most one change; must
  `skill_view` before patching; produces a draft.
- R11.4: Evidence classes: correction of method, non-obvious working
  method, wrong/missing/outdated step in a used skill; excluded: one-offs,
  personal facts, secrets, guesses, transient failures, generic advice.
- R11.5: Reflector→curator split with delta patches; no full rewrites of
  mature skills.
- R11.6: Taint propagates from web/inbound/connector/git sources; tainted
  drafts never auto-publish; poisoned-experience drafts show source refs.
- R11.7: `/learn [sources]` foreground turn; large sources become
  knowledge-base skills (lean SKILL.md + `references/` per topic).
- R11.8: Daily curation: stale after 30 days unused, archive after 90
  (pinned exempt); duplicates → merge draft (`absorbed_into`);
  description-evolution suggestions from trigger misses as drafts.
- R11.9: Learning mode per profile: `off` / `drafts` (default) /
  `drafts+auto-own-text` (auto-publish only small text patches to own
  learned skills — never scripts, tainted or org skills); daily review
  budget; platform/org skills are never mutated.

### R12 — quality: lint, evals, telemetry (M24.12, #367)

- R12.1: Lint on every draft: name = dir + charset; description imperative
  ("Use when …"), ≤1024 (advise ~250), no "This skill" prefix; body ≤500
  lines; references one level + existing; no incident-log shape; ≤60 files;
  advisory except spec violations on drafts.
- R12.2: `evals/triggers.json` (~20 queries, 60/40 train/validation split,
  3 runs, threshold 0.5) and `evals/evals.json` (prompt, expected_output,
  files, assertions; with/without runs; evidence-graded).
- R12.3: `hubctl skill eval` runs isolated real-model comparisons in a
  temporary space; activation detected via `skill` tool calls in the run
  receipt; results under ignored `.work/tmp/`.
- R12.4: Platform skills pass trigger evals before release; results are CHG
  evidence; model-dependent legs are reported honestly when unavailable.
- R12.5: Usage stats per skill (`hubctl skill stats`, `skills_list`
  metadata): uses, helped/failed, last_used, trigger misses, cost — no
  content exposure.
- R12.6: `hermes-hub-system-guide`, `hermes-hub-mcp-onboarding`,
  `deep-research-embedded` pass lint + trigger evals; deep-research bounds
  are enforced in code, not skill prose.

## Acceptance

| ID | Requirement set | Proof |
|---|---|---|
| A2 | R2.* | Store/view unit + concurrency tests; mount assertions in rendered compose/supervisor output; migration dry-run + apply on a copied real layout (names only); supervisor spawn refusal on broken view; ≥85% coverage on `internal/skillstore`. |
| A3 | R3.* | Render tests: fresh space emits zero ungranted native toolsets; grant/revoke round-trip; `skill_manage`/`skills_list`/`skill_view` absent in the live capabilities probe (devcheck); no `.bundled_manifest` in new spaces. |
| A4 | R4.* | Scanner fixtures (curl\|sh, hidden unicode, instruction hijack, literal secret) at expected severity; redaction-on-import warning; taint/scripts publish gates; quota errors; audit without content. |
| A5 | R5.* | Tool contract tests (enum rejection, foreign-field rejection, stale base, draft-not-live, cross-principal 404); approval flow through the protected link incl. org admin rule; identical managed/unmanaged behavior. |
| A6 | R6.* | Budget tests (10 inline / 200 bounded); ineligible hidden + explicit error; `/name args` + `$ARGUMENTS`; session prefix byte-stable across publish; search coverage. |
| A7 | R7.* | Bounded `skill_view`; scratch-copy execution; deps-allowlist rejection; no-exec-grant readiness; documents tool reads `assets/`; `/skills` stays unwritable. |
| A8 | R8.* | Canary test: secret never in env/results/logs/audit; lease scoped to (principal, skill, call); setup-needed flow; revocation; settings injection. |
| A9 | R9.* | Profile init/sync-members/apply/diff on fixtures; layered SOUL order; org-ceiling rejection; legacy members unchanged. |
| A10 | R10.* | URL validation rejects non-HTTPS/userinfo/redirects; pinned-commit install + origin record; multi-skill selection; canary token absent from disk/config/logs; diff draft + force-push flag; no `.git`/`.origin.json` in agent view. |
| A11 | R11.* | DEFERRED with #366 — post-MVP. Trigger/idle rules; one-change + view-before-patch; evidence/exclusion classes; tainted-draft path; stale/archive/merge; auto mode boundary; client cannot write usage events. |
| A12 | R12.* | Lint rules red→green; trigger split stability; grader requires evidence; stats shape; hub skills pass lint. |
| A13 | all | Live acceptance per #368: org + 2 members + personal space, managed and unmanaged, recorded in `state.yaml` with redacted receipts; `just check` green; docs re-verified; all M24 issues closed with evidence; CHGs moved to `.work/done/`. |

## Non-goals

- Graph-of-skills retrieval and embeddings; hosted eval dashboards;
  pushing skill changes upstream (PR/MR from local edits); a web UI for
  profiles; RL-trained curators; making the credential broker a runner or
  identity provider; business services.
