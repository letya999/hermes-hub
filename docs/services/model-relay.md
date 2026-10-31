---
description: model-relay — per-space model selection endpoint for managed spaces; layered models.yaml, owner switch bearer, cliproxy upstream.
last_verified: 2026-10-31
---

# model-relay

`hub-runtime model-relay` (`internal/runtime/model_relay*.go`), deployed only
in `managed` capability mode as the `model-relay` compose service. Listens on
`0.0.0.0:8318`; reachable as `model-relay-<user>` on the shared network (the
per-space alias stops name collisions across spaces) and on the managed agent
network.

## What it does

- Serves the per-space model ladder and the owner's switch operation. The
  merged registry mounts read-only at `/run/model-config/models.yaml` — rendered by
  `internal/stack` from the layered `spaces → org → user` model sources.
- Persists selection state at `/run/model-state/model-select.json` (space's
  `managed/<env>/model-relay` dir).
- `HUB_MODEL_RELAY_OWNER` pins which principal may switch models.
- `HUB_MODEL_RELAY_CLIPROXY` points at the shared CLI-proxy upstream
  (`http://cliproxy:8317`, or `cliproxy-<user>:8317` when the space runs a
  dedicated cliproxy).

## Env and secrets

Env files: `runtime.<env>.env` + `model-relay.<env>.env` + `model-relay.auth`.
Provider `key_env` names resolve from the space's own secrets env file; the
owner-facing switch bearer comes from `model-relay.auth` — never as literal
values in compose. Quota accounting lives in `model_relay_quota.go`.

## Consumers

- `communication-hub` gets `HUB_MODEL_RELAY_URL=http://model-relay-<user>:
  8318` plus the `model-relay.auth` env file in managed mode, so channel
  surfaces (e.g. `/model` commands) go through the relay rather than the
  runtime.
- The shared `communication-hub` (infra space) serves several managed
  principals: `RenderEnvironment` enrolls each sibling's
  `HUB_MODEL_RELAY_ADMIN` into `model-relay-admins.json` (principal →
  `{url, admin}`), mounted read-only at `/config/model-relay-admins.json`
  with `HUB_MODEL_RELAY_ADMINS_FILE`. `/model` resolves the caller's
  principal from the authenticated envelope — never from message text — and
  the relay re-checks the bearer, so an unmapped principal gets no relay and
  a mapped one can only switch its own granted models.
- `PUT /v1/model` switches the model (admin bearer only): a granted logical
  name, or — under the `catalog` switch grant — any id the owner's accounts
  actually serve, written as `provider/model` or resolved as a bare id
  across the granted providers' live catalogs. `GET /v1/model` reports the
  effective selection `{model, provider?}` (same bearer).
  `GET /v1/registry` (admin bearer) reports each granted model with its
  ordered provider ladder — `{name, ladder:[{provider, model, down}]}` —
  plus `accounts`: every provider used by a granted ladder with its live
  upstream catalog (`GET {provider}/v1/models`, fetched with the hop's own
  credentials under a 10-second bound; a failed fetch just omits the list),
  and `switch`: the caller's resolved switch grant.
  This is the account view `/model` renders; provider URLs, env key names
  and grant ids never leave the mounted file.
- Spawned runtimes reach the relay over the managed agent network. They do not
  mount the registry, provider keys, Broker signing key or switch-admin bearer.

## Owner and provider boundaries

`model-relay.auth` contains independent `HUB_MODEL_RELAY_AUTH` (chat/catalog)
and `HUB_MODEL_RELAY_ADMIN` (selection/quota) tokens. Rendering preserves existing
tokens and adds a missing token during migration. The managed runtime receives
only the chat token as its `OPENAI_API_KEY`; the relay retains the original
provider credential. A foreign bearer or ungranted requested model is rejected
before dispatch. The pinned Hermes model name is an alias for the owner's saved
selection, so `/model` switching does not require editing Hermes.

Registry layers are `spaces/models.yaml`, `spaces/<organization>/models.yaml`,
then `spaces/<owner>/models.yaml`; later entries replace the same named entry.
For example, these are references, not credential values:

