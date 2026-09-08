# SPEC-0001: Private Hermes hub

Frozen: 2026-09-07. Version: 0.1.0.

1. Init creates a private person deployment without overwriting existing secrets/config.
2. Render validates known features, dependencies and endpoints, then emits Hermes and
   Compose configuration. Existing SOUL content is retained.
3. CLI supports build/up/down/logs/chat, Telegram personal login and Meet login.
4. Enabled MCP connectors use actual upstream contracts and segregated Python environments.
5. Personal Telegram defaults to a server-enforced read-only tool allowlist; bot access
   requires numeric owner IDs. Optional sending remains bound to owner instructions.
6. File tools constrain access to two roots, refuse archive writes, require hash revisions
   for updates and serialize participating writers. Text is UTF-8 and at most 2 MiB.
7. HH apply requires applicant token, resume/vacancy IDs and authorized=true. It sends once,
   preserves a 201 receipt and never retries unknown outcomes automatically.
8. JobFetch reads the configured tenant's v1 jobs pages and rejects mismatching tenant IDs.
9. Native companion uses a fixed executable, bearer authentication and optional tool
   allowlist. Browser Origin requests are rejected. No arbitrary remote process launching.
10. Agent/career run without host Docker socket or public ingress; login surfaces bind to
    host loopback. Users' deployment directories, bot tokens and OAuth sessions are separate.
11. CI checks formatting, static analysis, tests, documentation and workflow syntax; a
    Docker job builds both images and checks a credentials-free runtime startup.
12. Release archives contain source, documentation and platform CLI binaries, never live
    deployment directories. Account-dependent integrations must be identified as unverified
    until exercised with real authorized accounts.
