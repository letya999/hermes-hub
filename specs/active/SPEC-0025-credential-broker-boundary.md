---
title: Credential Broker boundary and opt-in integration gate
status: active
---

# Credential Broker boundary

`services/credential-broker` is a copied, separately licensed Go module. It is
available in the monorepo through `go.work`, root build recipes, the runtime
image and CI. It is opt-in per deployment: setting the three Hub-side Broker
URLs/key configurations enables the Broker-only credential path for that
deployment; leaving them unset preserves the existing compatibility path.

The Broker owns reviewed credential contracts, browser approval pairing, provider
references, grants, leases and runtime materialization. Hermes Hub continues to
own canonical identity creation, Communication Hub delivery, ToolHub source
review and workload admission, binding projection and reconnect.

The integrated path now proves the following with the copied Broker SDK and
HTTP contract tests:

1. purpose-separated signed assertions for `broker:control`, `broker:approve`
   and `broker:runtime`;
2. durable onboarding-to-request mapping and idempotent retry;
3. approval URL delivery through the authenticated Communication Hub path;
4. compatible contract-to-ToolHub credential mapping without guessed provider
   APIs;
5. grant, lease, revoke and restart behavior with no plaintext in Hermes
   messages, projections or ledgers; and
6. same-path ENV materialization plus ToolHive readiness evidence.

The required environment is `HUB_CREDENTIAL_BROKER_CONTROL_*` for ToolHub,
`HUB_CREDENTIAL_BROKER_APPROVE_*` for Communication Hub and
`HUB_CREDENTIAL_BROKER_RUNTIME_*` for runtime materialization. A reviewed
ToolDefinition carrying credentials must declare `credential_contract_id`,
`credential_contract_revision` and an explicit `credential_contract_env`
mapping; missing mappings fail closed instead of guessing a provider API.
Communication approval is `/credentials <request-id> <code>` and returns no
credential data. `api/v1.Materialized` remains runtime-only.

File deliveries use the ToolHive `--volume ...:ro` hand-off when the generic
controller has an explicit `credential_mount_root`. The controller accepts
only regular non-symlink files below that root, starts a one-call workload and
removes it through the authenticated `/release` endpoint before the runtime
adapter releases the Broker lease. A deployment that does not share the
Broker materialization directory with the ToolHub/controller and Docker daemon
fails closed; it must not copy the secret to a persistent workspace.
