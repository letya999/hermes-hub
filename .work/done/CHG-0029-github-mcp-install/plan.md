---
description: "Исправить generic GitHub MCP source review без ослабления ToolHub admission."
last_verified: "2026-09-19"
---

# CHG-0029 GitHub MCP install

## Root cause

`prepare_source` pins the public GitHub repository, downloads a bounded verified
recipe context, then calls `RecipeResolver.Resolve`. `parseDockerfile` currently
rejects every `RUN --mount`, so the official GitHub MCP Dockerfile fails before
catalog selection or the existing restricted generated-build fallback can run.

The rejection is broader than the execution boundary requires. The resolver
never executes the upstream Dockerfile, and `ImportGitHubArtifact` builds only
the project-generated `.hub/Dockerfile`. A BuildKit cache mount therefore needs
no runtime/build permission. A secret mount still describes undeclared secret
input and must remain rejected.

## Minimal scope

- In `internal/toolhub/recipe_resolver.go`, classify Dockerfile mount directives
  narrowly: ignore/normalize cache-only `RUN --mount=type=cache` metadata and
  reject secret mounts plus every existing privileged, insecure, host-network
  and Docker-socket directive.
- Do not rewrite or execute repository commands and do not copy an upstream
  Dockerfile into the generated build recipe.
- Keep the change generic; do not add a GitHub provider branch or guess a GitHub
  API, image, command, OAuth flow or credential value.
- Keep the existing official opt-in connector path intact: feature `github`
  renders `https://api.githubcopilot.com/mcp/` with `GITHUB_TOKEN`. Source
  self-install remains a separate explicitly granted ToolHub path.

## Regression boundaries

- Add table-driven resolver tests proving cache mounts no longer abort recipe
  resolution, including the whitespace/options forms actually used upstream.
- Prove `type=secret` remains fail-closed, including
  `oauth_client_id`/`oauth_client_secret`, mixed mounts and case/spacing variants.
- Retain regression coverage for privileged, security=insecure, host networking
  and Docker socket directives.
- Add one control/reviewer regression showing the source path reaches the safe
  generated fallback without executing the upstream Dockerfile; mocks establish
  control flow only, not live GitHub/ToolHive integration.

## Documentation and specification

- Amend `specs/active/SPEC-0026-mcp-recipe-resolver.md` to distinguish ignored
  cache metadata from rejected secret/privileged BuildKit directives.
- Update `docs/integrations.md` with the two opt-in GitHub paths and their
  separate credential/admission boundaries. Update `SETUP.md` only if operator
  instructions need clarification; preserve existing dirty-tree edits.
- No ADR is expected: the ToolHub authorization/admission decision is unchanged.

## Risks

- A substring check can misclassify comments, shell text or multiple mounts;
  parse only the Dockerfile instruction/options needed for the policy.
- Treating every non-secret mount as cache would accidentally admit bind/ssh/tmpfs
  semantics. Allow only `type=cache`; unknown and secret-bearing mounts fail closed.
- A passing resolver test is not evidence of a live provider login, ToolHive
  workload, GitHub API call or mutation authorization.

## Verification

- `go test ./internal/toolhub`
- `go test ./internal/toolhub -run 'RecipeResolver|PrepareSource'`
- `just check` (including race, static analysis, docs checks and >=85% own Go coverage)
- `just security` only if dependency files change
- `just docker-check` when Docker is available; report availability and never
  claim provider integration from fixture/mock results

## Rollback

Revert the narrow Dockerfile mount classification and its tests/docs. No stored
provider credential, catalog record, deployment or user binding is migrated by
this change.
