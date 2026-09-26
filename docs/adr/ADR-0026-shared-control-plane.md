---
description: One ToolHub, one Credential Broker and one workload controller serve all spaces; per-user isolation moves from duplicated Compose projects to token-scoped identity plus a shared runtime network.
last_verified: 2026-09-25
---
# ADR-0026: Shared control plane, scoped compute

Accepted 2026-09-25.

## Decision

ToolHub, the credential broker, the workload controller and cliproxy are
control-plane services rendered once by the space whose `settings.yaml` keeps
`infra: true` (default). A space with `infra: false` renders only its
`hermes-runtime` and user volumes — it never re-declares shared services, host
ports or the shared broker state volume.

All shared services and every spawned runtime join the external Docker network
`hermes-hub-runtime` (created once by the operator or the first `up`). Spawned
runtimes no longer join per-user project networks: the supervisor passes
`--network hermes-hub-runtime`, and the compose name `toolhub` resolves to the
single shared service for every user. The default `HUB_TOOLHUB_ENDPOINT`
returns to `http://toolhub:8090/mcp`; `host.docker.internal` is removed as the
endpoint default.

One ToolHub process authenticates many identities. `HUB_TOOLHUB_TOKENS_FILE`
points at a JSON object mapping extra bearer tokens to full identity envelopes
(`principal_id`, `context_id`, `runtime_id`, `policy_version`), validated on
startup and merged with the primary `HUB_TOOLHUB_TOKEN` (duplicates are
rejected). Enrolling a space means writing its token→envelope entry into the
owning space's `toolhub-tokens.json`; the store already keys every binding,
connection and credential by `(principal, context, runtime)`, so data stays
owner-scoped inside the shared service.

## Why

Rendering the full stack into every `hermes-hub-<user>-<env>` project gave each
user a private copy of shared services bound to identical loopback ports
(8090/8787/8545/8317). Only the first project could start; the
`host.docker.internal` workaround then routed a user's runtime into whichever
ToolHub owned the host port, whose single-token auth rejected the foreign
bearer — silently stranding secondary users. Splitting ownership (infra flag),
sharing one network and authenticating N envelopes restores the intended "one
hub, scoped users" model and matches the ToolHive/vMCP gateway pattern
(centralized auth per request, backends scoped per tenant).

## Consequences

- Two spaces on one host no longer compete for `127.0.0.1:8090` and friends;
  the ports exist once, published by the infra-owning project.
- Runtimes lose L2 network isolation from each other; the boundary moves to
  ToolHub token→envelope auth plus per-user volumes, same as the oracle
  scope model. If L2 isolation is ever needed again, runtimes get a second
  dedicated network without changing this model.
- Operators must create `docker network create hermes-hub-runtime` once before
  `up`; `doctor`/render declare it `external` so a missing network fails fast.
- `toolhub-tokens.json` is secret material: it lives under `spaces/<owner>/`,
  is bind-mounted read-only and is covered by the existing `spaces/` gitignore.
- Rotating a secondary token means editing one JSON key; no per-user service
  restarts beyond the owning ToolHub.
