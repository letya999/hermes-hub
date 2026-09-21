# Docker build and disk diagnosis

## Scope

- Investigate slow Hermes Hub Docker builds and disk growth using only the local repository and read-only Docker CLI commands.
- Trace `docker/Dockerfile`, `justfile`, `.dockerignore`, generated Compose, layer history, current and untagged images, containers, volumes, BuildKit cache, project `spaces/*/local/runtime/artifacts`, and ToolHub persistence.
- Separate evidence for base-image size, cache invalidation, dangling images, persistent `hermes-build-state-shared`, orphan output volumes, and OCI artifact CAS without retention.
- Record exact commands, measured sizes/counts, and repository file/line references in the worker receipt.
- Propose scoped cleanup commands and minimal fixes without executing them.

## Boundaries

- No product, documentation, or configuration behavior changes.
- Preserve the existing dirty worktree; do not reset, stash, checkout, format, or rewrite unrelated files.
- Docker is read-only: do not run `prune`, `rm`, `delete`, `stop`, `start`, `restart`, or mutate contexts. Do not stop or restart Docker Desktop.
- Compare explicit `desktop-linux` and `default` context observations without changing the active context. Record the API 500 as an environment boundary, not an integration result.
- Do not expose tokens, credentials, browser profiles, transcripts, or real personal data in the receipt.
- Do not claim integration success from mock tests.

## Verification

- Worker receipt exists at `.herdr/runs/chg-0031-docker-disk-diagnosis/receipts/t1.worker.md`.
- Receipt includes read-only command evidence and code references for each requested cause category, plus uncertainty where Docker metadata is unavailable.
- Every proposed cleanup command is scoped, reversible where practical, and explicitly marked as requiring owner confirmation; none is executed.
- The repository diff remains limited to the coordination plan/state and worker receipt.
