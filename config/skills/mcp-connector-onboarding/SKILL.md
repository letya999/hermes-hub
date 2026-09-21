---
name: hermes-hub-mcp-onboarding
description: "Route requests to install or connect an MCP, connector, or CLI through the authenticated ToolHub onboarding flow."
license: AGPL-3.0-only
metadata:
  hermes:
    tags: [MCP, connectors, ToolHub, GitHub, onboarding, OAuth, credentials, CLI]
---

# MCP and connector onboarding

Use this skill whenever the owner asks to add, install, connect, enable, or set up a
new MCP, connector, integration, or CLI-backed service, or asks to continue, resume,
check, or finish an earlier connector setup. A public GitHub repository URL is a
self-install request, not a request for package-management instructions.

## Mandatory routing

- Do not use `terminal`, `patch`, `git clone`, `npm`, `npx`, `node`, or a CLI installer
  to set up the requested service.
- Do not edit `~/.hermes/config.yaml`, `/state/hermes/config.yaml`, `.env` files, or
  workspace files. Hermes configuration is managed by the runtime and ToolHub.
- For a GitHub URL, call `mcp__toolhub__prepare_source` with `source` equal to the
  URL and a stable `request_key` for this user and request. Do not invent a commit
  SHA; ToolHub resolves and pins the immutable source. This must be the first
  ToolHub lifecycle call for every explicit install/add message containing a GitHub
  URL, even when older chat history mentions that connector. Never call `remove`,
  `revoke`, `disable`, or `status` first unless the current message explicitly asks
  for that lifecycle action.
- Follow the returned `onboarding_id` with `mcp__toolhub__status`. If credentials are
  required, call `mcp__toolhub__required_credentials` and show the returned protected
  `form_url` or OAuth URL. Never ask the owner to paste a secret into chat.
- On a follow-up without the original URL or `onboarding_id`, call `status` with the
  connector's `definition_id` (for example `notion-mcp-server`). ToolHub resolves the
  latest onboarding for this user. If it is awaiting credentials, call
  `required_credentials` with that same `definition_id` and return the protected URL.
  Never infer progress from chat history and never fall back to manual configuration.
- After the user completes authorization, poll `status` until the phase is
  `awaiting_confirm`, then call `mcp__toolhub__confirm` with `onboarding_id` and the
  returned `nonce`. Call `mcp__toolhub__enable` for that `onboarding_id` and poll
  `status` until `enabled`/`ready` or `failed`.
- Report ToolHub progress and errors plainly. Never claim installation, readiness, or
  available provider tools before ToolHub returns the corresponding phase. A failed
  or unavailable ToolHub call means the current state is unknown: do not reuse an old
  chat result, claim a previous clone/token still exists, or suggest manual config.
- After enable, read `projected_tools` from `status` and call a provider tool through
  `mcp__toolhub__invoke` when the native MCP client has not loaded it yet. Pass one of
  those exact names and its normal arguments; never construct or guess the name. Do
  not claim live access until that invocation succeeds.

For a known catalog connector without a repository URL, use the same ToolHub control
flow with its catalog definition; do not bypass ToolHub with manual Hermes config.
