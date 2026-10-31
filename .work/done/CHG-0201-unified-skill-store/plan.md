# CHG-0201: Unified skill store and effective view (M24.2)

Chain: `specs/active/SPEC-0084-agent-skills-platform.md` R2.* -> this plan ->
`state.yaml`. Issue: #357 (acceptance A2). Decision: ADR-0085 D4–D6, D13.

## Goal

`spaces/<user>/skills/`, `<org>/skills/` and `config/skills/` become the only
skill sources. The hub builds one read-only effective view per space at
`generated/skills-effective/` and mounts it at `/skills`; `/state/hermes/skills`
is an empty read-only mount in both modes. Drafts, revisions, archive and
migration live in `internal/skillstore`; `internal/skills` is retired.

## Read before coding

1. SPEC-0084 R2.1–R2.9, issue #357 slices S2.1–S2.9
2. `internal/skillstore/*` (store landed and green under `-race`)
3. Anchors: `internal/stack/materialize.go`, `render.go`, `compose.go`,
   `scope.go`, `space.go`, `internal/supervisor/ensure.go`,
   `internal/migration/migration.go`, `cmd/hubctl/context.go`

## Slices (probity red-first; one assertion per write)

### S2.1–S2.5 Store core — done this change

`internal/skillstore`: layout + strict/lenient parse + revision hash +
flock/atomic swap + drafts + archive/restore + quotas + index + effective
view builder + `MigrateLegacySpace`. Package green `-count=2 -race`.

### S2.6 Effective view wiring (R2.5, R2.6)

Anchors: `internal/stack/materialize.go`, `render.go:135-154`,
`compose.go:184-219`, `internal/supervisor/ensure.go:461-479`.

- `MaterializeEffectiveSkills(dir, s)` builds `generated/skills-effective/`
  from sources platform (`generated/skills` staging incl. system-guide) >
  org (`<org>/skills`) > user (`<space>/skills`).
- Compose/supervisor mount it ro at `/skills` in both modes;
  `/state/hermes/skills` empty ro tmpfs in unmanaged too; `/opt/hub/skills`
  mount and `skills.external_dirs` removed.
- Supervisor fails spawn when the effective build fails (no log-and-continue).

### S2.7 Init + org + migration (R2.7)

Anchors: `render_env.go` (skills dir already created), `scope.go`
`InitOrganization` + `ApplyOrganization`, `internal/migration`.

- Org init creates `<org>/skills`; `OrganizationSkillsDir` repoints to it.
- `MigrateLegacySpace` wired into the space migration path (dry-run first).

### S2.8 Platform skill rename

`config/skills/mcp-connector-onboarding` -> `hermes-hub-mcp-onboarding`
(dir matches frontmatter `name:`); update `docs/agent-home.md`,
`config/SOUL.md` references.

### S2.9 `hubctl skill` on the new store (R2.8)

`cmd/hubctl/context.go runSkill`: `list/show/rollback/archive/restore`
against `internal/skillstore`; the hardcoded `telegram-1` envelope and
`internal/skills` import go away. `internal/skills` retires once callers
are migrated (its ScanSkillText consumers move or stay — decided by callers;
`internal/communication` test usage keeps the package alive only if still
imported elsewhere).

## Red-team checklist

- Rendered unmanaged + managed output: no writable skill path anywhere.
- `<org>/hermes/skills` legacy path cannot shadow `<org>/skills`.
- hubctl never accepts a scope outside the caller's own space dir.

## Acceptance

- A2 (SPEC-0084): mount assertions in rendered compose/supervisor output;
  supervisor spawn refusal on broken view; migration dry-run + apply on a
  copied real layout (names only); `internal/skillstore` coverage >= 85%.

## Done checklist

- [x] `internal/skillstore` package: all red->green slices, `-race` clean
- [x] Effective view wired into compose + supervisor (both modes)
- [x] Init/org/migration updated
- [x] Platform skill renamed + refs updated
- [x] `hubctl skill` rewritten on the store; `internal/skills` retired
- [ ] `just check` green; coverage >= 85% (skillstore 85.2%; repo gate pending)
- [ ] Issue #357 commented with evidence and closed; folder to `.work/done/`
