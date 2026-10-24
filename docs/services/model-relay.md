---
description: model-relay — per-space model selection endpoint for managed spaces; layered models.yaml, owner switch bearer, cliproxy upstream.
last_verified: 2026-10-24
---

# model-relay

`hub-runtime model-relay` (`internal/runtime/model_relay*.go`), deployed only
in `managed` capability mode as the `model-relay` compose service. Listens on
`0.0.0.0:8318`; reachable as `model-relay-<user>` on the shared network (the
per-space alias stops name collisions across spaces) and on the managed agent
network.

## What it does

- Serves the per-space model ladder and the owner's switch operation. The
  merged registry mounts read-only at `/run/models.yaml` — rendered by
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

- A `communication-hub` rendered inside the managed space gets
  `HUB_MODEL_RELAY_URL=http://model-relay-<user>:8318` plus the
  `model-relay.auth` env file, so `/model` goes through the relay rather than
  the runtime.
- The shared `communication-hub` (infra space) serves several managed
  principals: `RenderEnvironment` enrolls each sibling's
  `HUB_MODEL_RELAY_ADMIN` into `model-relay-admins.json` (principal →
  `{url, admin}`), mounted read-only at `/config/model-relay-admins.json`
  with `HUB_MODEL_RELAY_ADMINS_FILE`. `/model` resolves the caller's
  principal from the authenticated envelope — never from message text — and
  the relay re-checks the bearer, so an unmapped principal gets no relay and
  a mapped one can only switch its own granted models.
- `PUT /v1/model` switches the logical model (admin bearer only);
  `GET /v1/model` reports the effective selection (same bearer).
- Spawned runtimes inherit the same merged registry and reach the relay over
  the managed agent network.
