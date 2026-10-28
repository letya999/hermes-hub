# CHG-0193: M24 #361 S6 — progressive disclosure and discovery

Chain: `docs/adr/ADR-0084-agent-skills-platform.md` D9 -> `specs/active/SPEC-0083-agent-skills-platform.md` R6
-> this plan -> `state.yaml`.
Previous stage: `CHG-0192`. Next stage: `CHG-0194` (resources/scripts).

## Goal

The eligible catalog is disclosed progressively: a budgeted tier-0
`<available_skills>` block in soul-effective, tier-1 `skill` activation,
tier-2 `skill_view` resources. Readiness and invocation flags filter the
catalog; explicit `/name args` and inline `$name` resolve harness-side with
argument substitution. Catalog text can never inject markup; the session
prefix stays byte-stable, mid-session changes surface as one note.

## Read before coding

1. SPEC-0083 R6.*, ADR-0084 D9, `.work/done/CHG-0192-skill-tools/plan.md`.
2. `internal/skillstore` (index/Build), `internal/stack/materialize.go`
   (soul render order), `internal/agenttools/skills.go`,
   `internal/communication/gateway.go` dispatchChannelCommand default.

## Preconditions and rules

- Red test first, then fix. No Docker this session.
- Eligibility is stamped at build time in MaterializeEffectiveSkills where
  Settings knows granted tools/features — never evaluated from skill text
  at call time.

## Slices

### S6.1 Index metadata + eligibility (R6.1, R6.6)

Anchors: `internal/skillstore/skillstore.go` Entry/ParseManifest/Build;
`internal/stack/materialize.go` MaterializeEffectiveSkills.

Change:
1. Entry gains `Tags`, `Requires` (tools/toolsets/connectors/credentials),
   `Platforms`, `Hidden` (disable-model-invocation or
   allow_implicit_invocation:false), `Eligible`, `Missing`. Compat:
   `required_environment_variables`/`requires.env` fold into
   `requires.credentials`.
2. `skillstore.EvaluateReadiness(idx, caps)` marks eligible/missing;
   MaterializeEffectiveSkills derives caps from Settings (tools config,
   native carveouts, features, configured connectors) and stamps the index.
3. agenttools list/search/tier-0 filter `hidden || !eligible`; `skill` on a
   hidden-but-eligible name works; on ineligible names returns an explicit
   "not eligible: <missing>" error. `skill` output carries `readiness`.

Tests (red first):
- `TestIndexParsesReadinessMetadata`: frontmatter fields land in Entry.
- `TestEligibleCatalogHidesUnmetAndHidden`: list/search hide, invoke errors.
- `TestHiddenSkillStillInvokes`: disable-model-invocation -> explicit ok.

### S6.2 Tier-0 catalog block (R6.2, R6.3, R6.6)

Anchors: `internal/stack/materialize.go` MaterializeSoulEffective;
`internal/agenttools/skills.go` effectiveIndex.

Change:
1. `SkillCatalogBlock(idx) string`: eligible+visible entries, XML-escaped +
   ~200-char descriptions; inline rows <=20 skills and <=14000 chars, else
   pinned-first names + search hint; empty -> omitted. Appended to
   soul-effective after ChannelContract; embeds `revision` (index hash).
2. agenttools `catalogRevision()` compares the session-pinned revision seen
   on first read with the current index; one `note` on first divergence.

Tests (red first):
- `TestSoulEffectiveEmbedsSkillCatalog`: block, escaped descs, omission.
- `TestCatalogBlockFallsBackToNames`: >20 skills -> names+hint.
- `TestCatalogRevisionNoteFiresOnce`: publish mid-session -> single note.
- `TestCatalogByteStableAcrossPublish`: same index -> identical block.

### S6.3 Lexical search (R6.4)

Anchors: `internal/agenttools/skills.go` skillsSearch.

Change: tokenize unicode letters/digits (RU+EN); score exact-name first,
then name/description token hits weighted 2x over tag hits; `coverage`
reports matched count; hits bounded.

Tests:
- `TestSearchScoresNameOverTags`, `TestSearchUnicodeTokens`.

### S6.4 Harness references `/name` + `$name` (R6.5)

Anchors: `internal/communication/gateway.go` dispatchChannelCommand default
+ enqueueChannelJob text path; `internal/stack/compose.go` gateway volumes.

Change:
1. Communication mounts the effective view ro at `/skills-effective` +
   `HUB_SKILLS_EFFECTIVE` env (toolhub convention).
2. `internal/communication/skill_refs.go`: unknown `/name` resolves against
   the user's index -> enqueues a job whose text is
   `<skill_content name=...>` + body with `$ARGUMENTS`/`$0..$n` substituted
   + re-load instruction; unknown name -> visible error, hidden -> allowed,
   ineligible -> explicit error.
3. Plain text `$name` refs (<=5, stop at first non-skill) expand the same
   way pre-enqueue; activation dedupes per task.

Tests:
- `TestSlashSkillEnqueuesSkillContent`, `TestSlashSkillUnknownIsVisibleError`.
- `TestDollarRefsExpandWithArgs`, `TestDollarRefsStopAtNonSkill`,
  `TestSkillActivationDedupesPerTask`.

### S6.5 Docs/state (R6.*)

`internal/systemguide/SKILL.md`, `internal/stack/prompt_contract.go`,
`docs/tools.md` contract note; state.yaml; CHG -> done on `just check`.

## Red-team checklist

- A skill named like a known command (`goal`, `help`) never shadows the
  real command — skill resolution only runs for unknown commands.
- Catalog description with `<script>`/closing-tag bytes is escaped; a
  4 KiB description is capped, never breaks the block.
- `$name` inside a pasted code block still expands — documented as
  harness-level behavior; `$$name` escapes stay literal.
- An ineligible skill invoked explicitly reports missing deps, never the
  draft path.

## Acceptance

A6: budget tests (inline vs names fallback); ineligible hidden + explicit
error; `/name args` + `$ARGUMENTS`; session prefix byte-stable across
publish; search coverage field.
