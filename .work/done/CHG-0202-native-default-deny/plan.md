# CHG-0202: Native Hermes capabilities default-deny (M24.3)

Chain: `specs/active/SPEC-0084-agent-skills-platform.md` R3.* -> this plan ->
`state.yaml`. Issue: #358 (acceptance A3). Decision: ADR-0085 D7. The issue's
"ADR-0083/SPEC-0082" references resolve to ADR-0085/SPEC-0084 — M24 numbering
was re-based to free IDs after the m26 chain (see CHG-0200 research).

## Goal

Every `native:*` governance section defaults to deny; a toolset renders only
with a workspace wish plus an active ToolHub grant. The native `skills`
toolset and the curator stay off in both modes; upstream bundled-skill
seeding is suppressed by marker plus read-only mounts.

## Read before coding

1. SPEC-0084 R3.1–R3.3, issue #358 slices S3.1–S3.7
2. `internal/toolhub/api/governance_io.go` `SeedInventory`
3. `internal/stack/governance.go` `implicitToolsetSections`,
   `internal/stack/render.go` toolset assembly,
   `internal/stack/hermes_capability.go` `nativeCarveoutToolsets` +
   `disabledToolsets`, `internal/stack/defaults/workspace.yaml`

## Slices

- S3.1 done: every `native(...)` seed section defaults `RuleDeny`; migration
  (`denyDefaultSelections`) preserves only explicitly selected sections.
- S3.2 done: implicit unmanaged base list removed (`implicitToolsetSections`
  returns hub:tools + guest browser only); render emits only wished+granted
  toolsets; explicit denied wishes fail the read.
- S3.3 done: `skills` removed from `nativeCarveoutToolsets` (not grantable,
  always in `agent.disabled_toolsets`); unmanaged render skips it in the
  toolset loop and pins `curator.enabled: false`; `hermes/.no-bundled-skills`
  written at every render.
- S3.4 deferred: image-side export of upstream bundled skills as catalog
  source `hermes` requires a `docker/Dockerfile` change — excluded this
  session by owner instruction (no Docker delivery). `source_hermes.go`
  catalog reader lands together with S3.5 grant vocabulary.
- S3.5 deferred with S3.4 (no source to grant yet).
- S3.6 done (pre-existing): `hubctl governance --kind grant --section
  native:<name>` covers the vocabulary; `hubctl capability --kind tools`
  prints the compiled plan incl. natives and the reviewed carve-out list.
- S3.7 done: `defaults/workspace.yaml` no longer selects memory/todo/web/
  deep_research natively — commented wishes document the wish+grant rule.

## Mandatory red-team checks

- Platform missing from `platform_toolsets` cannot re-emit denied tools —
  the inventory universe supplies every upstream platform with `[]`
  (fail-closed), denied leaves land in `agent.disabled_toolsets`.
- Composites (`debugging`, `safe`, `search`) skipped from the per-leaf
  denylist (`platformDenylistComposite`) so granted surfaces stay working;
  an ungranted composite cannot reintroduce a denied leaf because its own
  `native:<composite>` section is deny-by-default too.
- Prompt-level "enable terminal" cannot open a toolset: admission is fixed
  at render (`agent.disabled_toolsets` + `platform_toolsets`), the runtime
  config is read-only to the agent.

## Evidence

- `internal/toolhub/api/governance_io.go` — all native seeds `RuleDeny`.
- `internal/stack/render.go` — empty implicit list; `skills` skipped;
  `curator.enabled:false` in unmanaged cfg too.
- `internal/stack/governance.go` — `implicitToolsetSections` = hub:tools
  (+guest browser) only.
- `internal/stack/hermes_capability.go` — `skills` off carve-out list.
- `internal/stack/render_env.go` — `.no-bundled-skills` marker.
- Test updates: wishes that previously relied on allow-by-default now carry
  explicit grants or rules; `TestNativeSkillsToolsetAlwaysDenied`,
  `TestSeedInventoryIntegrity` deny assertions, governance ordering updated.
