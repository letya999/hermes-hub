---
description: Hermes runtimes with organization policy overlays and user isolation.
last_verified: 2026-09-10
---
# Architecture

Hermes owns reasoning, chat, tool selection, memory, skills, hooks and cron. Go owns
space initialization, strict configuration, organization policy resolution, Docker
lifecycle, bounded file/HH MCP and an optional native MCP bridge. A small Go binary
supervises container processes. There is no replacement agent loop, CRM, crawler, queue
or vector database. Retrieval is search -> read -> reason using files and tools.

## User namespace

Each user has **spaces/<user>** with one settings.yaml, SOUL.md, secrets.dev.env,
secrets.prod.env and a read-only archive directory. An optional `organization` in the
user settings resolves to **organizations/<org>** and is checked against its members
before rendering. The effective runtime is the user settings narrowed by the
organization allowlist; the user cannot add organization features, MCP servers or
actions. Render emits hermes.dev.yaml, hermes.prod.yaml and the corresponding Compose
files. Dev/prod is a Docker selection, not part of the user path. Secrets are never
copied from one env file to the other.

The Compose project name includes user and environment. Each project owns independent
state/workspace named Docker volumes. Two people or two runtimes never share browser
cookies, OAuth sessions, memory or generated skills. One active agent owns each home;
CLI chat is refused while that space is configured for a Telegram gateway.

| Location | Data |
|---|---|
| spaces/<user>/settings.yaml | Features, model endpoint, MCP connections and hook configuration |
| spaces/<user>/secrets.dev.env / secrets.prod.env | Independently entered credentials |
| spaces/<user>/SOUL.md | Owner-authored persistent assistant instructions |
| /state/hermes in a runtime volume | Config, sessions, memories, skills, hooks, plugins and Meet artifacts |
| /state/google, /state/telegram, /state/browser | Connector state and personal browser session |
| /state/cache | Speech models and caches |
| /state/self-env.json | User-owned connector env overlay, loaded before Hermes restart |
| /workspace in a separate runtime volume | Working documents, Markdown drafts and outputs |
| /archive | User-owned host archive, mounted read-only |
| /org | Organization `docs/`, mounted read-only into that user's runtime |

Organization credentials are loaded as a separate Compose env file before user
credentials. They are not copied into the user space. Organization MCP definitions are
host-owned and inherited into each approved member runtime. Each must declare a non-empty
`tools.include` allowlist and appear in `read_only_mcp`; user settings may only list
approved servers under `disabled_mcp` to narrow access. Generic upstream MCP servers
still enforce their own tool-level permissions; this hub's explicit action gates cover
hub-owned HH, Telegram and Slack mutations.

## Docker boundaries

The multi-stage Dockerfile has explicit prod and dev targets. Prod has no Go compiler
or source bind mounts and uses a read-only root filesystem. Dev adds Go, writable
container scratch and an allowlist of source mounts under /src. It does not mount the
project root, spaces, secrets or the Docker socket. The host editor changes mounted
source; Docker-host permissions still govern in-container edits. Unmounted build/test
outputs live in the disposable dev layer or /state caches.

Both targets run the same pinned Hermes/connector versions as UID 10001, drop capabilities,
and persist personal runtime data in named volumes. A short preparation container uses
CHOWN/DAC_OVERRIDE/FOWNER only to initialize those volumes. Browser desktop and Google
OAuth ports are published on host loopback. Use SSH forwarding from a VPS.

Hermes terminal can read credentials in its own user runtime: this is an isolated
organization/user deployment boundary, not a public hostile-tenant service. A user's
OAuth and Hermes state stay in that user's named volumes. Organization documents are
read-only. Organization actions are explicit strings in host policy and are passed to
hub-owned tools, not exposed as free `tenant`, `org` or `user` tool arguments.
The hub `env_update` tool writes only the user runtime's `self-env.json`; the supervisor
loads it on startup and restarts after an explicit owner update. Its allowlist comes
from configured connector references, and organization-owned keys plus runtime-control
keys are rejected. It cannot edit host env files, settings or organization secrets.
Chromium uses --no-sandbox within the container boundary. A native companion runs with
its OS user's permissions and needs a private tunnel. Public authentication and request
routing are outside this repository; an ingress must authenticate a principal and map
it to exactly one user space before invoking `hubctl`.

## Connections

Built-ins are opt-in connection presets. settings.yaml's mcp_servers adds ordinary
HTTP or stdio servers without embedding their applications. CareerGo or any other
external tool is used only if configured. The agent works without any business service.
Remote hosted MCP APIs may change independently of this code.

File writes use cross-process locks, hash preconditions and atomic rename. Search/read
are bounded UTF-8 operations. HeadHunter applications use an existing applicant resume,
explicit task authorization and actual HTTP receipts; unknown outcomes are not retried.
