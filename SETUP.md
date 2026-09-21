# Connect your accounts

User secrets stay in `spaces/<user>/secrets.dev.env` or `secrets.prod.env`; organization
secrets stay in the matching `spaces/<org>/` home. Use literal **unquoted**
single-line `NAME=value` entries; dollar signs are not expanded. Never commit this
directory. `hubctl render` rewrites generated configs but preserves SOUL and secrets.
`doctor` validates configuration, not successful login or account entitlements.

## 1. LLM and deployment

Run `init`, set model/model_url/timezone in settings, then fill OPENAI_API_KEY.
Use `--env dev` for the dev Docker target and secrets.dev.env; prod is the default.
The initial features are `workspace`, `browser`, `hh`. For Telegram operation
add `telegram`; otherwise the container waits for `hubctl chat` sessions. `hubctl build`
forces BuildKit/Bake, builds the shared image once, and prunes dangling layers
left by the previous tag. It does not require every credential, so it is useful
before Telegram login. Do not set `DOCKER_BUILDKIT=0` in the operator environment.

For the prepared work-service bundle, add these settings once:

```yaml
features: [workspace, browser, hh, gitlab]
gitlab_host: gitlab.com
```

Fill the matching GitLab PAT. Google Workspace is the official ToolHub remote MCP
(`hubctl connector connect --provider google --official-mcp`), not a copy inside
the hub image. Atlassian is `docker/mcp-atlassian.Dockerfile`. Keep `google_write`
out unless Workspace mutations are needed.

Run commands from the extracted project root, or pass `--root /absolute/project` on
build/up/render. Use `hubctl logs` for runtime errors. Do not start simultaneous CLI
and gateway sessions that mutate the same Google OAuth session or browser context.

## 2. Telegram: bot and personal account are separate

For `telegram`, create a bot with Telegram's BotFather. Fill TELEGRAM_BOT_TOKEN and
TELEGRAM_ALLOWED_USERS with your numeric account ID (comma-separated owner IDs;
no wildcard). Send the bot `/start`, then your task. Bot polling needs no public port.
This is only the Hermes input transport; it does not let Hermes read your personal
Telegram chats.

For `slack_app`, create a Slack App with Events API. Fill `SLACK_SIGNING_SECRET`,
`SLACK_BOT_TOKEN` and `SLACK_ALLOWED_USERS` as `TEAMID/USERID` pairs (never email or
display name). Compose publishes loopback `slack_events_port` (default 8081, set in
`settings.yaml`) to communication-hub `:8081`. Point the app Request URL at
`http://127.0.0.1:<slack_events_port>/v1/slack/events` (tunnel if Slack Cloud must
reach this host). This is ingress/delivery only and does not enable Slack
search/read/write tools; those stay on the separate `slack` feature.

