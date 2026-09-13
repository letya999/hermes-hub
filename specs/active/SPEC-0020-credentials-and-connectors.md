---
status: active
title: User-owned encrypted credentials, injection, OAuth broker and audit
---
# Credentials and connector platform

This specification freezes the M3 credential contract on top of SPEC-0017,
SPEC-0018 and SPEC-0019. It does not switch the default Hermes runtime to
ToolHub, patch Hermes, deploy Vault, or perform live provider login.

## Ownership versus ciphertext

ToolHub remains the only ownership and authorization registry. A
`CredentialReference` carries `backend`, an opaque `locator`, symbolic key
names, revision and status. It never carries a secret value or ciphertext.

Ciphertext lives in a separate store opened by `internal/credstore`. The
encryption key is supplied by `HUB_CREDENTIAL_KEY` or `HUB_CREDENTIAL_KEY_FILE`
and is never written to the repository, the ToolHub snapshot, the ciphertext
file, or a ciphertext backup. Writes are atomic with mode 0600.

A later Vault product client may implement the same `Backend` interface. The
`vault` backend name is a locator seam only: swapping it does not change
exact-owner checks, binding IDs or `AuthorizeProjected` semantics.

## Resolution and injection

`AuthorizeProjected` is the only path that may resolve a locator. After that
fence, the shipped ToolHub endpoint decrypts the locator for the authorized
owner (without re-entering the fence) and injects values only into the selected
workload environment or workload-owned files. CLI workloads receive the
authorized environment through `CallEnv`. MCP/container workloads receive
`credentials.env` under the workload workspace; the private vMCP HTTP call
never carries secret values. Shared workloads may receive credentials only
when every input is `per_request: true`. Static user credentials cannot be
shared.

Injected material is wiped on stop or failure. Hermes, vMCP, catalog,
manifests, audit, logs, process arguments and other connectors never receive
the value. Prompt-injected identity or credential fields are rejected before
injection.

Personal-terminal exposure is an explicit, reversible, owner-scoped flag on
the connection. Default is off. Enabling it copies selected names into that
owner's `self-env.json` overlay; disabling it removes them. The overlay is
not a shared credential and is not a ToolHub authorization grant.

## Rotation, revocation and degraded state

Rotate, disable, remove and revoke increment connection and credential
revision, revoke the previous locator, deny the next `tools/call` on an
already-open ToolHub MCP session before backend execution, and stop affected
starting/running workloads. Cached `tools/list` visibility is not an
authorization grant.

A rotate that writes ciphertext and then fails to publish the new reference
leaves the connection in status `degraded`. Degraded connections are denied
until an operator retries a successful rotate or explicitly revokes them.

Hermes transport reconnect is outside this specification (#74).

## User mutation paths

`hubctl secret set|list|delete` is the host CLI. Values are read from stdin or
`--from-file`, never from process arguments. List and replies print names and
status only. Set and delete load `HUB_TOOLHUB_STORE` (or
`spaces/<user>/runtime/toolhub/store.json`) and rotate or revoke matching
connections so an already-open ToolHub MCP session is denied before backend
execution.

An explicit owner chat message containing connector `KEY=value` lines is
intercepted by the communication hub before a Hermes job is enqueued. The
original message is best-effort deleted. Groups reject credential entry and
never persist values. When the ToolHub registry is present, the intercept
rotates matching connections. Organization documents and untrusted tool output cannot
authorize a mutation.

When `HUB_CREDENTIAL_STORE` is unset, chat intercept still refuses to enqueue
the message to Hermes.

## OAuth broker

One broker implements authorization-code with PKCE (S256) and device
authorization. State is bound to principal, context, connection and expiry,
is single-use, and is rejected when the redirect is not allowlisted. Refresh
is serialized per connection. Tokens are stored only through the encrypted
credential backend.

Provider endpoint URLs are the official values:

| Provider | Authorization | Token | Device |
|---|---|---|---|
| Google | `https://accounts.google.com/o/oauth2/v2/auth` | `https://oauth2.googleapis.com/token` | `https://oauth2.googleapis.com/device/code` |
| Slack | `https://slack.com/oauth/v2/authorize` | `https://slack.com/api/oauth.v2.access` | not used |
| Atlassian | `https://auth.atlassian.com/authorize` with `audience=api.atlassian.com` | `https://auth.atlassian.com/oauth/token` | not used |
| Remote MCP | RFC 9728 `/.well-known/oauth-protected-resource` then RFC 8414 `/.well-known/oauth-authorization-server` | discovered `token_endpoint` | discovered when present |

Live Google/Slack/Jira login is not required to satisfy this specification.

## Audit ledger

`internal/audit` appends privacy-preserving events: who, runtime, connection,
policy revision, credential revision, outcome, and correlation among
communication job, Hermes run and tool call. ToolHub records `job_id` and
`hermes_run_id` from `X-Hub-Job-ID` / `X-Hub-Run-ID` on the MCP request and
generates `tool_call_id` per call. The communication worker appends job events
with `job_id` and `hermes_run_id` when a ledger is configured. Prompts, message
bodies, tokens, ciphertext and tool results are rejected by schema. Users may
list their own events. Credential mutations and tool admission fail closed when
the ledger cannot be written.

## Verification

Regression tests cover encrypt/decrypt, cross-user, stale and removed
locators, restart, restore, key rotation, catalog/store/audit omission of
values, inject-after-authorize through `NewEndpointHandler` against a real
ciphertext store, MCP file inject without HTTP secret leakage, shared-static
denial, terminal exposure reversal, open-session revoke before backend from
`hubctl secret` and chat intercept, workload stop, degraded rotate,
communication job/run/call ledger correlation, group rejection, OAuth
state/redirect/refresh, official endpoint constants, and owner-scoped activity
listing. They do not claim live provider login or live ToolHive/VPS isolation.
