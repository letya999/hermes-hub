---
status: active
title: Scoped homes and communication-hub service boundary
---

# Goal

Make `spaces/` the visible and backupable home of every Hermes scope. A member's
runtime receives the organization scope as the shared baseline and the user scope as
the personal layer. Move channel transport into a separately deployed
`communication-hub` service and keep Hermes execution in the runtime service.

# Scope model

1. Organization and user homes are peers under `spaces/`. Every space declares its
   `kind` (`organization` or `user`) and stable ID; IDs are unique across the whole
   `spaces/` namespace.
2. A user space declares at most one organization ID. Membership is validated from
   the organization space before a job is accepted or a runtime is started.
3. A member's effective scope is `organization baseline + user overlay`. Organization
   policy limits the available features, MCP servers and actions. The user may narrow
   those permissions and may override personal presentation or preference files.
4. Every persisted object has one owner: organization or user. Organization data is
   available to members; user data is available only to that user. A user-owned object
   is never copied into the organization space as part of normal execution.
5. Direct messages create user-scoped sessions by default. An organization-scoped job
   must carry `scope_id: organization:<org>` from the trusted control plane. Prompts,
   provider content and Hermes tools cannot select or change `scope_id`.

# Directory contract

For a user `artem` in organization `acme`, the target host layout is:

```text
spaces/
├── acme/
│   ├── scope.yaml                  # kind: organization; id: acme; members and policy
│   ├── SOUL.md                     # common organization instructions
│   ├── secrets.dev.env
│   ├── secrets.prod.env
│   ├── hermes/
│   │   ├── memories/
│   │   ├── skills/
│   │   ├── sessions/
│   │   ├── hooks/
│   │   ├── plugins/
│   │   └── state.db
│   ├── connections/
│   ├── workspace/
│   └── archive/
└── artem/
    ├── scope.yaml                  # kind: user; id: artem; organization: acme
    ├── settings.yaml
    ├── SOUL.md
    ├── secrets.dev.env
    ├── secrets.prod.env
    ├── hermes/
    │   ├── memories/
    │   ├── skills/
    │   ├── sessions/
    │   ├── hooks/
    │   ├── plugins/
    │   └── state.db
    ├── connections/
    │   ├── google/
    │   ├── telegram/
    │   └── browser/
    ├── workspace/
    └── archive/
```

Generated Compose and Hermes configuration belongs under an ignored `generated/`
directory inside the owning space. Gateway jobs, replies and delivery ledger belong
to a separate `communication-hub-data/` volume and never to either scope home.

# Hermes resolution contract

1. A user-scoped job starts Hermes with `spaces/<user>/hermes` as `HERMES_HOME`,
   `spaces/<user>/workspace` as its working directory and the user's connection state.
2. An organization-scoped job starts Hermes with `spaces/<org>/hermes` as
   `HERMES_HOME` and `spaces/<org>/workspace` as its working directory.
3. Organization skills are exposed to a member through upstream Hermes
   `skills.external_dirs`; a same-named user skill takes precedence in a user-scoped
   job. The hub must not patch upstream Hermes to implement this overlay.
4. Organization SOUL, memory and session material is exposed with explicit
   organization provenance. User-scoped writes go to the user home. Organization
   writes require an organization-scoped job and the corresponding organization
   action grant.
5. Secrets are merged only into the selected runtime process. Organization secrets
   are the baseline; duplicate keys in the user secret file are rejected rather than
   silently overriding an organization-owned credential.
6. One Hermes process handles one job. Its files persist in the selected space after
   the process exits.

# Service contract

1. The service, executable and Go package are named `communication-hub`. It owns
   channel adapters, external identity mapping, job creation, the file queue, reply
   delivery and the Telegram bot token.
2. `hermes-runtime` owns Hermes process creation, timeout/cancellation, scoped paths,
   effective config, connector environment and the result returned for a job.
3. `communication-hub` and `hermes-runtime` are separate containers on a private
   Docker network. The runtime is not published on a host port.
4. A runtime instance is bound to one user space and its optional organization space.
   It rejects jobs whose resolved user or organization does not match that binding.
5. The communication service never mounts user or organization homes and never
   receives provider credentials. The runtime never receives the Telegram bot token
   or identity mapping.
6. Keep the existing bounded file queue for the single-host, single-worker release.
   Do not add PostgreSQL, Redis or a general queue framework.

# Migration contract

1. `hubctl migrate-spaces` performs a dry run by default and requires an explicit
   `--apply` to mutate data.
2. Migration stops the selected runtime, copies current configuration and persistent
   volume contents into a staging directory, verifies required files and counts, then
   atomically activates the new space. Existing volumes remain untouched until the
   operator separately removes them.
3. Current `organizations/<org>` becomes `spaces/<org>` and gains
   `scope.yaml`. Current `spaces/<user>` remains the user path and gains its persisted
   `hermes/`, `connections/` and `workspace/` data.
4. The migration refuses symlinks, a destination that already contains unrelated
   data, ID collisions between organization and user spaces, running containers and
   partial source volumes.
5. A migration report contains paths, object counts and checksums, never secret
   values, message text, browser cookies or session contents.

# Acceptance criteria

1. Initializing `acme` and `artem` produces two peer homes under `spaces/` with
   different declared kinds; duplicate IDs and path traversal are rejected.
2. A user-scoped Hermes process can read approved organization skills and shared
   material, writes memory/session/workspace data only to `spaces/artem`, and cannot
   access another user's space.
3. An organization-scoped process writes only to `spaces/acme` and is rejected for a
   non-member or without the required organization action.
4. The rendered deployment contains separate `communication-hub` and
   `hermes-runtime` services. Only the runtime mounts `spaces/acme` and
   `spaces/artem`; only the communication service receives the bot token and gateway
   data volume.
5. Job replay, concurrent path selection and runtime identity mismatch cannot cross
   scope boundaries. Add regression tests for all three cases.
6. A dry-run and applied migration are tested with representative state, workspace,
   skills, sessions and connection directories. Rollback remains possible from the
   untouched source volumes.
7. `just check`, `just security` and `just docker-check` pass. Live Telegram and
   provider acceptance remain separate and cannot be claimed from mocks.

# Compatibility and rollout

The old CLI flags remain aliases for one release, with a deprecation message that
prints the resolved new path. The old executable name may remain as a packaging alias
for one release; documentation and generated deployments use `communication-hub`.
Existing installations continue on the old layout until `migrate-spaces --apply`
succeeds; startup never performs an implicit migration.

# Open implementation point

Before implementing the container split, define and freeze the smallest private job
execution contract between `communication-hub` and `hermes-runtime`, including request
authentication, cancellation and uncertain-result handling. Do not guess or expose a
public API while making the split.
