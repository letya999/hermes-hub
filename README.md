# hermes-hub

[Быстрый старт на русском](START_HERE.ru.md)

A standalone, general-purpose [Hermes](https://github.com/nousresearch/hermes-agent)
deployment with a Go control plane. Chat, internet research, personal files, memory,
skills, hooks and MCP connections. No embedded CRM, vacancy collector or required business service.

## Quick start

Requires Docker Engine/Desktop with Compose 2.30+. Choose a platform binary from the
release archive; Go is only needed when building from source. Linux example:

```bash
./bin/hubctl-linux-amd64 init --user artem
# Edit spaces/artem/settings.yaml and spaces/artem/secrets.prod.env locally.
./bin/hubctl-linux-amd64 doctor --user artem
./bin/hubctl-linux-amd64 up --user artem --env prod
./bin/hubctl-linux-amd64 chat --user artem
```

Set your model ID, an OpenAI-compatible model_url ending in /v1, and OPENAI_API_KEY.
CLIProxy API can be that endpoint. Use host.docker.internal for a provider on the Docker
host. For Windows use the included .exe and Docker Desktop Linux containers.
Start with 4 vCPU, 8 GB RAM and 30 GB free disk; browser/speech dependencies make the
agent image larger than the small Go CLI. The first build downloads pinned upstreams.

## Organizations, users and environments

The namespace is **spaces/<id>** for both users and organizations. Every home has a
`scope.yaml`; user homes also have `settings.yaml`, SOUL, secrets and persistent
`hermes/`, `connections/`, `workspace/` and `archive/` directories. An organization
home contains membership, approved features/MCP servers, shared material and policy.
The user can narrow that policy with `disabled_mcp`, but cannot expand it. Public
authentication/routing is not part of this local control plane: the caller must map
an authenticated principal to one user space before starting its runtime.

Dev/prod selects a Docker target and env file; it does not create another user
namespace. Generated files live under `spaces/<id>/generated/`. Runtime state is bind-
mounted from the selected homes; the communication queue is the separate
`communication-hub-data` volume. No automatic copying of tokens or memories between users.

```bash
./bin/hubctl-linux-amd64 org-init --org acme --user artem
./bin/hubctl-linux-amd64 init --org acme --user artem
./bin/hubctl-linux-amd64 up --user artem --env dev
./bin/hubctl-linux-amd64 init --user another-person
./bin/hubctl-linux-amd64 up --user another-person --env prod
```

Edit `spaces/acme/scope.yaml` to remove features, add approved read-only
`mcp_servers` with `tools.include` allowlists, and grant only the required
`org_actions` such as `slack.write` or `hh.apply`. Run
`up`, `render` and `doctor` with the same `--org`/`--user` selection. Organization
documents and skills are available to the runtime only through read-only `/scope/org`.
Existing installations stay on the old layout until an explicit, verified migration:

```bash
./bin/hubctl-linux-amd64 migrate-spaces --user artem --org acme
./bin/hubctl-linux-amd64 migrate-spaces --user artem --org acme --apply
```

Dev adds the Go toolchain and selected source mounts under /src for development. Prod has no project
source mount or Go compiler. Both use an unprivileged runtime and persistent data; the runtime
HTTP service is reachable only on the private Compose network.
Prod has a read-only root; dev has a writable disposable layer for tests/build outputs. Choose different base browser/OAuth ports for
another user; dev uses the next port. Docker Compose rejects accidental host-port collisions.

## Connections and capabilities

| Capability | Implementation |
|---|---|
| General agent | Upstream Hermes CLI, communication gateway with Telegram adapter, cron, files, terminal |
| Self-environment | Telegram-visible service catalog, explicit `KEY=value` updates and self-service connector enablement; per-runtime persistence and restart |
| Skills / hooks / memory | Native Hermes mechanisms persisted per user and runtime |
| Personal Telegram | Separate pinned Telegram MCP source, server-enforced read-only default; not the bot transport |
| Google Workspace | Read-only Calendar, Drive, Gmail, Docs, Sheets, Slides and Tasks over OAuth; writes opt-in |
| Internet / LinkedIn | Persistent Chromium + Playwright MCP; optional native search providers |
| HeadHunter | Official vacancy, personal resume and explicitly authorized application API |
| Slack / GitHub / Atlassian | OAuth/token MCP connections; Atlassian covers Jira and Confluence with personal API-token auth |
| GitLab | Bundled `glab` CLI with `GITLAB_TOKEN` PAT authentication |
| Meet / audio | Native Meet caption plugin and local faster-whisper |
| Native desktop / Drafts.app | Authenticated bridge to a trusted native stdio MCP server |
| Other services | Arbitrary configured stdio/HTTP MCP; external services remain external |

Default features are workspace, browser and hh. All account integrations are opt-in.
Hermes works without CareerGo and JobFetch. If you run an external MCP service, add
its URL and token reference under mcp_servers; see [connections](docs/integrations.md).
An explicit owner message containing connector `KEY=value` lines is intercepted
before Hermes. `hubctl secret set|list|delete` is the host path. Replies contain
key names and status, never values; groups reject credential entry.

## Engineering

`just check` is the single local CI entrypoint. It runs Go race tests, the >=85%
coverage gate for all original Go statements, static analysis, documentation/workflow
checks and CLI integration tests. Upstream source is not counted toward coverage.

GitHub Actions runs the same gate and builds/smoke-tests both Docker targets. A manual
workflow is included for a labelled self-hosted GitHub Runner. Never use a personal
agent VPS as a public pull-request runner. See [CONTRIBUTING](CONTRIBUTING.md).

- [SETUP](SETUP.md): account bootstrap, VPS and native tools.
- [Architecture](docs/architecture.md), [operations](docs/operations.md), [validation](docs/validation.md).
- [Memory Bank](docs/index.md): rules / decisions / frozen specifications / plans.
- [LICENSE](LICENSE): AGPL-3.0-only for original code; upstream licenses remain separate.

This is source plus compiled Go control/runtime tools. Docker execution and real account access
must be validated on the deployment host; see the delivery evidence before rollout.


Persistent communication rollout uses `hubctl execution-audit` and
`hubctl select-execution` per context. The supervised gateway sends durable jobs to
the host supervisor, which starts the exact user's Hermes home, holds active leases,
and gracefully shuts compute down after the default five-minute idle window.
See [operations](docs/operations.md) for drain, rollout, rollback and native cron prerequisites.