```yaml
providers:
  account_a:
    url: https://provider.example/v1
    owners: [alice, bob]
    broker_grants: {alice: grant-alice, bob: grant-bob}
    credential_env: MODEL_API_KEY
models:
  default:
    owners: [alice, bob]
    ladder:
      - {provider: account_a, model: provider-model}
```

The model's owner list cannot widen a provider's owner list or supply a missing
owner grant. `switch` maps owner (or `*`) to the model-switch grant: `none`
keeps automatic ladders only — even a granted name is refused; `logical`
(the absent default) allows granted registry names; `catalog` additionally
allows any model id the owner's granted providers report in their live
catalog. A catalog selection persists `{model, provider}` and re-validates
against the current registry on every request, so a revoked provider or a
downgraded grant stops routing immediately. Each upstream account should
have a distinct provider ID: cooldown
is shared across its logical models, isolated between owners and reset after a
registry reload. `key_env` is an alternative resolving the current owner's
process env; it cannot be combined with `broker_grants`. An anonymous custom
provider receives neither the runtime bearer nor an unrelated provider key.
Missing declared credentials fail closed.

## Network, deadlines and reload

Custom URLs are fixed HTTP(S) bases without userinfo, query, fragment or parent
traversal. The configured registry is the endpoint allowlist. DNS is checked
and pinned at dial time; redirects and environment HTTP proxies are disabled.
`allow_private: true` is an explicit operator trust exception for a local/LAN
provider. It permits private and loopback addresses, but not metadata/link-local,
multicast or other reserved ranges. Built-in CLIProxy has this local trust.

Every ladder has 1–8 ordered hops. Auth/quota failures, server failures and
transport failures put that account on a 60-second cooldown and try the next
declared hop. Other HTTP statuses pass through. Each attempt/skip is logged
without request contents or keys. `GET /v1/hops` exposes the selected ladder's
cooldown under owner/admin authentication. Defaults are 5 minutes per hop
(`HUB_MODEL_RELAY_HOP_TIMEOUT`, maximum 5 minutes) and 6 minutes for the whole
request (`HUB_MODEL_RELAY_TIMEOUT`, maximum 10 minutes); hop cannot exceed total.
The hop bound covers the entire exchange, including a streamed body. A transport
timeout allows one bounded recovery attempt after cooldown on a later request;
concurrent callers do not all probe the recovering hop. The request budget leaves room for cooldown and one more bounded attempt.
A slow provider can still exhaust the bound; the relay then fails closed.
Credential resolution consumes the hop budget. Broker lease cleanup has its own
bounded 5-second cancellation-independent deadline. SSE chunks flush as they
arrive; model rewriting preserves other JSON values, including large integers.

Rendering writes `generated/model-registry/<env>/models.yaml` atomically. The
relay mounts its narrow parent directory so atomic replacement is visible;
mounting the individual file would retain an old inode in Docker. Reload rejects
invalid/unknown fields and multiple YAML documents. An in-flight old request
cannot repopulate the new registry's cache. No automatic service restart is
needed for a registry-only edit after rendering.

## Broker provisioning (operator action)

Compose declares `broker-secrets-model`, mounted read-only only in the relay.
Provision its CA as `server.crt` and an Ed25519 signing key as `model.private`,
owned by the service uid with mode 0600. Add the matching public key to the
Broker's trusted keys with ID `model`, issuer `hermes-model-relay`, and audiences
`broker:control` and `broker:runtime`. The development Broker initializer does
not automatically create this production identity.

Each grant/contract must deliver the configured `credential_env`. The signed
actor has principal/context/runtime equal to the owner, conversation and delivery
`model-relay`, policy version `model-relay`, binding `model-relay-<provider-id>`
and workload `model-relay`. Acquire/materialize/release use TLS and signed
audiences; no signing key is mounted into Hermes. Shared secret use requires an
explicit Broker contract and separate authorized owner grants. Provisioning and
live account verification are separate from code/fixture verification.
