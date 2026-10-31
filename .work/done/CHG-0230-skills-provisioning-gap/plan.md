# CHG-0230: Skills platform — capability provisioning + governance lifecycle gaps

Chain: [ADR-0085](../../../docs/adr/ADR-0085-agent-skills-platform.md) ->
[SPEC-0084](../../../specs/active/SPEC-0084-agent-skills-platform.md) -> этот plan -> state.yaml.

Live-audit findings of 2026-10-29: the skill tool surface was implemented and
cataloged (hub-agent-tools 5.8.0), but no managed runtime could ever project
it — three independent provisioning/lifecycle breaks, each sufficient alone.

## Goal

Сделать управляемую поверхность скиллов реально достижимой и устранить
классы багов «новая секция/профиль не доезжает до живого стейта».

## Scope

1. **`toolhubFamilies` принимает skills.** `skills`/`skills_manage` were
   mapped by `toolhubCapabilityID` but absent from the wish vocabulary —
   `tools: {skills: toolhub}` failed validation
   ("not a ToolHub capability family"), so the manifest `projected` list
   could never carry the families. Both names added; the default
   workspace.yaml template now wishes them.
2. **Governance inventory reconcile.** `LoadGovernance` merges the shipped
   `SeedInventory` into a persisted document: sections added after the seed
   are appended and inventoried defaults track the shipped posture —
   a doc seeded before M24 (or before the SPEC-0083 R3.1 deny flip) can no
   longer keep `hub:skills*` permanently "unknown" → deny, nor keep stale
   `native:*` allow floors. Extra unshipped sections stay; rules untouched.
3. **`governanceDocPath` is infra-deterministic.** A secondary space
   (`infra: false`) reaches the infra sibling's document first — never the
   alphabetically-first sibling's leftover doc; its own stale doc is last
   resort. The infra space always resolves its own dir (seed target), no
   sibling scan. `spaceRendersInfra` peeks only at the `infra:` yaml key —
   a full Read would recurse into governance resolution.
4. **Ops unblocking (evidence, not code).** Recorded user-scope migration
   allows for artem_main/artem_two/local deny-default sections emitted
   under stale docs; `skills`/`skills_manage` wishes on both managed
   spaces; catalog 5.8.0 registered + bound; policies republished
   (artem_main rev13, artem_two rev15) with skills ceiling rules; profiles
   republished (artem_main rev18, artem_two rev17) with the 5 skill
   selections — preview admits all five with real limits; dev-rebuilt
   toolhub + supervisor + per-space runtime images.

## Verification

Red-first tests: `TestGovernanceSkillsSections` (wishes resolve + default
template ships them), `TestGovernanceInventoryReconcile` (missing sections
appended, stale defaults synced, extras kept), `TestGovernanceDocPathInfraSibling`
(infra doc wins, own-doc last resort, infra seed target, no-infra fallback).
Plus live-store evidence under `.work/tmp/` (candidate records, preview
admissions, active binding list).
