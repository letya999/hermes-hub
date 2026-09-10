---
description: Peer organization/user homes and the private runtime boundary.
last_verified: 2026-09-10
---
# Architecture

Hermes owns reasoning, chat, tool selection, memory, skills, hooks and cron. Go owns
scope validation, configuration, lifecycle, communication routing, migration and the
bounded hub tools. There is no replacement agent loop, embedded business service,
database queue or vector database.

## Scope homes

Both organizations and users live at `spaces/<id>`. `scope.yaml` declares the kind and
stable ID; IDs are unique across the namespace. A user may reference one organization.
The effective user scope is the organization policy baseline plus the user's narrowed
settings. A direct conversation creates `user:<id>` state. Organization jobs require
the trusted `organization:<id>` scope and an approved organization action.

| Location | Data |
|---|---|
| `spaces/<id>/scope.yaml` | Scope kind, ID and organization membership/policy |
| `spaces/<user>/settings.yaml` | User features, model endpoint, MCP and hooks |
| `spaces/<id>/hermes/` | Hermes memories, skills, sessions, hooks, plugins and state |
| `spaces/<id>/connections/` | OAuth, browser, Telegram and self-service state |
| `spaces/<id>/workspace/` | User or organization work files |
| `spaces/<id>/archive/` | Read-only extracted archive |
| `spaces/<id>/generated/` | Ignored Compose, Hermes and filtered env files |
| `communication-hub-data` | Gateway queue and delivery ledger; outside scope homes |

Organization skills are configured through upstream Hermes `skills.external_dirs` and
are mounted read-only. Hub tools expose organization material with explicit provenance;
user writes remain in the user home. Duplicate organization/user secret keys are
rejected rather than overridden.

## Service boundary

`communication-hub` owns Telegram transport, external identity mapping, the bounded
file queue and reply delivery. It mounts only `communication-hub-data` and receives
the bot token. `hermes-runtime` owns the selected scope homes, provider credentials,
configuration and one Hermes process per job. It is not published on a host port.

The services use a small authenticated private HTTP contract: `POST /v1/jobs` carries
version 1, immutable identity fields, scope and a bounded prompt; `DELETE
/v1/jobs/<id>` requests cancellation. A Bearer runtime token is required. The runtime
is bound to one user and optional organization, rejects identity mismatches before
opening a path, and records idempotency outcomes as succeeded, failed or uncertain.
An uncertain result is never silently retried.

Prod has a read-only root and no source or Go compiler mount. Dev adds only the listed
source paths under `/src` and uses the same scope bind mounts. No project root,
other user's space or Docker socket is mounted into the runtime.

## Migration and compatibility

`hubctl migrate-spaces` is dry-run by default. `--apply` inventories, checks symlinks,
stages and verifies checksums/counts, then activates new homes while leaving source
directories and volumes in place for rollback. Startup never migrates implicitly.
The old `--dir`, `--org-dir` and `hub-communication` names remain compatibility
aliases for one release; generated deployments use the new service name.

Public authentication and ingress routing are outside this repository. The caller must
map an authenticated principal to one user before creating a runtime job. Provider
content cannot select a scope or authorize a mutation.

## Connections

Built-ins are opt-in. GitLab uses the runtime's `glab` CLI; Atlassian uses its official
Rovo MCP; Google is read-only unless explicitly enabled. Remote MCP APIs remain
external and must be configured by the owner. Live provider and Telegram acceptance
are separate from local and mock tests.
