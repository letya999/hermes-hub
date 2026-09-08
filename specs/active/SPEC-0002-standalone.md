# SPEC-0002: Standalone general-purpose Hermes hub

Frozen: 2026-09-08. Version 0.2.0. Replaces SPEC-0001.

1. The project operates without CareerGo, JobFetch or any embedded business service.
2. User namespace is spaces/<user>, with settings.yaml, SOUL.md and independent
   secrets.dev.env/secrets.prod.env. Init never overwrites an existing space.
3. Dev/prod selects Docker target, generated config and env file. Named Docker volumes
   isolate each user's runtime memories, skills, hooks, sessions, workspace and OAuth.
4. Prod is an unprivileged read-only runtime with no source mounts or Go compiler.
   Dev adds Go, a disposable writable layer and an allowlist of source mounts.
   Neither target mounts the project root, spaces or the Docker socket.
5. Native Hermes skills, hooks, plugins, memory and session search are enabled/available;
   arbitrary owner-configured stdio/HTTP MCP servers are supported.
6. Connection presets cover Telegram bot/personal account, Google Workspace, HH,
   Slack, GitHub, Atlassian, browser/internet, Meet, transcription and native desktop.
   Missing OAuth/login is reported; configuration validation does not claim live access.
7. Personal Telegram reads by default. External mutations follow the owner's task.
   HH applications require applicant credentials, a resume ID and explicit authorization;
   uncertain send outcomes are not automatically retried.
8. File tools enforce root boundaries, immutable archive access, revision preconditions,
   cross-process locking and bounded text reads/search.
9. npm run ci uses Jest to run native Go/Python tests and CLI integration checks.
   Coverage gate >=85% applies independently to all own Go statements, Python runtime
   statements and the JavaScript coverage-check helper. No upstream code inflates coverage.
10. GitHub Actions checks code, builds both Docker targets and smoke-tests runtime.
    A manual trusted-main workflow supports a dedicated self-hosted Runner.
11. Original project source is AGPL-3.0-only. Upstream licenses remain intact. Archive
    includes sources, setup, release tooling, checksums and platform CLI binaries.
12. Memory Bank separates rules, current docs, immutable ADRs, frozen specs and work plans.
