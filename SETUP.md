# Connect your accounts

All secrets stay in `spaces/<user>/secrets.dev.env` or `secrets.prod.env`. Use literal **unquoted**
single-line `NAME=value` entries; dollar signs are not expanded. Never commit this
directory. `hubctl render` rewrites generated configs but preserves SOUL and secrets.
`doctor` validates configuration, not successful login or account entitlements.

## 1. LLM and deployment

Run `init`, set model/model_url/timezone in settings, then fill OPENAI_API_KEY.
Use `--env dev` for the dev Docker target and secrets.dev.env; prod is the default.
The initial features are `workspace`, `browser`, `hh`. For Telegram operation
add `telegram`; otherwise the container waits for `hubctl chat` sessions. `hubctl build`
builds/prepares without requiring every credential, useful before Telegram login.

Run commands from the extracted project root, or pass `--root /absolute/project` on
build/up/render. Use `hubctl logs` for runtime errors. Do not start simultaneous CLI
and gateway sessions that mutate the same Google OAuth session or browser context.

## 2. Telegram: bot and personal account are separate

For `telegram`, create a bot with Telegram's BotFather. Fill TELEGRAM_BOT_TOKEN and
TELEGRAM_ALLOWED_USERS with your numeric account ID (comma-separated owner IDs;
no wildcard). Send the bot `/start`, then your task. Bot polling needs no public port.
This is only the Hermes input transport; it does not let Hermes read your personal
Telegram chats.

