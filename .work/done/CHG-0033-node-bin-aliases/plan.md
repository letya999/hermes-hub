---
description: "Node bin aliases to one file resolve as a single MCP entrypoint."
last_verified: "2026-09-21"
---

# CHG-0033 Node bin alias entrypoint

## Root cause

`GenerateArtifactRecipe` counted every `package.json` `bin` map entry as a
separate entrypoint candidate. Repositories that publish several command names
for the same file (for example `mcp-gitlab` and `zereight-mcp-gitlab`, both
pointing at `build/index.js`) produced two identical argv candidates and failed
closed with `ambiguous MCP entrypoint`, although only one server exists.

## Minimal scope

- In `internal/toolhub/artifact_language.go`, deduplicate `bin` map targets by
  resolved context path before counting candidates. Aliases to one file yield
  one automatic entrypoint; distinct targets remain ambiguous and still require
  an explicit literal argv.
- No change to Python/Go/Rust selection, the pnpm workspace rule, operator
  `ArtifactImportConfig.Entrypoint` override, or any admission boundary.

## Regression boundaries

- Positive: two `bin` names (`./build/index.js` and `build/index.js`) produce
  `["node", "/app/build/index.js"]`.
- Negative: two `bin` names with different target files remain fail-closed.

## Documentation and specification

- `docs/integrations.md` Node recipe paragraph records the alias rule.
- No ADR and no spec change: the fail-closed ambiguity contract is unchanged,
  only the candidate identity is corrected from map entries to resolved files.

## Verification

- `go test ./internal/toolhub -run TestGenerateArtifactRecipe`
- `just check`

## Rollback

Revert the deduplication block and its tests/docs line.
