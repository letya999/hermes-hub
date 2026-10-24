---
description: model-relay — per-space model selection endpoint for managed spaces; layered models.yaml, owner switch bearer, cliproxy upstream.
last_verified: 2026-10-20
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

- `communication-hub` gets `HUB_MODEL_RELAY_URL=http://model-relay-<user>:
  8318` plus the `model-relay.auth` env file in managed mode, so channel
  surfaces (e.g. `/model` commands) go through the relay rather than the
  runtime.
- Spawned runtimes inherit the same merged registry and reach the relay over
  the managed agent network.
