---
description: Use Just and Go for project-owned CI, packaging and runtime supervision.
last_verified: 2026-09-08
---
# ADR-0003: Go-first project tooling

Accepted 2026-09-08. Supersedes ADR-0002 only for its CI-orchestrator decision.

## Decision

Use `just` as the single local CI entrypoint. Keep coverage checks, documentation
validation, packaging, Docker smoke checks and runtime supervision in Go. Retain
Python and Node only inside upstream runtime dependencies. The isolated Playwright
MCP package lock remains because that upstream server is distributed through npm.

## Consequences

Windows and Linux contributors run the same recipes without a root Node or Python
toolchain. Container builds still install pinned upstream Python and Node packages.

## Related records

SPEC-0003, ADR-0002, docs/architecture.md, docs/validation.md.
