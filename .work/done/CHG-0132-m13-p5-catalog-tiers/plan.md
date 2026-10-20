# CHG-0132 — M13 P5: catalog tiers T0/T1/T2, CI-built images, install budgets

Issue: https://github.com/letya999/hermes-hub/issues/286
Branch: feat/m14-toolhub-connector-platform
Depends on: CHG-0128 (recipe ledger), CHG-0130 (connector surface), CHG-0131 (labels/finalizers)

## Scope (contract-testable slices)

### S5.1 Tier + image pin on prepared catalog entries
- `PreparedEntry.Tier` (`t0` default for reviewed catalog, `t1` imported,
  `t2` arbitrary source) + `Image {Digest, Provenance, SBOM}` — strict
  validation: T0 requires a `sha256:` digest; provenance/SBOM are URI fields.
- `PreparedEntry.Apply` carries the image digest into `DefinitionSource` so a
  T0 definition is born pinned.
- Tests: catalog parse round-trip with tier/image; missing digest on T0
  rejected; unknown fields still fail closed.

### S5.2 T0 admission performs zero builds
- Wherever the artifact builder runs for a definition whose source carries a
  pinned image digest, admission must resolve the image reference and skip
  the builder entirely (builder mock asserts never called).
- Tests: `TestT0AdmissionSkipsBuilder` — prepared T0 install with a builder
  spy returns the pinned image without invoking build.

### S5.3 hubctl connector import — T1 refs
- `hubctl connector import <ref>` parses `mcp-registry:`, `docker:mcp/`,
  `toolhive:`, `mcpb:`, `hermes:`, `aqua:` refs into a T1 recipe draft +
  ledger entry; prints the review digest; writes only with `--confirm`
  (capability-style gate).
- Tests: golden digest per source type from #282 fixtures; write refused
  without `--confirm`.

### S5.4 Toolsets and default projection
- Recipes/catalog entries carry `toolsets` + `default_enabled` /
  `default_excluded`; install projects the default read set only.
- Tests: `github` install projects read toolset; excluded set absent until
  explicit enable.

### S5.5 Prefetch + warm-start budget notes
- Prefetch seam on catalog publish/hub start honouring the 8 GB cache cap;
  benchmark notes on fixture providers recorded in state evidence.
- Tests: prefetch respects cache cap and never prunes foreign images.

### S5.6 Docs + closeout
- `docs/prepared-connectors.md` tiers section; ADR "CI-built connector
  images" appended; issue #285-adjacent update on #286.

## Out of scope (live gates)
- Actual GHCR publish/SLSA provenance generation — CI-side, owner push.
- Real 15 s/30 s budgets against live providers — reported, not gated.

## Done checklist
- [ ] Tests red then green per slice; `just check` green.
- [ ] ADR appended; prepared-connectors.md tiers section.
- [ ] Live acceptance deferred honestly to owner deploy.
