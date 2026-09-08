---
description: Standalone Hermes runtime, user spaces and environment isolation.
last_verified: 2026-09-07
---
# Architecture

Hermes owns reasoning, chat, tool selection, memory, skills, hooks and cron. Go owns
space initialization, strict configuration, Docker lifecycle, bounded file/HH MCP and
an optional native MCP bridge. A small Go binary supervises container processes.
Just runs the local engineering gate. There is no replacement agent loop, CRM, crawler,
queue or vector database. Retrieval is search -> read -> reason using files and tools.

## User namespace

Each user has **spaces/<user>** with one settings.yaml, SOUL.md, secrets.dev.env,
secrets.prod.env and a read-only archive directory. Render emits hermes.dev.yaml,
hermes.prod.yaml and the corresponding Compose files. Dev/prod is a Docker selection,
not part of the user path. Secrets are never copied from one env file to the other.

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
| /workspace in a separate runtime volume | Working documents, Markdown drafts and outputs |
| /archive | Host-managed extracted archive, mounted read-only |

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

Hermes terminal can read credentials in its own runtime: this is a trusted owner-operated
agent, not hostile multi-tenant hosting. Chromium uses --no-sandbox within the container
boundary. A native companion runs with its OS user's permissions and needs a private tunnel.

## Connections

Built-ins are opt-in connection presets. settings.yaml's mcp_servers adds ordinary
HTTP or stdio servers without embedding their applications. CareerGo or any other
external tool is used only if configured. The agent works without any business service.
Remote hosted MCP APIs may change independently of this code.

File writes use cross-process locks, hash preconditions and atomic rename. Search/read
are bounded UTF-8 operations. HeadHunter applications use an existing applicant resume,
explicit task authorization and actual HTTP receipts; unknown outcomes are not retried.
