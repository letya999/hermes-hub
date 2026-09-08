# SPEC-0003: Go-first project tooling

Frozen: 2026-09-08. Supersedes SPEC-0002 items 9 and 10; all other SPEC-0002
requirements remain in force.

1. `just check` is the single local CI entrypoint on Windows and Linux.
2. It runs Go race tests, integration tests, formatting/static checks, documentation
   checks and a >=85% statement coverage gate across all original Go code.
3. Project-owned CI, coverage, packaging, Docker smoke checks and container runtime
   supervision are Go. Upstream Hermes and connectors keep their native runtimes.
4. Root npm/Jest and project-owned Python/TypeScript helpers are absent. The isolated,
   pinned Playwright MCP npm package remains an upstream runtime dependency.
5. GitHub Actions invokes Just and smoke-tests both Docker targets. Docker and live
   provider success are claimed only when those checks actually run.
