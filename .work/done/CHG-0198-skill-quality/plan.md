# CHG-0198 — M24 #367 S12: quality — lint, evals, telemetry (R12)

Issue #367, SPEC-0084 R12, ADR-0085 addendum. (#366/R11 deferred post-MVP —
removed from milestone 25 on 2026-10-28; usage-stats surface ships the
counter file format now, populated by `use`/`view` recording only.)

## Slices

- S12.1 `skillstore/scan/lint.go`: `Lint(dir string, m Manifest-lite,
  inputs)` advisory findings (R12.1): `name.charset`, `name.mismatch`,
  `desc.prefix` (This skill), `desc.imperative`, `desc.length` >1024,
  `desc.advise` >250, `body.lines` >500, `shape.incident_log`,
  `tree.files` >60. Wired into `scanTree(name, tree)` — Info/Low never
  block; one-level refs + name/dir already enforced hard (lintRefs,
  ParseSkillMD). Call sites: CreateDraft, publish validate.
- S12.2 usage telemetry surface (R12.5): `.state/usage/<skill>.json`
  `{stats:{uses,helped,failed,missed,last_used},events[256]}`;
  `RecordUsage`/`Usage`; agenttools records `use` on skillInvoke and
  `view` on skillView (best-effort); `skills_list` exposes
  `stats:{uses,helped,failed,missed,last_used}` per skill — no content.
  `hubctl skill stats [--name]`. Outcome kinds (helped/failed/missed/
  corrected) accepted in the file format; recording them is R11's job.
- S12.3 `skillstore/evals.go`: loaders+validators for `evals/triggers.json`
  `{queries:[{q,expect}]}` ≥10 queries, deterministic 60/40 train/valid
  split (hash by q), `threshold` (0,1], `runs` ≥1; `evals/evals.json`
  `{evals:[{id,prompt,expected_output?,files?,assertions:[]}]}` —
  assertions typed (contains|matches|calls_tool), grader requires an
  `evidence` field naming what was observed (R12.2).
- S12.4 `hubctl skill eval --name [--dir]`: builds the run plan from
  triggers.json/evals.json, executes through an injectable runner seam
  (`SkillEvalRunner`; production = managed model relay
  `HUB_MANAGED_MODEL_URL/chat/completions`, activation = `skill` tool call
  in the run receipt); writes results to `<space>/.work/tmp/skill-eval-<ts>.json`.
  No model → honest "runner unavailable" (R12.3/R12.4).
- S12.5 `evals/triggers.json` for the three hub skills
  (hermes-hub-system-guide, hermes-hub-mcp-onboarding,
  deep-research-embedded) ~20 queries each + lint-clean descriptions
  (imperative form). deep-research bounds already enforced in code
  (`WebResearchBounds` ceilings — R12.6); test pins them.
- S12.6 contract: catalog `hub-agent-tools@5.5.0` (skills_list gains usage
  stats), prompt_contract.go, docs/tools.md + docs/routines.md.
- S12.7 docs (spaces.md `.state/usage` + evals layout, operations.md eval
  runbook), ADR-0085 addendum, e2e registry row, index/state, `just check`,
  commit `feat(skills): M24.12 quality — lint, evals, telemetry (#367)`.

## Out of scope

Live model runs (runner seam is mocked; real legs = #368); embedding/graph
retrieval; hosted dashboards; #366's learning pipeline (deferred); outcome
event recording beyond use/view.
