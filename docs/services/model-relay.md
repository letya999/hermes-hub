---
description: model-relay — per-space model selection endpoint for managed spaces; layered models.yaml, owner switch bearer, cliproxy upstream.
last_verified: 2026-11-07
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

Registry layers on unmanaged spaces are `spaces/models.yaml`,
`spaces/<organization>/models.yaml`, then `spaces/<owner>/models.yaml`;
later entries replace the same named entry. Under a published passport
revision the layered files are never consulted — the `models.yaml`
passport (`services`/`routes`/`default`) compiles into the mounted
registry alone and is the only model authority. For example, these are
references, not credential values:

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
provider, authored under `models.services.<name>` in a six-passport space. It
permits private and loopback addresses, but not metadata/link-local,
multicast or other reserved ranges. Built-in CLIProxy has this local trust.

Every ladder has 1–8 ordered hops — one credential principal per hop.
Auth/quota failures, server failures and transport failures put that account
on a 60-second cooldown and try the next declared hop. A hop may carry
`models: [...]`, the ordered in-service model list: a model-level failure
(400/404 — the account does not serve that model) retries the next model on
the same principal instead of advancing the ladder; `model:` stays the
single-model shorthand. Once a response has started it is never replayed to
another hop. `reasoning: true` declares the service accepts reasoning
parameters; a request carrying `reasoning_effort`, `reasoning` or `thinking`
skips hops that never declared support. `sharing: personal` requires
`broker_grants` — a shared `key_env` principal cannot be declared personal.
Other HTTP statuses pass through. Each attempt/skip is logged
without request contents or keys. `GET /v1/hops` exposes the selected ladder's
cooldown under owner/admin authentication. Defaults are 5 minutes per hop
(`HUB_MODEL_RELAY_HOP_TIMEOUT`, maximum 5 minutes) and 6 minutes for the whole
request (`HUB_MODEL_RELAY_TIMEOUT`, maximum 10 minutes); hop cannot exceed total.
Durable `{of, amount, per, scope}` limits may be declared per hop:
`of: requests` counts every upstream call, `of: tokens` reserves the
conservative request estimate up front; `per` is `5h`, `1d` or `1w` in aligned
windows; `scope` is `shared` (the principal) or `personal` (per owner).
Counters persist under the relay state directory, survive restarts, are
isolated per space, and a full window parks the hop so the ladder advances.
`of: spend` and `of: provider_quota_percent` are rejected until a verified
price feed or per-session usage/reset telemetry exists — an unverifiable
quota must fail closed rather than drift.
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

### Reviewed model contracts (ADR-0092 R7)

Two contract types ship in `services/credential-broker/examples/contracts/`
and must be installed into the space broker `contracts_dir` by the operator:

- `model-api-key` — one bounded API key, delivered only as `MODEL_API_KEY`
  env on the `model-relay` workload lease (`broker_grants` + `credential_env`
  in `models.yaml` reference it; the relay resolves per call and releases).
- `cliproxy-session` — one bounded OAuth account session file, delivered as
  a mounted file (`env_name` points at the path, never the contents) into the
  owning dedicated cliproxy's `auths/` view. Each account is a separate
  credential with its own grants; rotation mints a new revision under the
  same connection and revocation kills existing leases at next acquire —
  independently per account, never across principals.

Migrating a space off the legacy `cliproxy/api-key` + `auths/` material or a
`key_env` provider is an explicit operator action: enroll each account as a
`cliproxy-session` credential, grant it to workload `model-relay`/`cliproxy`
with execution `dedicated`, point `broker_grants` at the new grant ids, then
re-render. Bytes never enter passports, ledger DTOs, argv or diagnostics —
the journal and every API view carry refs only.

### Per-instance session delivery (S7.3)

Each declared `cliproxy-*` instance gets its own init companion
(`<instance>-init`, entrypoint `hub-runtime cliproxy-materialize`) and two
tmpfs volumes: `cliproxy-session-<instance>` mounted at `/cliproxy/auths` and
`cliproxy-cfg-<instance>` mounted at `/run/cliproxy-cfg`. The init companion
is the only container that sees the shared `broker-materialized` lease view;
the cliproxy process never mounts it. The companion reads the published
models passport and `generated/model-grants.json` at `/run/model-gen`,
acquires the workload lease for the service whose endpoint names its
instance, copies the materialized session file into `auths/` as exactly
`session.json`, renders the operator config plus the `proxy_access` key into
`/run/cliproxy-cfg/config.yaml` (`api-keys:`), and releases the lease. A tmpfs
volume only exists while a live container holds its mount, so the companion
then stays up holding the session and cfg mounts; its healthcheck reports the
moment both files exist and the runtime service depends on `service_healthy`.

Enrollment is `hubctl models login --dir <space> --service <name>`: it mints
an idempotent `cliproxy-session` + `model-api-key` request per auth ref, the
owner submits secrets through the reviewed link, and a re-run grants each
ready credential to binding/workload `<instance>` (execution `dedicated`) and
records ref→grant ids in `generated/model-grants.json`. The registry
projection resolves each hop's credential through that map, so rotation is a
map update, not a passport change. Once both grants exist the command
removes the plaintext `session.json` under the instance bind `auths/` — the
tmpfs delivery is the only place session material may exist.

For sibling spaces the external `broker-materialized` reference resolves to
the infra project's volume (`hermes-hub-<infra-owner>-<env>-broker-materialized`),
located through `InfraSpaceDir`; a space rendering its own infra declares the
tmpfs locally instead. If more than one sibling claims infra ownership, the
resolver does not choose one by directory order; the shared reference stays
local until the deployment has one unambiguous owner.

Every space rendering cliproxy instances also declares `broker-secrets-cliproxy`
(project-local, mounted read-only only in the init companion). Provision its
CA as `server.crt` and an Ed25519 signing key as `cliproxy.private`, owned by
the service uid with mode 0600 — the same procedure as `broker-secrets-model`
above — and register the public key with the Broker under ID `cliproxy`,
issuer `hermes-cliproxy-session`, audiences `broker:control` and
`broker:runtime`.
