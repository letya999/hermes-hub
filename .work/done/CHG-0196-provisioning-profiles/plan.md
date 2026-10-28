# CHG-0196 — M24 #364 S9: provisioning profiles

Issue: #364. Spec: SPEC-0083 R9.*. ADR: ADR-0084 D-series (skill refs).

## Goal

Turn the fleet `*-default.yaml` templates into real *provisioning profiles* a
platform operator or organization assigns: runtime template, tool wishes,
default grants document, persona layer, standard skill pins and extra layout.
Profile updates propagate safely via render-time layers plus a 3-way
`profile.lock` for stamped files.

## Layout

```
policies/profiles/<name>/          platform profiles (hub root, beside spaces/)
<orgDir>/profiles/<name>/          org profiles — override platform same-name
  profile.yaml   {description, extends, version}
  agent.yaml     agent template (today: policies/agent-default.yaml)
  workspace.yaml wishes template (today: policies/workspace-default.yaml)
  grants.yaml    {defaults: [CapabilityRule...], default_groups: [CapabilityGroup...]}
  SOUL.md        render-time persona layer (never stamped into the user file)
  skills.yaml    {skills: [{name, revision?, source?, pinned?}]}
  layout.yaml    {dirs: [...]} extra workspace dirs
```

## Slices

- **S9.1 profile resolution** (`internal/stack/profiles.go` new): `resolveProfile(spaceDir, orgDir, name)`
  walks the `extends` chain leaf→root; at each level org copy wins over
  platform copy. Per-file overlay: first occurrence of each file wins.
  Platform `default` falls back to legacy `policies/agent-default.yaml` /
  `policies/workspace-default.yaml`, then embedded — full backward
  compatibility with today's `policyTemplate` behavior.
  Names must match `idPattern` (traversal rejected by construction);
  `extends` cycles and depth >8 rejected.
- **S9.2 org schema** (`internal/stack/scope.go`): `Members` becomes
  `map[string]OrgMember{Role, Profile}` with UnmarshalYAML accepting the
  legacy scalar (`alice: member` → `{role: member}`); Marshal emits scalar
  when Profile is empty so org-init output is byte-identical. New
  `default_profile` field; `ApplyOrganization` resolves the member's
  provisioning profile (`member.Profile || o.DefaultProfile`) into
  `s.OrganizationProfile`.
- **S9.3 init + lock** (`internal/stack/space.go`, `cmd/hubctl/main.go`):
  `initScopeEnvironment` gains a `profile` parameter; resolves the profile,
  stamps agent.yaml/workspace.yaml from it, writes `profile.lock`
  (`{profile, version, files: {agent.yaml: sha256, workspace.yaml: sha256}}`)
  and layout dirs. `hubctl init --profile` flag; org spaces resolve the
  member profile when `--profile` is absent.
- **S9.4 `hubctl org sync-members`** (`cmd/hubctl/main.go`): reads the org
  settings, lists members, inits `spaces/<member>` with the resolved profile
  for each missing space; `--dry-run` reports first. Existing spaces are
  skipped (init never overwrites) → idempotent.
- **S9.5 layered effective SOUL** (`internal/stack/materialize.go`):
  `MaterializeSoulEffective` composes, in spec order: `ChannelContract` →
  org SOUL (`<orgDir>/SOUL.md`, skipped when absent/stub) → profile SOUL
  (resolved profile dir, skipped when absent) → user SOUL (healed space
  file) → tier-0 skill catalog. This moves the contract ahead of the
  persona — R9.4's frozen order — and fixes the org-SOUL gap.
  Profile at render resolves org-assigned first, else `profile.lock`,
  else `default`.
- **S9.6 grants materialization** (`cmd/hubctl/profile.go`): `profile apply`
  emits the profile's `grants.yaml` merged into a `CapabilityPolicy` JSON
  document (defaults + default_groups) at `generated/profile-policy.json`;
  the operator confirms through the *existing* `capability --kind policy
  --file … --confirm` path. Nothing is published by profile commands.
  Ceiling containment stays enforced by `CapabilityPolicy.Validate`
  (defaults ⊆ ceiling → ErrUnauthorized) — proven by a store-level test.
- **S9.7 skills.yaml → effective view** (`internal/stack/materialize.go`):
  profile `skills` entries mark matching effective-index skills `pinned`;
  pinned entries order first in the index (and therefore in the tier-0
  block). `name[@revision]` refs with source `platform|org|hermes|user`;
  a pin naming a skill that is absent or ineligible after readiness
  stamping is a render-time error (fail closed, R9.6).
- **S9.8 `hubctl profile apply|diff`** (`cmd/hubctl/profile.go`): 3-way
  update via `profile.lock`: for each stamped file compare lock hash →
  current profile bytes → on-disk bytes. Untouched files update (lock
  hash == disk hash), customized files kept and reported. `diff` prints
  the plan without writing. `apply` also refreshes layout dirs and the
  lock; it does not touch SOUL.md (render layer), secrets, or agent state.
- **S9.9 docs**: `docs/spaces.md` (profile layout + resolution), `docs/
  operations.md` (sync-members, profile apply/diff workflow), `SETUP.md`
  if it references the template flow; `docs/rbac.md` cross-link for the
  grants.yaml → policy-defaults flow.

## Tests (red first)

- `TestResolveProfileExtendsChainAndOrgOverride` — chain order, org wins
  per level, missing file falls to parent, legacy `policies/*-default.yaml`
  still honored, embedded fallback.
- `TestMembersLegacyScalarAndMapForms` — both YAML shapes parse; invalid
  role/profile rejected; scalar marshal preserved.
- `TestInitUsesOrgDefaultProfile` / `TestMemberProfileOverridesDefault` —
  init stamps analyst workspace.yaml + lock; member profile wins over org
  default and `--profile`.
- `TestEffectiveSoulLayerOrder` — contract→org→profile→user→catalog.
- `TestSyncMembersIdempotent` — N spaces created, second run creates none.
- `TestProfileApplyKeepsCustomizedFiles` / `TestProfileApplyUpdatesUntouched`.
- `TestProfileGrantsIssuedByOperatorOnly` — generated policy doc has no
  confirmation; `PutCapabilityPolicy` rejects defaults exceeding ceiling.
- `TestPinnedSkillsOrderFirst` / `TestPinIneligibleSkillFailsRender`.
- Red-team: profile name `../x` rejected; layout `..`/absolute/denied
  segments rejected; user `profile:` key in settings.yaml is ignored for
  resolution (org/lock own the assignment).

## Explicit non-goals

- No web UI for profiles.
- No auto-publication of grants — the confirmed capability path is the only
  mutation route (R9.5).
- No model/hermes-issued profile changes; profile resolution is host-side
  file config only.
- `profile apply` never edits the user SOUL.md, secrets, or store data.
- Capability *profile* (SPEC-0040/0041) is a different concept — docs name
  this "provisioning profile" explicitly.

## Verification

`just check` (coverage ≥85%); no dependency changes expected.
Docs-check `last_verified` bumps on touched docs. Live stack acceptance
belongs to #368.
