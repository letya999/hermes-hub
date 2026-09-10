---
description: Environment selection, migration, backup and recovery operations.
last_verified: 2026-09-10
---
# Operations

Use `hubctl --user <id> --env dev|prod`; prod is the default. `--dir` remains an
explicit legacy alias. `render` writes generated files under
`spaces/<user>/generated/`, `up` builds and starts the selected runtime, `down` stops
it without deleting data, and `logs` tails the selected Compose project.

When Telegram is enabled, gateway mode supervises `hub-communication`. It maps numeric
sender IDs from the configured allowlist to the selected user scope, writes durable jobs
and replies under `/state/gateway`, and runs one fresh bounded Hermes process per job.
The current deployment uses one configured user; adding another is a configuration and
isolated-space operation, not a shared Hermes home.

Change settings, then run up. The runtime copies the generated Hermes config at startup;
existing memory and installed skills/hooks persist. SOUL.md initializes a new runtime's
instructions; edits made inside an established Hermes home remain there. To replace it,
explicitly copy the owner's new SOUL into that selected container and restart.
An explicit owner `KEY=value` message can use the hub `env_update` tool. It persists a
user-only `/state/self-env.json` overlay and asks the supervisor to restart; it does not
rewrite `spaces/<user>/secrets.*.env` or organization secrets. Remove/reset the overlay
when changing back to host-managed credentials.
Never run hermes update in prod: update reviewed source pins and rebuild instead.

The selected user home contains persistent Hermes, connection, workspace and archive
data. An organization home is mounted read-only for members. Dev/prod selects separate
generated env files and Docker targets but does not create another user namespace. Never
copy production secrets or memories into dev.

Existing installations are migrated only explicitly:

```text
hubctl migrate-spaces --user artem --org acme       # dry-run
hubctl migrate-spaces --user artem --org acme --apply
```

The command rejects symlinks, ID/kind collisions, unrelated destination data and an
active runtime. It stages and verifies files, leaves source directories and volumes in
place, and prints a JSON report with paths, counts and checksums but no secret values,
message text, cookies or session contents. Rollback is restoring the untouched source
into a new scope home; do not delete it until the new runtime is verified.

Back up all `spaces/<id>` homes and the `communication-hub-data` volume to encrypted
storage while services are stopped. Do not use `docker compose down --volumes` unless
deleting that user's data intentionally. Restore organization and user homes separately;
never merge memories, Telegram sessions or provider credentials.

Health checks prove process liveness and, where enabled, Chromium CDP readiness. They do
not prove OAuth, model, Telegram or provider access. Live acceptance requires the
operator's separate login and provider instructions.

Self-service `env_update` and `service_enable` persist only under the selected user
connection state and request a supervisor restart. They cannot edit organization policy,
host settings or runtime-control variables. Provider content cannot authorize any
mutation.
