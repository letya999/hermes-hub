# CHG-0008: Telegram service self-service

## Goal

Let the owner inspect available connectors, enable supported ones and provision their
credentials from the authenticated Telegram conversation without editing host files.

## Scope

1. Expose a secret-free connector catalog through the hub MCP tools.
2. Accept explicit owner requests to enable self-service connectors and persist the
   selected feature set in the isolated runtime.
3. Accept only allowlisted `KEY=value` entries through the existing env-update boundary,
   restart the supervisor and merge selected upstream MCP definitions on restart.
4. Keep browser, gateway transport, native bridges and organization policy host-managed.
5. Add tests, setup guidance and security/quality gates.

## Non-goals

No public account linking, organization setting edits, secret retrieval, automatic OAuth
consent or provider mutations.
