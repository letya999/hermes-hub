# CHG-0004: Organization/user scope overlay

## Goal

Add a filesystem-backed organization overlay without replacing Hermes or adding a
memory/search service. A user space remains an isolated Hermes runtime; an
organization supplies membership, allowed features, approved read-only custom MCP servers,
read-only shared documents and organization action permissions.

## Scope

1. Add organization settings and membership validation.
2. Merge organization policy with the user settings before doctor/render/runtime.
3. Keep user credentials, Hermes state, browser state and memory in the user space.
4. Mount only organization documents read-only and expose them through the bounded MCP.
   Require an explicit Hermes `tools.include` allowlist for every organization MCP.
5. Gate the existing hub-owned HH mutation by organization action policy and disable
   built-in Telegram/Slack writes unless separately allowed.
6. Add GitLab/ClickHouse or a memory service only in a later change.

## Acceptance

- A non-member cannot resolve a user space into an organization runtime.
- A user cannot enable a feature or custom MCP server absent from the organization policy.
- A user can disable an approved organization MCP but cannot change its transport or tool allowlist.
- Organization documents are available only through a read-only root.
- Organization secrets are separate from user secrets and are never copied into user files.
- Existing standalone spaces and tests continue to work unchanged.
- `just check` passes.
