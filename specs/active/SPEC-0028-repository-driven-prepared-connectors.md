---
status: active
title: Repository-driven prepared connectors
---
# Repository-driven prepared connectors

Extend SPEC-0026 through one generic lifecycle for the exact repositories
`makenotion/notion-mcp-server`, `nspady/google-calendar-mcp`,
`github/github-mcp-server` and `zereight/gitlab-mcp`.

1. Direct URL resolution pins the default branch to an exact commit before
   language, layout, entrypoint and credential discovery. Registries are optional.
2. Prepared reviewed overlays are data bound to repository, subfolder and commit.
   Each records license, Broker contract, egress, state, runbook and user handoff.
   New prepared entries must not require provider branches in the resolver.
3. Generated restricted Node/Go recipes remain independent of upstream Dockerfile
   build directives. Literal metadata can disambiguate entrypoints; duplicate bin
   aliases to one file are one entrypoint. Actual isolated initialize/tools/list
   must confirm the built artifact before binding.
4. Discovery is read-only, bounded to five candidates, preserves provenance, and
   uses expiring owner/context/runtime/policy-bound selection IDs. Selection uses
   the same source review, Broker, binding, projection and reconnect path.
5. Credentials and state never travel through chat. File delivery stays read-only;
   writable Broker state is a lease-owned directory, checkpointed only after its
   writer is stopped. Another owner cannot reuse its credentials, grants or state.
6. Reinstall reuses a compatible active owner connection. Explicit rotation uses
   a Broker replacement request, stops the previous workload and replaces its
   credential revision. Ambiguous active owner connections fail closed.
7. Confirmed non-secret HTTPS API URLs may extend egress; token strings, URL
   userinfo, fragments and query strings cannot grant egress.
8. Acceptance requires real Notion root/page reads, Calendar calendar/event lists,
   GitHub identity/repository reads and GitLab whoami after credential update.
   Mock tests and source-only checks cannot satisfy these live gates.
9. Each connector must prove owner-scoped projection and Hermes MCP reconnect while
   preserving the running user's session. Provider mutations remain separate
   explicit actions. Full Google Workspace is outside the Calendar entry.
10. A reviewed exact-source OAuth adapter may produce a provider authorization
    URL after a missing-token read probe. The authorization request is bound to
    owner, context, onboarding and a one-time state with PKCE. The loopback
    callback exchanges the code server-side, checkpoints only the owner's Broker
    state, then resumes confirmation and enablement. Status can reissue a lost
    link. Existing token files cannot be replaced implicitly. Unreviewed tool
    output cannot supply authorization URLs or token-file formats.
