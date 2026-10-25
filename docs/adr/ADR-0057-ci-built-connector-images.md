---
description: Catalog tiers T0/T1/T2 with CI-built digest-pinned connector images, deterministic ref imports under operator confirmation, staged toolset projection, and a bounded prefetch plan.
last_verified: 2026-10-23
---
# ADR-0057: Catalog tiers and CI-built connector images

Status: Accepted 2026-10-19. Implements the contract half of GitHub issue
#286 (M13 P5). Builds on [ADR-0053](ADR-0053-recipe-source-ledger.md)
(recipe/source ledger) and [ADR-0056](ADR-0056-ownership-finalizers.md)
(ownership labels).

## Context

Every install — including reviewed catalog entries — ran source review plus a
restricted BuildKit build at first use. Nothing pinned an image, nothing
pre-pulled, and a whole tool surface projected at once (43 tools for
github-mcp-server). The fix is to make the common case instant and to grade
sources by how much review already happened.

## Decision

- **D1 — Tiers on catalog entries.** `PreparedEntry` carries `tier`
  (`t0`/`t1`/`t2`, unknown values rejected at parse) and an optional
  `image` pin `{ref, digest, provenance, sbom}`. The pin's digest becomes the
  definition's admitted `Source.Digest`; `ref` splits at `@` into the bare
  repository image because definitions keep image and digest separate.
- **D2 — T0 admission never builds.** When `PreparedForSource` matches an
  entry with an image pin, `DefaultSourceReviewerWithCatalogs` skips the
  repository fetch and the restricted builder entirely and admits via the
  published-artifact path (`ImportPublishedArtifact` + preflight probe). The
  builder is behind the `restrictedBuild` seam so tests assert zero calls.
- **D3 — `hubctl connector import <ref>`** parses `mcp-registry:`,
  `docker:mcp/`, `toolhive:`, `mcpb:`, `hermes:` and `aqua:` refs into a T1
  `RecipeResolution` draft whose ledger records exactly what the ref
  asserted. Without `--confirm` the command prints the review digest and
  refuses to write, matching the `hubctl capability` gate; `--confirm` stores
  the content-addressed recipe. Parsing is pure — identical refs produce
  identical digests.
- **D4 — Staged toolset projection.** Definitions carry `toolsets`,
  `default_enabled` and `default_excluded`. Install projects the default set
  only; `connector_enable <target> toolsets=[...]` records the extra sets on
  the binding (`EnabledToolsets`) and the projection revision advances.
  A `default_excluded` tool stays hidden until an explicitly enabled toolset
  contains it. Definitions without toolset metadata project everything, so
  existing records are unaffected.
- **D5 — Bounded prefetch.** `PrefetchPlan` is a pure function over wanted
  digest-pinned targets and the local cache: it pulls only what is missing,
  evicts only `hermes-hub.*`-labelled (owned) images, in caller LRU order,
  and skips a target rather than breaching the 8 GiB budget or touching a
  foreign image.
- **D6 — Prefetch executor.** `hubctl connector prefetch` runs the plan
  against the daemon: wanted targets are the embedded catalog's digest-pinned
  `image` entries, sizes come from `manifest inspect`, and the cached set is
  `docker images --digests`. An image is an eviction candidate only when its
  repository equals one the catalog pins — a strictly narrower owned set than
  the `hermes-hub.*` label family, so foreign images can never match. The
  command is the operator step after a catalog release; entrypoints and
  schedulers call the same path. Amended 2026-10-19.

- **D7 — CI publication pipeline (`.github/workflows/connector-images.yml`).**
  A dispatch-only job runs `devcheck connector-build <id>`, which compiles the
  embedded entry through the same restricted generator local admission uses
  (verified source pin, generated recipe, rootless BuildKit, provenance
  `mode=max` plus the pinned syft SBOM scanner), exports the attested OCI
  archive and writes a `pin.json` of locally verified digests. `skopeo copy
  --all` pushes archive plus referrer attestations to
  `ghcr.io/<owner>/hermes-hub-connectors/<id>:src-<commit>`. `devcheck
  connector-pin` then hashes the pushed manifest bytes (`skopeo inspect
  --raw`), refuses on any mismatch with the archive digest, and promotes the
  catalog entry to `t0` with the full `{ref, digest, provenance, sbom}` pin.
  The patched `catalog.json` ships as a job artifact for the owner to commit;
  the workflow never commits on its own. Amended 2026-10-23.

- **D8 — Lifecycle benchmark gate.** `devcheck connector-bench RUNS`
  (integration-only) times the real Docker lifecycle of a fixture MCP stdio
  workload under the production profile — `--network none`, read-only
  rootfs, tmpfs `/tmp`, `--cap-drop ALL`, `no-new-privileges`, user
  `10001:10001`: create, start + `initialize`/`tools/list` handshake, first
  `tools/call`, and removal. Install is defined as create + handshake, the
  same work a T0/T1 install performs once the image is local. The verdict
  fails on budget regression: install p95 > 15 s, first call p95 > 2 s,
  remove p95 > 5 s. A `bench` gate in `container-gates` runs it on every
  image build; the JSON report on stdout is the issue evidence. Amended
  2026-10-23.

## Consequences

- Contract tests cover: tier/pin round-trip, unknown-tier rejection, T0
  admission with a builder spy (zero calls), all six import schemes plus
  malformed refs, deterministic digests, confirm-gated writes, toolset
  projection and the prefetch budget.
- The pipeline is honest about trust: `connector-pin` pins the locally
  verified archive digest, not a registry-reported digest, and refuses when
  they disagree; a foreign `ref` argument never reaches the catalog.
- Live gates remain open until the first dispatch run: the GHCR push, the
  `catalog.patch` commit, nightly probes, and the p95 budget measurements run
  on the owner side; this change does not claim them.