For `telegram_user`, obtain your API ID/hash at [my.telegram.org](https://my.telegram.org).
Fill TELEGRAM_API_ID and TELEGRAM_API_HASH, build, then run:

```bash
./bin/hubctl-linux-amd64 telegram-login --user me
```

Complete phone/2FA locally. Copy the generated session string into
TELEGRAM_SESSION_STRING; it grants account access and must be treated like a password.
Add `telegram_user` and restart with `up`. Default tools only read. Add `telegram_write`
only if you want Hermes to send/reply or save Telegram drafts under your account.
Enabling it makes these tools available; SOUL requires the owner's sending instruction.

## 2a. Updating connector env from the owner chat

After the runtime is already reachable, the owner can send explicit entries such as:

```text
GITHUB_TOKEN=...
SLACK_MCP_XOXP_TOKEN=...
```

Hermes passes the message to `env_update`. Values are stored only in that user's
runtime volume as `self-env.json`; the tool returns key names and schedules a supervisor
restart. Organization secret keys and runtime-control variables cannot be changed this
way. The initial bot token/model credential still has to be provisioned locally so the
runtime can receive the first message. Telegram itself may retain the message in chat
history; do not use this channel for secrets if that retention is unacceptable.

## 3. Google Workspace and archive

Create a Google Cloud OAuth client of type **Web application**. Enable Gmail, Drive,
Calendar, Docs, Sheets, Slides and Tasks APIs. Configure the consent screen and add
yourself as a test user if the app is in testing. Register exactly
`http://localhost:8000/oauth2callback` (substitute your configured oauth_port).
Fill GOOGLE_OAUTH_CLIENT_ID, GOOGLE_OAUTH_CLIENT_SECRET and settings.google_email.
Add `google`, start, and ask Hermes to list your upcoming calendar events. Follow its
OAuth URL in your browser; tokens persist under `/state/google in the selected runtime volume`. Consent scopes are
determined by the selected upstream tools. Google testing-mode refresh tokens can expire.

On a VPS establish the SSH tunnel below **before** OAuth. The callback binds to all
interfaces inside the container but is published only on host loopback. The browser
redirect uses localhost on your laptop, through that tunnel.

Gmail archive is searchable Gmail mail (for example, `in:all -in:inbox -in:trash`).
For a Google Takeout archive, extract it into `spaces/me/archive` on the host.
The file tools read UTF-8 text up to 2 MiB per file: split large mbox/JSON exports first.
Binary files need an appropriate reader. This does not implement Google Vault.

## 4. Browser, LinkedIn, Meet and speech

Open `http://localhost:6080/vnc.html` after enabling `browser` (or `meet`). It is a
private desktop without an additional VNC password: keep the localhost binding and
SSH tunnel. Log into LinkedIn/HH/other sites yourself. Cookies persist in the selected runtime’s state volume under /state/browser.
No private LinkedIn API, bulk outreach or CAPTCHA bypass is provided.

For Meet add `meet`, run `hubctl up`, then `hubctl meet-auth`. Use noVNC to sign into
the newly opened Google browser, then press Enter in the terminal. This saves the
Meet plugin's separate auth state. Ask Hermes to join an exact meeting with participant
consent. Captions are the supported transcript source; attendance/admission and captions
must work in that meeting. `hermes meet setup` inside the agent reports readiness.

Add `transcription` to transcribe supported audio through local faster-whisper. The
small model downloads on first use. Telegram MCP's own cloud transcription is disabled.
Live Meet audio routing, speaker separation and guaranteed verbatim transcripts are
not configured; these differ from caption capture and file transcription.

## 5. HeadHunter

For HH public search, `hh` alone is enough. For personal resumes and applying, obtain
an applicant OAuth token using an approved HH application; set HH_TOKEN and an
identifying HH_USER_AGENT per HH requirements. The tool sends an **existing HH resume
ID**, not an uploaded arbitrary PDF. Tests/CAPTCHA/platform rules may block applying.
An explicit task may authorize the exact application without another confirmation;
the agent supplies `authorized: true` only for that task and records the returned receipt.

## 6. GitHub, Slack, Atlassian

- `github`: GITHUB_TOKEN for the official remote MCP endpoint. Choose permissions for
  the repositories and operations you need. Account/organization policy still applies.
- `slack`: SLACK_MCP_XOXP_TOKEN from your authorized Slack OAuth app. User scopes must
  cover search/read on your accessible conversations. Leave SLACK_MCP_ADD_MESSAGE_TOOL
  empty to disable posting; set a comma-separated channel ID allowlist to opt in.
- `atlassian`: official remote OAuth MCP. After startup run
  `hubctl exec --user me -- hermes mcp login atlassian`.
  Follow Hermes's printed OAuth instructions. After browser consent, if its loopback
  redirect cannot reach Docker, copy the complete redirect URL from your address bar
  and paste it into Hermes's waiting terminal prompt. The pinned Hermes supports this
  callback-paste flow and still verifies OAuth state. Do not paste it into chat or logs.
  Atlassian's tenant policies and OAuth scopes must still permit the operation.

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

Edit `organizations/acme/settings.yaml`:

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

Add organization credentials to `organizations/acme/secrets.dev.env` or
`secrets.prod.env`; add personal OAuth/API credentials only to the member's matching
`spaces/<user>/secrets.*.env`. The organization file is loaded separately and is not
copied into the user space. A member may only remove an approved MCP with
`disabled_mcp`; they cannot add a feature, credential or MCP. Every organization MCP
must be listed in `read_only_mcp` and must use a non-empty `tools.include` allowlist;
the list is what Hermes exposes to the agent. Organization `docs/` is mounted as
read-only `/org`. `org_actions` is empty by default; add an action only when
the organization explicitly permits that mutation. The repository does not provide a
public authentication gateway, so an external ingress must authenticate the principal
and select the corresponding user space before invoking `hubctl`.

## VPS and another person

Install Docker with Compose 2.30+, copy the project, run init/setup/up on the VPS.
From your laptop, forward private login surfaces:

```bash
ssh -N -L 6080:127.0.0.1:6080 -L 8000:127.0.0.1:8000 user@your-vps
```

Do not open 6080 or 8000 in the VPS firewall. Telegram uses outbound polling.
For another person run init with a different user space, separate secrets and
different base browser_port/oauth_port (reserve each base and base+1 for dev). Use their own bot/session and Google consent.
Each deployment is fully capable within its own container; this is not a hostile-tenant
hosting platform. On Windows use the .exe binary and Docker Desktop's Linux containers.

## Native skills, hooks and memory

These are upstream Hermes features, not parallel implementations. Their paths inside
each runtime are /state/hermes/skills, /state/hermes/hooks, /state/hermes/plugins and
/state/hermes/memories. MEMORY.md and USER.md are enabled by default (memory: true).
Use `hubctl exec --user me -- hermes skills list`, `hermes hooks list`,
`hermes plugins list` or `hermes memory --help` through the same exec command.
Install skills with Hermes's own skill installer; review their code and permissions.

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
