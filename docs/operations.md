---
description: Environment selection, migration, backup and recovery operations.
last_verified: 2026-09-10
---
# Operations

Use `hubctl --user <id> --env dev|prod`; prod is the default. `--dir` remains an
explicit legacy alias. `render` writes generated files under
`spaces/<user>/generated/`, `up` builds and starts the selected runtime, `down` stops
it without deleting data, and `logs` tails the selected Compose project.

When Telegram is enabled, Compose runs two private services. `communication-hub` maps
numeric sender IDs, owns the bounded queue under `communication-hub-data` and delivers
replies. `hermes-runtime` receives authenticated jobs and starts one bounded Hermes
process using the resolved scope home. The runtime has no host-published HTTP port and
does not receive the bot token or identity mapping.

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
