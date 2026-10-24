# CHG-0136: Documentation restructure and enforcement

Owner-approved information-architecture rebuild. Not a milestone chain — no ADR/SPEC;
this is repository hygiene work governed directly by AGENTS.md.

## Goal

One `AGENTS.md`, one `docs/` tree, docs ≤500 lines, no number collisions,
`last_verified` enforced as "verified at or after last edit", `.work` triaged,
per-service and per-concern docs written from code truth.

## Slices

### S1 Mechanics

- Renumber `ADR-0033-deterministic-work-isolation` → ADR-0051; `SPEC-0040-media`
  → SPEC-0061; `SPEC-0047-toolhub-cli-transport` → SPEC-0062;
  `SPEC-0048-cli-policy-completion` → SPEC-0063. Fix all references.
- `docs/adr/index.md`: add missing ADR-0012 row, fix ordering.
- `.work` triage: completed dirs → `done/` with index rows; genuinely active stay;
  abandoned marked abandoned; unindexed in-progress dirs indexed; index grouped
  by milestone; live colliding in-progress pairs renumbered (younger → next free).

### S2 Gate

`internal/devcheck` docs check extended: unique ADR/SPEC/CHG numbers, index↔file
sync both directions, `last_verified` ≥ file's last commit date, ≤500 lines for
top-level docs/*.md with a shrinking allowlist for legacy files.

### S3 Content (each doc written against code, verified date honest)

- New: `principles.md`, `engineering.md`, `testing.md`, `boundaries.md`,
  `rbac.md`, `tools.md`, `spaces.md`, `agent-home.md`,
  `services/{hubctl,runtime,toolhub,communication,supervisor,credential-broker,media,cell-proxy,model-relay}.md`,
  `channels/{telegram,slack}.md`.
- Split: operations (1463), integrations (932), prepared-connectors (657),
  validation (655), architecture (638).
- Dissolve `m10-stabilization.md` into services/routines/testing docs.
- Merge `e2e-acceptance.md` into testing.md; migration-map gates → operations.md.
- Evidence docs → `.work/done/<chg>/`: toolhive-vmcp-audit, capability-boundary-audit,
  ai-stp-compatibility, ci-latency.
- Broker tree folded in: its ADRs → new main-sequence numbers; specs → specs/active
  (baseline.json regenerated); .work → shared; rules → AGENTS.md broker section +
  engineering.md. Russian language kept per its convention.
- threat-model refresh for default-deny / `?t=` links / event streams.
- Regenerate `docs/index.md`.

### S4 Conventions

AGENTS.md doc map + broker paragraph; milestone-playbook numbering and verified-bump
rules; CONTRIBUTING/SETUP pointers.

## Out of scope

- `.repo-safety/` stays as-is (owner decision).
- Broker docs stay Russian; no translation.
- No code behaviour changes; Go edits limited to devcheck docs gate.

## Done checklist

- [ ] `go run ./cmd/devcheck docs` green incl. new checks.
- [ ] `just check` green.
- [ ] docs/index.md regenerated; all docs ≤500 or in shrinking allowlist.
- [ ] .work/index.md grouped, complete, honest statuses.
