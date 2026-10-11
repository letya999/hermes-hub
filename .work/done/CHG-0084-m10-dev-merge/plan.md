# M10 merge and build completion

Merge M10 A–C plus stabilization into the main worktree's dev branch, preserve
newer CLI/CI behavior and rebuild the configured local Docker images.

The media-enabled profile retained a second build declaration for the same core
tag. Remove that declaration; the media service consumes the image built by
cliproxy. A regression must cover both dev and prod and reject any second service
that builds the shared tag. Keep media credentials, mounts and runtime behavior.

Validation: full just check (coverage >=85%), just security, secret scan,
hubctl BuildKit build, isolated runtime smoke and control CLI check.
No publication, shared volume pruning or automatic deployment restart.
