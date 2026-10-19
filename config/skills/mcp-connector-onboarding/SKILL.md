---
name: hermes-hub-mcp-onboarding
description: "Route requests to install, manage, or remove an MCP, connector, or CLI through the unified ToolHub connector_* tool surface."
license: AGPL-3.0-only
metadata:
  hermes:
    tags: [MCP, connectors, ToolHub, GitHub, onboarding, OAuth, credentials, CLI]
---

# MCP and connector lifecycle

Use this skill whenever the owner asks to add, install, connect, enable, disable,
reauthorize, or remove an MCP, connector, integration, or CLI-backed service, or
asks to continue, resume, check, or finish an earlier connector operation. A
public GitHub repository URL is an install request, not a request for
package-management instructions.

## Mandatory routing

- Do not use `terminal`, `patch`, `git clone`, `npm`, `npx`, `node`, or a CLI
  installer to set up the requested service.
- Do not edit `~/.hermes/config.yaml`, `/state/hermes/config.yaml`, `.env` files,
  or workspace files. Hermes configuration is managed by the runtime and ToolHub.
- Every connector action is exactly one `mcp__toolhub__connector_*` call:

  | Intent | Tool | Arguments |
  |---|---|---|
  | find a connector | `connector_search` | `query` |
  | list mine | `connector_list` | — |
  | install | `connector_install` | `target` (name, alias, or source URL) |
  | check state | `connector_status` | `target` or `operation_id` |
  | enable / disable | `connector_enable` / `connector_disable` | `target` |
  | allow writes | `connector_allow_writes` | `target`, `on` |
  | re-enter credentials | `connector_reauth` | `target` |
  | remove | `connector_remove` | `target` |

- `target` is the connector's name, alias, source URL, repository basename, or
  registry name — ToolHub resolves it against the caller's own connectors. An
  `ambiguous_target` result carries a `candidates` list; retry with an exact id
  or URL, never guess.
- Every call returns `{operation_id, state, code?, summary, link_sent?}` — the
  durable journal resumes itself. When `state` is `awaiting_user` or
  `awaiting_confirm` and `link_sent` is true, the action link was already
  delivered to the user through the channel; wait for the human step and then
  repeat the same call (or `connector_status` with `operation_id`) to continue.
  A repeat call after the human action resumes the same operation — it never
  duplicates the install or the remove.
- `state: failed` carries a typed `code` (`ambiguous_target`,
  `unresolved_target`, `unsupported_auth`, `probe_failed`, `cleanup_incomplete`,
  `not_permitted`, `quota`). Report `summary` plainly and retry the same call
  only when the code is retryable.
- Write-effect tools stay hidden until `connector_allow_writes` with `on: true`
  is confirmed by the user through the link; `on: false` applies immediately.
- `connector_remove` is destructive but convergent: the first call sends a
  one-time confirmation link, the human click purges credentials, workspace and
  runtime state, and a repeated call returns the finished operation.

## Never

- Never ask the owner to paste a token, secret, nonce, or link into chat — the
  operation result intentionally carries no link material.
- Never claim installation, readiness, or provider access before a call returns
  `state: done`; never infer progress from chat history.
- Never fall back to manual configuration when a ToolHub call fails — report the
  returned `code`/`summary` and let the human act on the delivered link.
