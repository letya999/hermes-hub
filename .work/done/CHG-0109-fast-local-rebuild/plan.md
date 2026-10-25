# CHG-0109: Fast local Go service rebuild

## Goal

Rebuild one Go service in a dev stack without rebuilding the shared Hermes image or unrelated cell images, while keeping old image layers bounded.

## Scope

- `cmd/hubctl`: dev-rebuild command, service-to-binary mapping, bounded Docker lifecycle.
- Temporary Compose override for the selected service; one dev runtime image
  selection file for supervised spawns.
- `internal/supervisor`: use the selected dev runtime image on the next spawn.
- `docker/Dockerfile.dev-overlay`: one-binary overlay on the existing pinned dev image.
- Tests and operations documentation for the new lifecycle.

## Slices

1. Validate service selection and build one Linux Go binary before any Docker mutation.
2. Build a small overlay image, record its dev-only selection, and recreate only the requested Compose service.
3. Select the dev runtime image for supervised spawns without interrupting active work.
4. Verify failure handling, cleanup, documentation, and `just check`.

## Boundaries

No provider operations, source mounts into managed runtimes, prod changes, dependency updates, or automatic restart of active supervised tasks.
