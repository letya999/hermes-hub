---
name: hermes-hub-mcp-onboarding
description: "Route requests to install, manage, or remove an MCP, connector, or CLI through the unified ToolHub connector_* tool surface."
license: AGPL-3.0-only
metadata:
  hermes:
    tags: [MCP, connectors, ToolHub, GitHub, onboarding, OAuth, credentials, CLI]
---

# MCP and connector lifecycle

For terminology and inventory, read `hermes-hub-system-guide` (ToolHub
`system_guide` in managed mode). MCP, CLI and hub executors must retain their
actual format labels. Current connections, catalog entries and setup history
are separate categories.

Use this skill whenever the owner asks to add, install, connect, enable, disable,
reauthorize, or remove an MCP, connector, integration, or CLI-backed service, or
asks to continue, resume, check, or finish an earlier connector operation. A
public GitHub repository URL is an install request, not a request for
package-management instructions.

## Mandatory routing

- For a service name without an exact source, call `connector_search` first.
  Show up to five returned candidates with their repository URLs and prepared
  or registry status. Ask the user which one to install; do not silently pick
  the first search result. If fewer candidates exist, show only those found.
- An exact matching prepared recipe (for example Google Workspace) can be
  installed directly by its prepared id when the user requested that service.
  An explicit repository URL or an explicit selection authorizes that source,
  not a different implementation. Pass the selected candidate id as `target`;
  do not replace it with a bare repository URL and lose its pinned revision.
- When a selection expires, search again and ask for the renewed choice if the
  source or revision changed. Never interpret a missing MCP as permission to
  install a similarly named unrelated package.
- For repository metadata corrections use the reviewed `connection_recipe`
  contract on `connector_install` with an exact repository URL; top-level `credentials` belongs only to remote
  MCP URLs. Retain the exact source revision when retrying a failed install.
  Read the pinned source documentation before concluding that no credentials
  are needed: some servers list tools before checking authentication. File-based
  credentials require a reviewed file delivery in `connection_recipe`; never
  ask the user for a host path or a credential value in chat.

- Do not use `terminal`, `patch`, `git clone`, `npm`, `npx`, `node`, or a CLI
  installer to set up the requested service.
- Do not edit `~/.hermes/config.yaml`, `/state/hermes/config.yaml`, `.env` files,
  or workspace files. Hermes configuration is managed by the runtime and ToolHub.
- Prefer the advertised `mcp__toolhub__connector_*` surface:

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

- Some existing profiles advertise only the legacy ToolHub operations. In that
  case use the tools actually available: `discover` with `query`, then
  `prepare_source` with the chosen `candidate_id` or exact `source`, and `status`
  with the returned `onboarding_id`. These use the same review and protected
  credential flow. Pass a documented `connection_recipe` to `prepare_source`
  when needed. Do not change permissions or use the terminal to obtain missing
  tools. Report the legacy `phase` accurately: `awaiting-credentials` means the
  form is ready; `awaiting-confirm` means confirmation is pending, not that
  provider access has been verified.

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
- `state: done` establishes completion of that operation only. Check current
  connection status, tool admission, workload readiness and a suitable allowed
  provider read separately before claiming working provider access. Never infer
  progress or readiness from chat history.
- A failed candidate may be incompatible upstream. Report the actual error;
  offer alternatives from a new search and wait for selection before replacing it.
- Never fall back to manual configuration when a ToolHub call fails — report the
  returned `code`/`summary` and let the human act on the delivered link.
