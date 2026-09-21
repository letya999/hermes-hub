---
status: active
title: MCP Recipe Resolver
---
# MCP Recipe Resolver

ToolHub may resolve a user-supplied GitHub repository after it is pinned to an
exact commit SHA and optional subfolder. The resolver is part of the existing
M5.3 `prepare_source` flow; it does not create another gateway, runtime or
deployment service.

The resolver returns a secret-free Launch Recipe and Connection Recipe. It may
read repository declarations and metadata, but never executes README commands,
client configuration, JavaScript `commandFunction`, Docker Compose, or an
upstream Dockerfile. It records file digests as evidence and returns `draft`
when a complete launch contract is not proven.

Catalog adapters are explicit `RecipeCatalog` implementations. Lookups run in
parallel and a result is admissible only when its repository matches the pinned
owner/name and its optional commit matches the pinned SHA. A name-only result is
ignored. No provider API is guessed by the core resolver.

The shipped opt-in adapters are the official MCP Registry, ToolHive catalog,
Docker MCP catalog, Smithery, Docker Hub and GHCR. `HUB_RECIPE_CATALOGS=all`
enables the complete fixed set. Docker Hub namespace/tag search uses the
official Hub API. GHCR package/tag search uses the official GitHub Packages
API when the operator supplies an optional `HUB_GHCR_TOKEN_ENV`; repository
image hints are also checked directly through the OCI distribution API.
A container candidate must prove an immutable manifest, a GitHub
source/repository label, the exact selected revision, and both OCI provenance
and SBOM evidence from referrers or embedded Docker attestation manifests.
The resolver prefers this published-image path, pulls by digest and runs the
existing isolated `initialize`/`tools/list` preflight; it falls back to the
existing restricted GitHub build when that published path is unavailable or
cannot pass review.

Docker and Compose declarations are plans only. Immutable images may be used
only with a repository-matched digest; Compose selects one MCP service and
records sidecars for review without starting them. Host mounts, privileged
mode, host namespaces, shell healthchecks and unsafe Docker build directives
are rejected.

Upstream Dockerfile inspection is metadata only. `RUN` option tokens are
parsed as instruction options after line continuations are folded; comments
and command text are never treated as directives. `--mount=type=cache` is
inert metadata and is ignored. Secret, bind, ssh, tmpfs, typeless or unknown
mounts, host networks, insecure sandboxing, privileged flags, unknown RUN
options and Docker socket references mark the resolution `review` without a
launch recipe, which forces the existing restricted generated `.hub` build
fallback. The upstream Dockerfile is never executed, admitted as the active
recipe or given secrets; `ArtifactRecipe.VerifyContext` remains the only
build-recipe admission boundary.

Inside the restricted generated build the upstream Dockerfile stays inert
metadata. Its last exec-form `ENTRYPOINT`/`CMD` may only disambiguate a Go
source tree with several `main` packages (basename match against the selected
main directory or the `go.mod` module basename) and supply the default server
arguments for the matched binary; any other shape fails closed or is ignored.
The isolated `initialize`/`tools/list` preflight may retry once with every
declared-but-optional secret injected as a placeholder: when the probe then
succeeds it has proven those credentials gate server startup and they become
required inputs for onboarding. Credentials never receive or store real values
inside the resolver or preflight.

The artifact-context content gate stays fail-closed with one narrow exception:
a token-shaped match whose secret body (after the fixed detector prefix,
ignoring separators) is a single repeated character is a placeholder fixture,
not a usable credential, and does not block the build. Any varied body still
fails closed, and PEM private-key headers are never exempt.

The resolver may inspect `.mcp.json`, `.env.example`, package/lock manifests,
release metadata and CI files as bounded, secret-free evidence. It never
executes client configuration, package scripts, shell commands, Compose or an
upstream Dockerfile, and the two client metadata files are excluded from the
BuildKit context.

Connection fields contain names, types and delivery targets only. Values are
handled by the existing protected form or Credential Broker and are not stored
in recipes, onboarding status, Hermes context, logs or audit records.
