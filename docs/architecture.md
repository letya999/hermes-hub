---
description: Peer organization/user homes and the private runtime boundary.
last_verified: 2026-09-10
---
# Architecture

The current implementation below remains the AS IS architecture. The accepted target
for the next change is [ADR-0009](adr/ADR-0009-scoped-homes-and-communication-hub.md):
organization and user homes become peers under `spaces/`, mutable Hermes data moves
into those homes, and `communication-hub` becomes a separate container. The executable
and paths described below retain their current names until CHG-0009 is implemented and
an explicit migration succeeds.

Hermes owns reasoning, chat, tool selection, memory, skills, hooks and cron. Go owns
space initialization, strict configuration, organization policy resolution, Docker
lifecycle, communication routing, bounded file/HH MCP and an optional native MCP bridge.
A small Go binary supervises container processes. There is no replacement agent loop,
CRM, crawler, database queue or vector database. Retrieval is search -> read -> reason
using files and tools.

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

Gateway mode supervises `hub-communication`. Its channel adapters map an authenticated
channel sender (Telegram in v1) to the configured user scope, persist jobs and replies in
`/state/gateway`, and run one bounded Hermes invocation at a time. The initial deployment
has one configured user; the job envelope and per-user paths keep the expansion point for
multiple users, organizations and channels explicit without adding external IAM today.

Hermes terminal can read credentials in its own user runtime: this is an isolated
organization/user deployment boundary, not a public hostile-tenant service. A user's
OAuth and Hermes state stay in that user's named volumes. Organization documents are
read-only. Organization actions are explicit strings in host policy and are passed to
hub-owned tools, not exposed as free `tenant`, `org` or `user` tool arguments.
The hub `env_update` tool writes only the user runtime's `self-env.json`; the supervisor
loads it on startup and restarts after an explicit owner update. `service_catalog` reports
connector status, and `service_enable` writes only the user runtime's `self-services.json`;
the supervisor merges selected upstream MCP definitions into the next Hermes config.
The allowlist comes from connector references, and organization-owned keys plus
runtime-control keys are rejected. These tools cannot edit host env files, settings or
organization secrets.
Chromium uses --no-sandbox within the container boundary. A native companion runs with
its OS user's permissions and needs a private tunnel. Public authentication and request
routing are outside this repository; an ingress must authenticate a principal and map
it to exactly one user space before invoking `hubctl`.

## Connections

Built-ins are opt-in connection presets. settings.yaml's mcp_servers adds ordinary
HTTP or stdio servers without embedding their applications. CareerGo or any other
external tool is used only if configured. The agent works without any business service.
Remote hosted MCP APIs may change independently of this code.
GitLab uses the runtime's `glab` CLI rather than MCP; `GITLAB_TOKEN` and
`GITLAB_HOST` are supplied to the isolated runtime. Atlassian uses the pinned
`mcp-atlassian` package installed in the image and launched as a local stdio process;
`JIRA_URL`, `JIRA_USERNAME` and `JIRA_API_TOKEN` are passed only to that process.

File writes use cross-process locks, hash preconditions and atomic rename. Search/read
are bounded UTF-8 operations. HeadHunter applications use an existing applicant resume,
explicit task authorization and actual HTTP receipts; unknown outcomes are not retried.