For `telegram_user`, obtain your API ID/hash at [my.telegram.org](https://my.telegram.org).
Fill TELEGRAM_API_ID and TELEGRAM_API_HASH, then run:

```bash
./bin/hubctl-linux-amd64 telegram-login --user me --root .
```

That command builds `docker/telegram-account.Dockerfile` (not the hub image)
and runs the upstream session-string generator.

Complete phone/2FA locally. Copy the generated session string into
TELEGRAM_SESSION_STRING; it grants account access and must be treated like a password.
Add `telegram_user` and restart with `up`. Default tools only read. Add `telegram_write`
only if you want Hermes to send/reply or save Telegram drafts under your account.
Enabling it makes these tools available; SOUL requires the owner's sending instruction.

## 2a. Updating connector credentials

Connector credentials are entered only through the one-time protected form that Hermes
returns after `service_enable` or ToolHub `required_credentials`. They are never sent as
`KEY=value` through Telegram or Hermes. The form writes only to the selected user's
runtime and schedules a restart; it expires after a short TTL and cannot be reused.
The initial bot token/model credential still has to be provisioned locally so the runtime
can receive the first message. If an old chat message contains `KEY=value`, it is deleted
best-effort and rejected; use the returned protected form instead.

The same Telegram bot can manage the connector lifecycle. Ask “какие сервисы доступны”
to receive the current catalog and statuses, then say “включи GitLab” (or another
self-service connector). Hermes returns the exact missing key names and a protected form
link. After submission, retry the enable request. Google may then return its official OAuth
consent URL; follow that URL in the browser. Values are never returned by the tools.
For an arbitrary MCP, the form is generated on demand from its resolved Connection Recipe:
field names, count and order are taken from that recipe, with the declared input type,
delivery target and OAuth alternatives. There is no pre-created universal credential form.
Browser, Meet, Telegram transport and native bridges remain host-managed and are reported
as such.

## 3. Google Workspace and archive

Create a Google Cloud OAuth client of type **Web application**. Enable Gmail, Drive,
Calendar, Docs, Sheets, Slides and Tasks APIs. Configure the consent screen and add
yourself as a test user if the app is in testing. Register exactly
`http://localhost:8000/oauth2callback` (substitute your configured oauth_port).
Prefer the official ToolHub Google Workspace remote MCP
(`hubctl connector connect --provider google --official-mcp --product gmail`
and the sibling products). The settings `google` feature still describes the
legacy third-party stdio server under `/opt/google`, which the default hub
image no longer ships.

If you restore that tree, fill GOOGLE_OAUTH_CLIENT_ID, GOOGLE_OAUTH_CLIENT_SECRET
and GOOGLE_EMAIL (or settings.google_email before rendering), add `google`,
start, and ask Hermes to list upcoming calendar events. Follow its OAuth URL
in your browser; tokens persist under `spaces/<user>/connections/google`.
Consent scopes are determined by the selected upstream tools. Google
testing-mode refresh tokens can expire. The `google` feature is read-only by
default. Add `google_write` only when this runtime needs to send mail, change
events/tasks, or modify Workspace files; individual mutations still require
an explicit owner request.

On a VPS establish the SSH tunnel below **before** OAuth. The callback binds to all
interfaces inside the container but is published only on host loopback. The browser
redirect uses localhost on your laptop, through that tunnel.

Gmail archive is searchable Gmail mail (for example, `in:all -in:inbox -in:trash`).
For a Google Takeout archive, extract it into `spaces/me/archive` on the host.
The file tools read UTF-8 text up to 2 MiB per file: split large mbox/JSON exports first.
Binary files need an appropriate reader. This does not implement Google Vault.

## 4. Browser, LinkedIn, Meet and speech

The browser remains internal to `hermes-runtime` and is not published on a host port.
Use an explicitly approved operator path for login. Log into LinkedIn/HH/other sites
yourself. Cookies persist in `spaces/<user>/connections/browser`.
No private LinkedIn API, bulk outreach or CAPTCHA bypass is provided.

For Meet add `meet`, run `hubctl up`, then `hubctl meet-auth`. Use noVNC to sign into
the newly opened Google browser, then press Enter in the terminal. This saves the
Meet plugin's separate auth state. Ask Hermes to join an exact meeting with participant
consent. Captions are the supported transcript source; attendance/admission and captions
must work in that meeting. `hermes meet setup` inside the agent reports readiness.
The default image no longer ships Playwright browsers; enabling Meet needs those
layers restored in `docker/Dockerfile` before `hubctl build`.

Add `transcription` to transcribe supported audio through local faster-whisper. The
small model downloads on first use. Telegram bot voice uses the same image via
`HUB_STT_COMMAND=/usr/local/bin/hub-stt` on communication-hub. Telegram MCP's own
cloud transcription is disabled.
The default image omits the Hermes `voice` extra; restore it in the Dockerfile
before relying on local STT.
Live Meet audio routing, speaker separation and guaranteed verbatim transcripts are
not configured; these differ from caption capture and file transcription.

## 5. HeadHunter

For HH public search, `hh` alone is enough. For personal resumes and applying, obtain
an applicant OAuth token using an approved HH application; set HH_TOKEN and an
identifying HH_USER_AGENT per HH requirements. The tool sends an **existing HH resume
ID**, not an uploaded arbitrary PDF. Tests/CAPTCHA/platform rules may block applying.
An explicit task may authorize the exact application without another confirmation;
the agent supplies `authorized: true` only for that task and records the returned receipt.

## 6. GitHub, GitLab, Slack, Atlassian

- `github`: GITHUB_TOKEN for the official remote MCP endpoint. Choose permissions for
  the repositories and operations you need. Account/organization policy still applies.
- GitLab uses the bundled `glab` CLI with a PAT. Enable `gitlab`, put `GITLAB_TOKEN`
  in the selected secrets file, and set `gitlab_host` only for a self-managed host
  (the default is `gitlab.com`). No `glab auth login` command is needed: `glab` reads
  the token from the runtime environment. Verify with `hubctl exec --user me --env prod
  -- glab auth status`, then use `glab repo view`, `glab issue list` and `glab mr list`.
  For repository and write operations, create the PAT with at least `api` and
  `write_repository` scopes. Keep the PAT out of settings, commands, Git remotes and
  logs; use a separate PAT for dev and prod when their access should differ.
  Creating/editing issues or merge requests, comments, merges and pipeline actions
  require an explicit owner request.
- `slack`: SLACK_MCP_XOXP_TOKEN from your authorized Slack OAuth app. User scopes must
  cover search/read on your accessible conversations. Leave SLACK_MCP_ADD_MESSAGE_TOOL
  empty to disable posting; set a comma-separated channel ID allowlist to opt in.
- `atlassian`: pinned [`mcp-atlassian`](https://github.com/sooperset/mcp-atlassian)
  in `docker/mcp-atlassian.Dockerfile`, not the default hub image. For Jira Cloud set
  `JIRA_URL`, `JIRA_USERNAME` and `JIRA_API_TOKEN`. This bypasses the Atlassian Rovo
  endpoint and its organization-level API-token switch. The upstream MCP also supports
  Confluence; the built-in preset currently configures Jira only. The settings
  `atlassian` feature still emits the legacy in-process stdio command; that
  binary is absent until the dedicated image is used as a ToolHub artifact.

## 7. Native Computer Use and Drafts

Install the upstream native MCP server on the computer whose desktop/app you want to
control, with its OS permissions. The container cannot control the host desktop by itself.
Use `hubctl companion --config companion.yaml` there. Example:

```yaml
listen: 127.0.0.1:8765
token_env: DESKTOP_TOKEN
command: [cua-driver, mcp]
allowed_tools: []
```

Set DESKTOP_TOKEN to a random value of at least 32 characters **on both ends**. A
companion accepts only its configured executable and proxies tools, not arbitrary
commands from HTTP. Empty allowed_tools exposes all tools discovered from that trusted
executable; provide exact tool names to narrow it. Resources/prompts are not forwarded.

For Drafts.app use a second bridge on 8766, DRAFTS_TOKEN, and the absolute command
`[node, /path/to/drafts-mcp-server/dist/index.js]`. This requires macOS and Drafts
50.0.3+; use the official [server](https://github.com/agiletortoise/drafts-mcp-server).
Windows/Linux still have ordinary Markdown drafts in workspace/drafts.

Connect bridges over a private VPN or SSH tunnel. A localhost listener is not reachable
from Docker's host gateway; for direct Docker access bind to the host's private bridge/VPN
interface and firewall it to the agent, then set desktop_url/drafts_url accordingly.
Do not publish an unencrypted bearer endpoint on the public internet. Native companion
binaries are included for Windows amd64, Linux amd64/arm64 and macOS arm64.

## Organization scope

Create the host-owned overlay once, then initialize each member's isolated user space:

```bash
./bin/hubctl-linux-amd64 org-init --org acme --user artem
./bin/hubctl-linux-amd64 init --org acme --user artem
```

Edit `spaces/acme/scope.yaml`:

```yaml
schema: 1
organization: acme
members:
  artem: owner
features: [workspace, browser, google, slack, atlassian]
mcp_servers: {}
read_only_mcp: []
org_actions: []
```

Add organization credentials to `spaces/acme/secrets.dev.env` or
`secrets.prod.env`; add personal OAuth/API credentials only to the member's matching
`spaces/<user>/secrets.*.env`. The organization file is loaded separately and is not
copied into the user space. A member may only remove an approved MCP with
`disabled_mcp`; they cannot add a feature, credential or MCP. Every organization MCP
must be listed in `read_only_mcp` and must use a non-empty `tools.include` allowlist;
the list is what Hermes exposes to the agent. Organization `docs/` is mounted as
read-only `/scope/org`. `org_actions` is empty by default; add an action only when
the organization explicitly permits that mutation. The repository does not provide a
public authentication gateway, so an external ingress must authenticate the principal
and select the corresponding user space before invoking `hubctl`.

Existing installations must be migrated explicitly; the command is dry-run by default:

```bash
./bin/hubctl-linux-amd64 migrate-spaces --user artem --org acme
./bin/hubctl-linux-amd64 migrate-spaces --user artem --org acme --apply
```

## VPS and another person

Install Docker with Compose 2.30+, copy the project, run init/setup/up on the VPS.
The runtime HTTP service is private to the Compose network and is not published.
Telegram uses outbound polling. Use an explicitly approved SSH/Docker operator path
for interactive browser or OAuth work; do not expose runtime ports publicly.
For another person run init with a different user space, separate secrets and
different base browser_port/oauth_port (reserve each base and base+1 for dev). Use their own bot/session and Google consent.
Each deployment is fully capable within its own container; this is not a hostile-tenant
hosting platform. On Windows use the .exe binary and Docker Desktop's Linux containers.

## Native skills, hooks and memory

These are upstream Hermes features, not parallel implementations. Their paths inside
each runtime are `<space>/hermes/skills`, `<space>/hermes/hooks`,
`<space>/hermes/plugins` and `<space>/hermes/memories`. MEMORY.md and USER.md are
enabled by default (memory: true). Sensitive facts belong in those memory files and
are never copied into another user's home or an organization runtime. Inspect with
`hubctl memory list --user me` and delete a file with `hubctl memory delete --name USER.md`.
Honcho is optional and uses the official `$HERMES_HOME/honcho.json` contract; leave it
unset so native memory keeps working if Honcho is absent.
Use `hubctl exec --user me -- hermes skills list`, `hermes hooks list`,
`hermes plugins list` or `hermes memory --help` through the same exec command.
Install skills with Hermes's own skill installer or `hubctl skill install --consent`;
review their code and permissions. User skills stay writable only in the user home.
Organization and global skills are curated read-only mounts.

Gateway hooks use a directory containing HOOK.yaml and handler.py. Plugin hooks run
in CLI and gateway. Shell hooks are passed from settings.yaml's hooks block unchanged.
See the pinned upstream [hook guide](https://github.com/nousresearch/hermes-agent/blob/869228cab4a8276d3b4c78da9d9939670c47bd0f/website/docs/user-guide/features/hooks.md).
Copy reviewed local extensions into the selected container using Docker cp, or install
through Hermes. No hooks/skills are silently installed with extra account permissions.

The browser supports normal internet browsing without a search-provider key. Native
web_search/web_extract can use optional FIRECRAWL_API_KEY or TAVILY_API_KEY from the
selected env file. Availability of a paid search provider is not implied by enabling browser.

For external MCP services, see docs/integrations.md. There is no local CareerGo or
JobFetch service to start, migrate or configure in this project.
