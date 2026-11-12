# CHG-0299 — Standalone shared project and multi-session per-user cliproxy

## Goal

Shared infrastructure lives in its own Compose project `hermes-hub-shared-<env>`
instead of the infra owner's user project. A `per-space` cliproxy is one instance
per user that hosts every OAuth session the space's model services need — no
duplicate containers per session. `hub-media` and the rest of the shared control
plane stay shared across all spaces.

## Scope

- `internal/stack/compose_plan.go`: `SplitInfraCompose` renames the shared plan
  to `SharedProjectName(env)` (`hermes-hub-shared-<env>`) even when the owner has
  no per-space services; anonymous-by-key volumes pin to the pre-split infra
  project names so existing data reattaches.
- `internal/stack/render_env.go`: a shared-only owner writes the shared plan as
  `compose.<env>.yaml` as well as `compose.shared.<env>.yaml`.
- `cmd/hubctl/main.go`: model-check/compose consumers select the shared plan
  when it exists so container names resolve in the new project.
- `internal/credentialbroker/model_binding.go`: `CliproxySessionBinding` /
  `CliproxyKeyBinding` give each session ref and key ref its own workload slot
  on the owning instance (`<instance>`, `<instance>-<ref>`, `<instance>-key`,
  `<instance>-key-<ref>`); legacy unsuffixed slots keep existing grants valid.
- `internal/runtime/cliproxy_materialize.go`: materialize iterates every model
  service whose endpoint host equals the instance, deduplicates by ref, writes
  one `<ref>.json` per OAuth session and one api-key per key ref; falls back to
  legacy binding names for grants enrolled before ref-derived slots.
- `cmd/hubctl/models_login.go`: grant idempotency keys carry the binding slot
  so re-enrolling a service after an endpoint move mints a fresh grant instead
  of conflicting with the retired slot.
- `spaces/artem_two/passports/{stack,models}.yaml`: shared services marked
  shared; the separate `cliproxy-google` service declaration removed; the
  `cliproxy-google` model service endpoint retargeted to
  `http://cliproxy-artem_two:8317/v1` (session ref `google-antigravity`).

## Non-goals

- Browser and hosting stay statically declared; on-demand spawn is deferred.
- Remote CI acceptance — unpublished branch.
