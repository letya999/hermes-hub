# CHG-0013 Dependency refresh

## Scope

Apply the reviewed Dependabot updates from pull requests #1-#7 for GitHub
Actions and the pinned Docker base images.

## Acceptance

- All seven reviewed patches are represented in the worktree.
- `just check` and `just security` pass.
- Docker validation is run when the local Docker runtime is available.
- The corresponding superseded pull requests are closed with an audit note.
