# CHG-0185: hub-core image and compose role split (M26 stage 3)

Chain: `docs/adr/ADR-0083-lean-images-capability-packs.md` D1/D2 ->
`specs/active/SPEC-0082-image-role-budgets.md` R3 -> this plan -> `state.yaml`.
GitHub issue: #377. Previous stage: `CHG-0184`. Next stage: `CHG-0186`.

## Goal

Every service stops carrying the 4.6GB agent image. `hub-core`
(debian-slim + Go binaries + ca-certs, <=250MB) serves credential-broker,
communication-hub, hub-media, relays, cliproxy and supervisor; `hub-control`
adds docker CLI + thv + cellinit; only `hermes-runtime` keeps Python, Hermes,
node and the agent tool surface.

## Read before coding

1. `docker/Dockerfile` (targets: runtime/prod/control/cli-tools),
   `internal/stack/compose.go` (`common` ~L206, per-service image refs,
   `services["cliproxy"]`, healthchecks), `internal/stack/compose_test.go`,
   `cmd/devcheck/main.go`, `internal/devcheck/devcheck.go`.
2. `#371` budget gate (must pass per role), `docs/architecture.md`.

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. Go stdlib only unless an ADR allows a dependency.
- `just check` before handoff; `just docker-check` (or prebuilt gate) for
  image/compose changes.

## Slices

### S3.1 hub-core / hub-control targets (R3.1)

Anchors: `docker/Dockerfile` (new `FROM` targets, binary COPYs);
`go_build` stage binary list.

Change:
1. New `hub-core` target: debian-slim + ca-certificates + all Go binaries
   (hubctl, hub-runtime, toolhub, communication-hub, credential-broker,
   hub-media, cellinit, cli-proxy-api).
2. `hub-control` = hub-core + docker CLI + thv.
3. Remove `cli-proxy-api` from the runtime stage.

Tests (red first):
- `TestCoreImageContents`: expected binary set present, no python/node.
- `TestComposeServiceImages`: rendered compose maps each service to its role
  image.

### S3.2 compose role split (R3.2)

Anchors: `internal/stack/compose.go` `common`; `services` map; relay services.

Change:
1. `coreCommon`, `runtimeCommon`, `controlCommon` replace `common`; every
   service gets its minimal image.
2. `cliproxy["build"]` points at the core build (not the full Dockerfile).

Tests (red first):
- `TestNoServiceUsesRuntimeExceptRuntime`: render audit.

### S3.3 Healthchecks (R3.3)

Anchors: compose healthcheck entries; `cmd/hub-runtime` health subcommand.

Change:
1. Healthcheck commands run via a Go binary inside each role image.

## Red-team checklist

- No service silently keeps the fat tag (rendered-compose audit test).
- Control image never mounts docker.sock into a service that must not reach it.

## Acceptance

- A3: forbidden-binary gate green; all services pass healthchecks on dev.

## Out of scope

- Browser extraction (CHG-0186); distroless evaluation deferred.

## Done checklist

- [ ] Tests red then green; `just check`; Docker gates.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Issue #377 closed; folder moved to `.work/done/`.
