# CHG-0022: Credentials and Connector Platform (M3)

## Objective

Deliver user-owned connections and credentials without leaking secret values
through Hermes, manifests, catalog, audit, logs, process arguments or other
connectors. Close GitHub issues #47, #48, #51, #27, #49, #50 and #28. Leave
#73 (live ToolHive/VPS) and #74 (Hermes reconnect) open.

## Scope

1. Opaque credential locators in the ToolHub registry; ciphertext lives in a
   separate encrypted store. ToolHub remains the ownership/authorization
   boundary. A Vault-named backend reuses the same locator contract.
2. Local AES-256-GCM storage, key outside repository/store/backup, atomic
   writes, backup/restore and key rotation.
3. Inject only after `AuthorizeProjected` into the selected workload.
   Static user credentials cannot be shared. Personal-terminal exposure is an
   explicit reversible owner-scoped mode.
4. Rotate/disable/remove/revoke increment revision, deny the next ToolHub call
   on an already-open MCP session before backend execution, stop the affected
   workload, and leave a failed rotate in explicit degraded status. Audit
   records the revision. Hermes reconnect is not implemented.
5. `hubctl secret` set/list/delete. Chat `KEY=value` is intercepted before
   Hermes and best-effort deleted. Replies contain names and status only.
   Groups reject credential entry.
6. One OAuth broker for authorization-code+PKCE and device flow against a
   local fixture, using official Google, Slack, Atlassian and remote-MCP
   discovery URLs. Tokens persist only in the encrypted store.
7. Privacy-preserving audit ledger correlating job, Hermes run and tool call.

## Explicitly deferred

M4/M5/M6, live Google/Slack/Jira login, live ToolHive/VPS (#73), Hermes
transport reconnect (#74), default runtime switch to ToolHub, ZITADEL, Vault
product deployment and Kubernetes.

## Shipped-path successor (verification gaps)

The first M3 merge left four real-path gaps. This change set wires them without
re-entering `AuthorizeProjected` from the injector:

- `NewEndpointHandler` opens `HUB_CREDENTIAL_STORE` and decrypts the authorized
  locator inside admit
- `RoutingBackend` forwards env to MCP; `MCPBackend.CallEnv` writes
  `credentials.env` and never puts secrets on vMCP HTTP
- ToolHub audit fields include `job_id` / `hermes_run_id` / `tool_call_id` from
  MCP headers plus a generated call id; the communication worker appends job
  events
- `hubctl secret` set/delete and chat `ApplyChat` load the ToolHub registry and
  rotate/revoke so an open MCP session is cut before backend execution

## Verification

- In-repo Go tests for store, inject, rotate/revoke, intercept, OAuth fixture
  and audit
- `NewEndpointHandler` inject test against a real credstore (not a stub Injector)
- MCP env/file inject without HTTP secret leakage
- Ledger correlation from communication worker and ToolHub headers
- Open-session cut from `hubctl secret set` and chat intercept
- `hubctl secret` set/list/delete twice on a throwaway space
- Local OAuth fixtures for authorization-code+PKCE and device flow
- `just check` with own Go statement coverage >=85%
- `just security` only if module/lockfiles change
- `just docker-check` when Docker is available

## Rollback

Leave `HUB_TOOLHUB_*` unset to keep generated MCP. Leave
`HUB_CREDENTIAL_STORE` unset so the communication hub refuses chat secrets
instead of enqueueing them to Hermes. Restore the previous ciphertext file
and key; do not delete spaces or Hermes homes.
