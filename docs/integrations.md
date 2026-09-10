---
description: Connector contracts, scope rules and source pins.
last_verified: 2026-09-10
---
# Integration contracts

| Component | Source / pin | Contract |
|---|---|---|
| Hermes | [nousresearch/hermes-agent](https://github.com/nousresearch/hermes-agent), `869228cab4a8276d3b4c78da9d9939670c47bd0f` | CLI, gateway, config.yaml, MCP, Meet plugin |
| Telegram user | [chigwell/telegram-mcp](https://github.com/chigwell/telegram-mcp), `c9460f8ded6e2457bd70ebabfad840b58d23645d` | Python stdio; TELEGRAM_EXPOSED_TOOLS server allowlist |
| Google | [taylorwilsdon/google_workspace_mcp](https://github.com/taylorwilsdon/google_workspace_mcp), `54b1c56f7f9912ce32681460d7ca38f9c2a37564` | stdio single-user, selected extended tools, persisted OAuth |
| Slack | [korotovsky/slack-mcp-server](https://github.com/korotovsky/slack-mcp-server), `b88c0de3f706f4f07337c9eda7133c736d1c9524` | stdio, OAuth user token, channel posting allowlist |
| Playwright | [microsoft/playwright-mcp](https://github.com/microsoft/playwright-mcp), npm `@playwright/mcp@0.0.80` | stdio, persistent Chromium over local CDP |
| GitHub | [official MCP](https://github.com/github/github-mcp-server) | https://api.githubcopilot.com/mcp/ + bearer |
| Atlassian | [official Rovo MCP](https://support.atlassian.com/atlassian-rovo-mcp-server/) | https://mcp.atlassian.com/v2/mcp + OAuth |
| HH | [official API](https://api.hh.ru/openapi/redoc) | GET /vacancies, /vacancies/{id}, /resumes/mine; POST /negotiations |
| Memory Bank | [letya999/memory_bank_setup](https://github.com/letya999/memory_bank_setup), `2eb4e41968b86dae4c192dd9f9cff5b72c9d754f` | Documentation separation and change-folder conventions |

Telegram has two independent paths: Hermes `telegram` is the Bot API input gateway,
while `telegram_user` is the personal-account MCP. Enabling one does not enable the
other. Telegram is installed from its pinned Git repository: the unrelated PyPI package named
telegram-mcp is deliberately not used. Each Python MCP has a separate locked virtualenv
because Telegram and Hermes depend on incompatible MCP major versions. Upstream lockfiles
are honored. The Meet browser supplement has a separately pinned requirements lock.
APT system packages follow the pinned Debian release's repositories and security updates;
this is not a bit-for-bit reproducible OS build.

## Organization and user MCP connections

Standalone users may define `mcp_servers` in their own settings. In organization
scope, only the host-owned `organizations/<org>/settings.yaml` can define MCP servers;
the user can only narrow the approved set with `disabled_mcp`. Every organization MCP
must be listed in `read_only_mcp` and have a non-empty `tools.include` allowlist. The
allowlist is passed to Hermes, so shared organization credentials are reserved for
explicitly selected read-only tools. Write-capable servers need their own upstream
permissions and should not be granted as a shared organization connection. Built-in
hub-owned mutations are separately gated by `org_actions`.

## External MCP connections

Add user-owned connections in settings.yaml, then add secrets independently to the
selected secrets.dev.env or secrets.prod.env. Example external service:

```yaml
mcp_servers:
  career:
    url: https://your-career-host.example/mcp
    headers:
      Authorization: Bearer ${CAREER_TOKEN}
  local_tool:
    command: node
    args: [/workspace/tools/server.cjs]
    env:
      TOOL_TOKEN: ${TOOL_TOKEN}
```

These are examples, not preconfigured endpoints. Remote MCP needs an actual MCP server;
an arbitrary REST API is not automatically MCP. StdIO executables and their dependencies
must exist inside the agent image or selected workspace. No automatic unpinned package
installation is performed. `doctor` detects missing referenced credentials.
Native memory, skills and hooks are described in SETUP.md; user configuration passes
through to Hermes without replacing its extension system.
Connector credentials can also be supplied by an explicit owner `KEY=value` message to
the hub `env_update` tool. It persists a constrained user-runtime overlay and restarts
the supervisor; it is not a host `.env` editor and cannot alter organization-owned keys.

HeadHunter application requests use form fields vacancy_id/resume_id/message. Only HTTP
201 marks `sent: true`; receipt Location is preserved. A timeout may mean the send
succeeded; inspect negotiations before retrying. OAuth grants, already-applied errors,
required tests and CAPTCHA remain platform responsibilities.

Native bridges forward MCP tools and their schemas; they do not forward resources,
prompts, sampling or elicitation. A native MCP requiring those features is not compatible
with this limited bridge. Remote providers are maintained upstream and can change their
contracts independently; verify them after upgrades.
