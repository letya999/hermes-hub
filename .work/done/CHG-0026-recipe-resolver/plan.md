# Recipe Resolver

## Scope

- add a secret-free resolver that turns a pinned GitHub revision into launch and connection recipe metadata;
- parse only reviewed repository declarations and never execute repository commands/configuration;
- preserve the existing M5.3 prepare → credentials → confirm → enable flow;
- retain the source SHA and subfolder in onboarding metadata;
- add regression tests for OCI correlation, Docker/Compose safety, credentials and user isolation.

## Verification

- `go test ./internal/toolhub`
- `just check`
- `just security` when dependency files change
- `just docker-check` when Docker is available
