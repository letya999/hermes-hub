# hermes-hub

[Быстрый старт на русском](START_HERE.ru.md)

A personal AI agent you run yourself. One binary, one Docker Compose stack, and your
agent lives in Telegram — chatting, searching the web, working with your files,
remembering things, and connecting to the services you choose. Built on the upstream
[Hermes](https://github.com/nousresearch/hermes-agent) CLI with a Go control plane
around it. No embedded CRM, vacancy collector or business service you didn't ask for.

## What you actually get

- **An agent in your chat.** Telegram first (Slack exists too). Messages, voice
  notes, files in, files out — documents, images, videos are delivered back to the
  conversation automatically.
- **Skills the agent can learn.** Install a skill from a GitHub/GitLab URL, a ZIP,
  a folder or a single `SKILL.md`. Every install lands as a reviewable draft —
  nothing goes live until you approve a card. Org admins can publish skills to a
  whole team; collisions are hidden, never silently merged.
- **Connections on your terms.** MCP servers, Google Workspace, GitHub, GitLab,
  Slack, Notion, HeadHunter, SSH — each opt-in, each scoped. Secrets go through a
  credential broker: the agent sees a form link, the value leases into the exact
  call that needs it and never reaches chat, logs or audit.
- **Memory that survives restarts.** Per-user persistent homes under `spaces/<id>` —
  agent config, workspace, archive, skill store. Restart the stack, everything is
  where it was.
- **More than one agent if you want.** Multiple users on one host, each isolated.
  Organizations get their own policy, approved connectors and shared skills that
  members can't widen — only narrow.

## Quick start

Requires Docker Engine/Desktop with Compose 2.30+. Grab a platform binary from the
latest release; Go is only needed to build from source. Linux example:

```bash
./bin/hubctl-linux-amd64 init --user artem
# Edit spaces/artem/agent.yaml and spaces/artem/secrets.prod.env locally.
./bin/hubctl-linux-amd64 doctor --user artem
./bin/hubctl-linux-amd64 up --user artem --env prod
./bin/hubctl-linux-amd64 chat --user artem
```

Set your model ID, an OpenAI-compatible `model_url` ending in `/v1`, and
`OPENAI_API_KEY`. CLIProxy works as that endpoint; use `host.docker.internal` for a
provider on the Docker host. On Windows use the included `.exe` and Docker Desktop
Linux containers. Start with 4 vCPU, 8 GB RAM, 30 GB free disk — the first build
downloads pinned upstreams and the agent image is bigger than the small Go CLI.

Dev/prod is a Docker target + env file selection, not a second namespace. Dev mounts
source at `/src` with a writable layer; prod runs read-only root.

## How it fits together

```
Telegram ──> communication-hub (gateway, queues, cards)
                  │  durable job
                  ▼
            supervisor ──> spawns the per-user runtime on demand
                  │        (Hermes CLI in its own container)
                  ▼
            toolhub / workload-controller / credential-broker
               tools admitted per call, secrets leased per call
```

The gateway owns the conversation; the supervisor starts the right user's runtime,
keeps warm leases for 30 minutes and shuts idle compute down. ToolHub decides which
tools the model may see *for this principal*; the workload-controller runs bounded
cells (git installs, skill scripts) with egress allowlists; the broker hands out
credentials per call instead of baking them into environments.

## Organizations

Users and orgs share one namespace: `spaces/<id>`. An org home carries membership,
approved features/MCP servers, shared material and policy — members can deny entries
in their own `tools:` but never expand past the org ceiling.

```bash
./bin/hubctl-linux-amd64 org-init --org acme --user artem
./bin/hubctl-linux-amd64 init --org acme --user artem
./bin/hubctl-linux-amd64 org sync-members --org acme --org-dir spaces/acme
```

Members get org skills delivered into their catalog (`source: org`), org-approved
connectors, and the org's policies. Old layouts stay put until an explicit verified
migration (`migrate-spaces --apply`).

## Capability map

| Capability | Implementation |
|---|---|
| Agent core | Upstream Hermes CLI, durable job queue, cron, files, terminal |
| Skills | Unified store (drafts → approval → publish), git/ZIP/markdown/dir sources, secrets + settings via broker, per-org delivery, usage telemetry |
| Personal Telegram | Pinned Telegram MCP, server-enforced read-only default — not the bot transport |
| Google Workspace | Read-only Calendar/Drive/Gmail/Docs/Sheets/Tasks over OAuth; writes opt-in |
| Web / research | `web_search`/`web_extract` + parallel `web_search_multi`; `deep-research-embedded` skill for bounded long runs |
| Browser / LinkedIn | Persistent Chromium + Playwright MCP |
| HeadHunter | Official vacancy/resume/application API |
| Slack / GitHub / Atlassian | OAuth/token MCP; Atlassian covers Jira + Confluence |
| GitLab | Bundled `glab` CLI with PAT |
| SSH | Opt-in: pinned host keys, alias-bound destinations, allowlisted commands |
| Documents / images / video | Hub tools for txt/md/csv/html/pdf/xlsx/pptx; image inspect/convert; opt-in `image_gen` + video via `hub-media` sidecar |
| Voice | `hub-stt` (faster-whisper) + `hub-tts` (espeak) sidecars; remote OpenAI-compatible engines supported |
| Other | Arbitrary configured stdio/HTTP MCP — external services stay external |

The frozen capability contract: [SPEC-0040](specs/active/SPEC-0040-capability-profile.md),
[SPEC-0041](specs/active/SPEC-0041-default-deny-capabilities.md),
[SPEC-0042](specs/active/SPEC-0042-capability-isolation-and-proof.md).
The skills platform contract: [SPEC-0084](specs/active/SPEC-0084-agent-skills-platform.md).

## Honest status

Done and acceptance-verified on the live dev stack: multi-user gateway, supervisor
scheduling, the full skills platform (M24), media services, connector platform,
i18n. In flight or pending live legs: managed hosting delivery (M18), self-evolving
skills (deferred post-MVP). The [changelog](CHANGELOG.md) and
[milestones](https://github.com/letya999/hermes-hub/milestones) carry the record;
[.work/](.work) CHG records say what was proven versus what was assumed.

## Engineering

`just check` is the single local CI entrypoint: Go race tests, ≥85% coverage gate
on original code, static analysis, docs/workflow checks, CLI integration tests.
For fast iteration on a running dev stack:

```bash
go run ./cmd/hubctl dev-rebuild --dir spaces/<user> --env dev <service>
```

GitHub Actions runs the same gate plus Docker builds. Never point CI at a personal
agent VPS. See [CONTRIBUTING](CONTRIBUTING.md).

- [SETUP](SETUP.md) — account bootstrap, VPS, native tools
- [Architecture](docs/architecture.md) · [Operations](docs/operations.md) · [Validation](docs/validation.md)
- [Memory Bank](docs/index.md) — rules / decisions / frozen specs / plans
- [LICENSE](LICENSE) — AGPL-3.0-only for original code; upstream licenses separate
