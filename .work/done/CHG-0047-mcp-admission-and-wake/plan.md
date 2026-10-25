---
description: Admit MCP servers that refuse tools/list until real credentials exist, enroll sibling ToolHub tokens, and deliver a lost first turn.
last_verified: 2026-09-27
---

# CHG-0047 MCP admission and wake

## Change

- A `tools/list` that fails twice and names secret env vars opens the protected
  form instead of failing the install or inventing a tool contract. The confirming
  list uses the submitted values and allows network only on that probe.
- The infra owner enrolls sibling `HUB_RUNTIME_AUTH` values into
  `toolhub-tokens.json` in place. ToolHub reloads the file on an auth miss.
- An Init SOUL stub is replaced from the repo template on render, supervisor
  spawn, and runtime start. Edited SOUL content stays.
- A chat turn that fails before any tool call, within 20 seconds, is retried
  once. A finished background `prepare_source` also posts a channel notice and
  a continuation job. Progress text for a still-preparing step does not claim
  the image is built.
- Hub MCP instructions tell the model to stop when ToolHub is not on that
  server, instead of installing a GitHub repository from the terminal or
  `config.yaml`.
- Alternative sets stay separate submits of the names the server or recipe
  reported. The shortest set is open and the others stay collapsed. A rejected
  submit names the reason and does not say the secret was saved. A failure
  after the secret is stored says it was saved and points back to chat
  confirmation. The prepare notice includes the loopback form URL.
- A credential-gate definition with no reviewed broker contract confirms from
  the local store even when the broker process is configured. A reviewed
  contract still requires a broker credential.
- Another principal does not replace an immutable definition or a user
  publication. That submit stores the next free patch version.

## Verification

`just check`. Docker and live Slack login are separate gates.
