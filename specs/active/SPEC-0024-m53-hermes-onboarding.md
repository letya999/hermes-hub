---
status: active
title: M5.3 Hermes-mediated MCP onboarding
---
# Hermes-mediated MCP onboarding

Hermes, not the communication gateway, interprets an ordinary user message and
invokes the authenticated ToolHub control MCP. A self-install grant is still
required; the model cannot create one.

`prepare_source` accepts either an exact GitHub commit URL or a canonical public
repository URL. For a repository URL ToolHub resolves the default branch once,
validates a lowercase 40-character commit SHA, persists that SHA on the
onboarding record, and passes only the immutable source to review/build.
Ambient Git credentials, cookies, redirects and proxies are not used.

Long control calls use standard MCP progress notifications. User-facing clients
must surface acceptance, source review/build, workload start, credential/OAuth
requirement, credential acceptance, ready and failure without exposing secret
values, tokens, container identifiers, internal URLs, stack traces or foreign
identifiers.

The generic controller is provisioned once and must not require a definition or
digest edit for each self-install. On every authenticated admission it validates
the complete immutable ToolHub definition and rejects missing provenance, SBOM,
tool-contract, execution-policy or digest evidence. Hermes normalizes the pinned
upstream `tool.started`/`tool.completed` events into user-visible step progress;
an install turn must state the current step and a bounded ETA before a long build.

Credential forms remain loopback-only for the local MVP and are suitable only
for Telegram Desktop/Web on the same computer. Phone use requires a separately
specified authenticated temporary ingress; binding the form publicly is not an
acceptable fallback.

Completion requires real evidence for Telegram delivery into Hermes, Hermes
control-tool invocation, stock ToolHive lifecycle, automatic projection refresh
without Hermes process restart (ToolHub writes a durable revision marker and the
serve runtime invokes Hermes `/reload-mcp` through its authenticated API), a
read-only Notion call, idempotency, immediate
disable/revoke/remove and Alice/Bob isolation. Fixture tests cannot satisfy the
real Telegram, ToolHive, provider-login or Notion evidence rows.
