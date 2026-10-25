# CHG-0144: Recipe-driven OAuth installation

Chain: ADR-0070 -> SPEC-0067 -> this record. Preserve existing dirty work;
continue on the current branch without committing or publishing it.

Live installation exposed Broker's dynamic-state prohibition. ADR-0071 and
SPEC-0068 add one bounded owner-only JSON state file through the existing
registration API; retain all enrollment, grant and lease checks. Verify the
module's own Linux `just check` in addition to the root pipeline.

## Goal

Install Calendar and Workspace MCP through the normal chat/source pipeline.
Provider, client selectors, scopes, token JSON and probes are reviewed recipe
data; runtime recipes do not require a Hub rebuild.

## Slices and red tests

1. `api/recipe.go`, `recipe_resolver.go`, `definition.go`: bounded OAuth contract
   from pinned metadata or explicit install recipe. Tests: survives resolution
   and credential digest; unsafe URLs/paths/templates fail; non-catalog accepted.
2. `prepared_oauth.go`, `contract_synthesis.go`, `broker.go`: generic extraction,
   JSON tokens, canonical state with lease-local filename views. Tests: different
   issuer/layout, missing scope, replay, foreign owner, traversal/symlink,
   checkpoint only after quiescence. Reuse existing callback fixtures.
3. Migrate Calendar and add pinned Workspace read-only recipe; replace Google
   form dispatch with declared JSON fields. Verify chat install/status and reads.
4. Run focused red/green tests, `just check`, dev rebuild/live acceptance and
   Docker cleanup. Record unavailable gates honestly.

## Boundaries and acceptance

No dependencies, upstream patches, secret scripts/argv/results, provider writes,
auth/TLS bypass, publication or cross-user state. Unknown auth fails explicitly.
Both source URLs and a non-catalog recipe use one pipeline. New leases retain
sessions. Tests alone never establish live provider success.
