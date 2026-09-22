---
description: Frozen acceptance boundary for the ToolHive local vMCP evaluation.
last_verified: 2026-09-11
---
# SPEC-0027: ToolHive local vMCP evaluation

Frozen: 2026-09-11.

1. Hermes keeps direct MCP connections until a fixed authenticated vMCP URL survives
   backend add, disable, removal, address change and restart without rebuilding Hermes.
2. Tool visibility is not authorization; list and call must resolve the authenticated
   runtime, current binding, owner, revision and limits through the same policy.
3. ToolHive and connector artifacts must be exact-version or digest pinned and optional.
4. Real probes must cover remote and stateful container MCP, conflicts, filtering,
   secrets, identity, recovery and `tools/list_changed`.
5. Performance evidence separates direct MCP, vMCP, workload proxies, connector
   containers and optional isolation helpers.
6. The 0.48.0 evaluation is a no-go; no user data or production configuration migrates.
7. Go ToolHub derives identity from an authenticated runtime binding, reauthorizes every
   call, bounds output and forwards cancellation. Models cannot supply owner IDs.
8. Workloads support user/organization isolated, shared stateless and per-job ephemeral
   modes. Stateful definitions cannot use shared mode.
9. ToolHive remains external and optional. Its stdio-to-HTTP, legacy SSE and remote
   transports do not change ToolHub authorization semantics.
10. OAuth 2.1 issuance is delegated to a real authorization server. ToolHub implements
    the protected-resource challenge/metadata boundary and must later validate issuer,
    audience, expiry and scopes before production deployment.
