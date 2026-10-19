# Complete recipe catalog discovery

## Scope

- keep the existing secret-free Launch Recipe and Connection Recipe contracts;
- add official Docker Hub namespace/tag discovery and GitHub Packages/ GHCR discovery;
- enable the complete fixed catalog set with `HUB_RECIPE_CATALOGS=all`;
- normalize Smithery JSON-schema field names into Credential Broker-safe names while preserving JSON targets;
- verify Docker Hub, GHCR, Smithery metadata extraction and OCI SPDX/provenance correlation without storing secret values.

## Verification

- `go test ./internal/toolhub`
- `go test ./...`
- `just check`
- `just security`
- `just docker-check` when Docker is available
- real public Docker Hub catalog/image acceptance and real ToolHive `initialize`/`tools/list`.
