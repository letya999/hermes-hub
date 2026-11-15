---
description: Close the capability-profile digest-split wedge — bind publish to the bound catalog revision, gate uncovered selections at the write choke point, surface capability-rule denials in tool_search, and warn in hubctl doctor.
---

# CHG-0302: capability digest-split — bind, gate, surface, detect

## Incident

`telegram-837846104` (2026-11-12 ~21:39 UTC, chat at 00:39 local): the owner
asked for a media_generate → media_inspect → media_convert → media_edit
pipeline. The model answered "no media tools exist" — only `media_speak`
projected. Root cause was a durable catalog-generation wedge in both managed
profiles (`artem_main-default`, `artem_two-default`): at 21:19–21:20 UTC the
publish path (`DerivePublishedRightsAndSelections`, `issued_by: platform`)
repinned `profile.Selections` to `hub-agent-tools@18.3.19`
(`sha256:56feefe4…`) while `profile.Allows`/`policy.Ceiling` stayed on the
18.3.18 digest (`sha256:a18387ff…`). `CapabilityRuleDecision` requires
implementation-digest equality between an allow∩ceiling pair and the
selection, so every handler tool with `Uses` silently dropped out of
projection. Remote tools without `Uses` (`media_speak`, `tool_search`,
`connector_*`, `memory_*`) survived — exactly the ~15 tools the model saw.

The wedge persisted across restarts (journal replay restores records as
written), no runtime transition left it, and nothing surfaced it:
`tool_search` reported the suppressed selections as absent, so the model
concluded the tools did not exist.

## Implicit state machine found

Catalog generation of a managed profile is an implicit FSM encoded across
`profile.Selections[].implementation_digest`, `profile.Allows[]`,
`policy.Ceiling[]`, `bindings[].definition_version`, and — as a hidden input —
the compiled `agenttools.HubToolsDefinition()` version. Two writers owned
different halves of the invariant: `EnableCatalog` moves
selections+allows+ceiling+binding atomically; publish moved selections
alone, stamping the binary's digest. The resulting `digest-split` state is a
wedged absorbing state with no in-runtime egress and no detector.

## Changes

- `internal/migration/rights.go`: `selectedHubTools` now resolves the
  first-party definition through the profile's active `hub-agent-tools`
  binding (`selectedHubDefinition`, max projection revision, deterministic
  tie-break) — the same rule connector selections already follow. A rebuilt
  binary can no longer smuggle a catalog upgrade into durable state;
  `catalog-enable` remains the only generation transition. Bootstrap without
  a binding keeps the previous compiled-definition behavior.
- `internal/toolhub/api/capability_policy.go`: `scanCapabilityRules` factored
  out of `CapabilityRuleDecision` (deny predicate parameterized);
  `CapabilityCoverage` answers whether a selection's uses are backed by an
  active allow∩ceiling pair, mirroring projection's path binding exactly.
- `internal/toolhub/capability_store.go`: `validateProfileAdmissionLocked`
  gained the repin join — a new write of an active profile must not
  introduce or repin a selection whose uses are governed at another
  implementation digest (`CapabilityRepinConflict` in api). Plain uncovered
  selections stay writable: revocation and never-authorized intent are
  legitimate suppressed states the denial surface and doctor report — only
  the repin-that-abandoned-its-rules signature is a write bug. Selections
  carried byte-identical from the stored record re-affirm existing
  authority. Journal replay (`putCapabilityProfile(_, true)` in
  `storeFromSnapshot`) skips the check so stored historical splits still
  load; disabled profiles skip it too — disabling a wedged profile is the
  operator's unwedge path. `EnableCatalog`, `PutCapabilityProfile` and
  `PreviewCapabilityProfile` run the strict check.
- `internal/toolhub/rights.go` + `tools_list.go`: `CapabilityDenials` lists
  selections that resolve binding+definition yet fail `CapabilityDecision`;
  `tool_search`/`tools_list` reports them under `unavailable` with
  `reason=capability_rules` (distinct from `missing_right` and from a
  genuinely undeclared name), plus a top-level `reason` on exact lookups.
- `internal/toolhub/store_queries.go`: `SelectionGaps` — read-only store
  query reporting, per active profile, the selections no rule admits.
- `cmd/hubctl/toolhub_doctor.go` + `main.go`: doctor warns per split profile
  (count + `hubctl catalog-enable` repair hint, localized key
  `doctor.capability-selection-gap` in all four catalogs); missing store =
  unmanaged space, not a failure.
- `internal/stack/prompt_contract.go` (`contractMid`): a selection the
  capability rules deny reports `reason=capability_rules` — an operator
  repair state to describe honestly, not a grant to work around.
- `docs/tools.md`: apply pins selections to the bound revision;
  catalog-enable is the only upgrade and derives rules in the same write;
  a publish leaving selections uncovered is refused; `unavailable` reasons.

## Regression tests

- `internal/toolhub/capability_coverage_test.go`: repinned/uncovered
  selection write refused; deny-shrunk write still admitted; replay+disable
  tolerate a stored split incl. save→load; `SelectionGaps` reports;
  `CapabilityDenials` names the rule-denied selection; `toolsList` emits
  `unavailable`/`reason=capability_rules` for list and exact lookup.
- `internal/migration/rights_test.go`: `TestSelectedHubToolsPinsBoundDefinition`
  — publish on a newer compiled catalog still stamps the bound revision.
- `cmd/hubctl/toolhub_doctor_test.go`: split profile warned via a JSON
  store; unmanaged dir and consistent store stay silent.
- `cmd/hubctl/apply_recovery_test.go`: fixture policy now carries
  catalog-enable-shaped ceiling/defaults (enabled catalogs always cover
  their cataloged uses).
- `TestCapabilityHistoryRecoversPolicyAndIssuerTogether` and
  `TestToolsetCoreLimitAndExtendedInvokeRevoke` keep passing — carried-over
  selections re-affirm under a narrowed policy.

## Operator repair (separate step, not part of this changeset)

Both managed profiles need `hubctl catalog-enable --version 18.3.19
--extend-ceiling` (policy `policy-dc98f27198e06535` for artem_two,
`policy-a159d14f33d95855` for artem_main), then managed respawn and a real
media pipeline rerun. Upstream provider 403 remains an independent risk.
