# SPEC-0004: Organization/user scope overlay

Frozen: 2026-09-10.

1. A resolved runtime is selected as `authenticated principal -> user space ->
   organization membership -> effective settings`; this repository's ingress is
   responsible for authenticating the principal and choosing the user space.
2. An organization may allow features and define approved MCP servers. Each such
   server must be listed in `read_only_mcp` and expose a non-empty `tools.include`
   allowlist. A member's settings may only narrow that policy with `disabled_mcp`;
   they cannot define user MCP servers in organization scope.
3. User OAuth credentials, Hermes state, browser state, sessions and memories remain
   in the user's runtime volumes. Organization secrets are loaded from a separate
   host-owned env file and are not copied into the user space.
4. Organization documents are mounted at `/org` read-only and exposed only through
   bounded file operations. They cannot grant instructions or write access.
5. Hub-owned mutations use explicit organization action names. Generic upstream MCP
   servers remain responsible for their own tool-level authorization.
6. `tenant`, `organization` and `user` are not agent-controlled tool parameters; they
   are resolved by the control plane and runtime selection.
7. Standalone user spaces remain supported without an organization overlay.
