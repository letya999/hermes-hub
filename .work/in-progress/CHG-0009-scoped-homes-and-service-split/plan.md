# CHG-0009: Scoped homes and communication-hub service split

## Goal

Implement SPEC-0009 without losing current user state or weakening isolation.

## Handoff rule

Implement one slice at a time. Run the slice checks before continuing. Do not combine
the storage migration and container split in one unreviewable change. Do not delete
old Docker volumes or compatibility aliases during this change.

## Slice 1 — Freeze names and scope paths

1. Add a `Scope` model with `kind`, `id`, optional `organization`, and canonical root.
2. Resolve both organization and user homes under `spaces/<scope-id>` and reject ID
   collisions, traversal, symlinks and kind mismatches.
3. Change `org-init` default from `organizations/<org>` to `spaces/<org>` and make
   both init paths create `scope.yaml` plus the target subdirectories from SPEC-0009.
4. Preserve explicit legacy `--dir` and `--org-dir` flags for one release.
5. Update scope tests first; keep standalone users valid.

Likely files: `internal/stack/config.go`, `internal/stack/scope.go`, their tests,
`cmd/hubctl/main.go` and CLI tests.

## Slice 2 — Make space directories the persistent storage

1. Render bind mounts from the selected user home for `hermes/`, `connections/` and
   `workspace/`; keep `archive/` read-only.
2. Mount the organization home separately and expose only the paths allowed for the
   resolved job scope.
3. Map existing runtime paths (`HERMES_HOME`, `HOME`, browser, Google, Telegram and
   workspace paths) onto the new host directories without changing upstream Hermes.
4. Configure organization skills through `skills.external_dirs`; test precedence and
   provenance. Route memory/session writes according to immutable `scope_id`.
5. Remove gateway spool ownership from the user state contract.

Likely files: `internal/stack/render.go`, `internal/runtime/*`, `internal/agenttools/*`
and their isolation tests.

## Slice 3 — Build the explicit migration

1. Add `hubctl migrate-spaces --user <id> [--org <id>] [--env dev|prod]` with dry-run
   default and `--apply` mutation.
2. Inventory the current host files and named volumes without reading secret or
   session contents into logs.
3. Copy into a staging directory, reject symlinks/collisions, verify checksums and
   counts, then rename the staging directory into place.
4. Leave source directories and volumes intact and print exact rollback instructions.
5. Add fixture-based tests for complete migration, interruption, retry and refusal
   while the runtime is active.

Likely files: a small new `internal/migration` package, `cmd/hubctl/main.go` and tests.

## Slice 4 — Move Hermes execution behind hermes-runtime

1. Move `HermesRunner` and process environment selection out of the communication
   package into the runtime package.
2. Define the private execution contract requested by SPEC-0009. It must carry the
   immutable job identity/scope fields, bounded prompt and idempotency key; it must
   return a bounded result or explicit uncertain outcome.
3. Bind each runtime instance to one configured user and optional organization, and
   reject mismatched jobs before opening any path or starting Hermes.
4. Preserve one-process-per-job behavior and the current timeout/cancellation rules.
5. Add HTTP-contract, authorization, replay and cross-user regression tests.

Likely files: `internal/communication/*`, `internal/runtime/*` and a minimal shared job
contract package only if importing one of those packages would create a cycle.

## Slice 5 — Split and rename the service

1. Rename `cmd/communication` and its binary to `communication-hub`.
2. Render separate `communication-hub` and `hermes-runtime` services on an internal
   network. Publish no runtime port to the host.
3. Give the communication service only its bot env, routing config and
   `communication-hub-data` volume. Give the runtime only its scope homes and provider
   env. Assert these mount/env boundaries in tests.
4. Keep `hub-communication` as a one-release executable alias if packaging still has
   consumers; generated deployments use the new name.
5. Move supervisor health/restart handling to the owning service.

Likely files: `docker/Dockerfile`, `internal/stack/render.go`, `cmd/package/*`, runtime
supervision, Docker smoke tests and operator docs.

## Slice 6 — Documentation and gates

1. Rewrite README, SETUP, START_HERE and current architecture/operations/integrations
   after the behavior exists. Remove legacy path text only after migration works.
2. Record measured checks in `docs/validation.md`; do not claim provider or Telegram
   integration from fake services.
3. Run `just check`, `just security` and both dev/prod `just docker-check` targets.
4. Manually inspect rendered Compose for secret and mount isolation.

## Stop conditions

Stop the slice and report the concrete blocker if the pinned Hermes version cannot
consume organization skills through `skills.external_dirs`, the private runtime
contract cannot distinguish failed from uncertain execution, or migration cannot
prove a recoverable source remains.
