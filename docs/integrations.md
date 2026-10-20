---
description: Upstream pins and provider contracts — Hermes API, broker env layout, per-provider OAuth/auth contracts, web/doc/image capabilities.
last_verified: 2026-10-20
---

# Integration contracts

The connector install/review pipeline and the prepared catalog live in
docs/prepared-connectors.md; ops procedures in docs/operations.md; the
authorization model in docs/rbac.md. This file is the pin + contract
reference — what we integrate with, at what revision, under what shape.

## Pins

| Component | Source / pin | Contract |
|---|---|---|
| Hermes | nousresearch/hermes-agent `869228cab4a8276d3b4c78da9d9939670c47bd0f` (0.21.0) | ADR-0012; pin predates upstream tool-`isError`/transport split (PRs 67340/17016 closed unmerged, 68455 open) — hub tools already mark expected failures as `isError`; residual risk CHG-0093 |
| Local STT | faster-whisper 1.2.1 + PyAV 18.1.0 in runtime and standalone STT image | PyAV 19 removed the decoder arg that release uses; native + STT CI gates decode a synthetic WAV offline so skew cannot pass |
| CLIProxy | image-pinned `ba7e55836dee959e93ec6d41395865d9ec535086` | model/image relay upstream |
| ToolHive | `ghcr.io/stacklok/toolhive:v0.48.0` @ `sha256:f1adc609e11e5f60fd5a27d23e7a63eb14dcfee13f3c0b2f1741680a64fcea5d` | container executor for ToolHub workloads (ADR-0013); cellinit/cred-proxy are built from this repo, not upstream |
| Telegram account | letya999/telegram-mcp `26f1632b2b07cca16fa8f172fe645477921db9ed` | Python stdio; prepared Broker entry defaults `TELEGRAM_EXPOSED_TOOLS=read-only`; bot token stays separate; the unrelated PyPI `telegram-mcp` is deliberately not used |
| Telegram bot channel | Telegram Bot API via `hub-communication` | channel adapter; sender allowlist + durable outbox |
| Slack App channel | Slack Events API via `hub-communication` | official `v0` HMAC request verification; workspace+sender mapping; not Slack data tools |
| Google | official `https://<product>mcp.googleapis.com/mcp/v1` (ADR-0019) | ToolHub remote read grants per product; third-party Workspace MCP is not in the default image |
| Slack data | korotovsky/slack-mcp-server `b88c0de3f706f4f07337c9eda7133c736d1c9524` | stdio, OAuth user token, channel posting allowlist |
| Playwright | microsoft/playwright-mcp, npm `@playwright/mcp@0.0.80` | hub-owned stdio; `browser` attaches to per-user persistent Chromium over loopback CDP, `browser_guest` isolated; mutation tools only under `browser_act` |
| GitHub | official remote MCP | `https://api.githubcopilot.com/mcp/` + `GITHUB_TOKEN` bearer |
| Atlassian | sooperset/mcp-atlassian `74bdaa8f1d28783cccfe99f7b4d75e6dc947cf76` | dedicated `docker/mcp-atlassian.Dockerfile`; Jira via `JIRA_URL`/`JIRA_USERNAME`/`JIRA_API_TOKEN` (Confluence supported upstream, preset enables Jira only; no Rovo endpoint) |
| GitLab | Debian `glab` from the pinned runtime distribution | CLI; `GITLAB_TOKEN` PAT + optional `GITLAB_HOST` |
| HH | api.hh.ru/openapi/redoc | GET /vacancies, /vacancies/{id}, /resumes/mine; POST /negotiations |
| Memory Bank | letya999/memory_bank_setup `2eb4e41968b86dae4c192dd9f9cff5b72c9d754f` | documentation separation conventions |

Local manager instructions for the owner's PC: docs/local-accounts-manager.md.
Each Python MCP has a separate locked virtualenv (Telegram and Hermes need
incompatible MCP majors). APT packages follow the pinned Debian repos; not a
bit-for-bit reproducible OS build.

## Pinned Hermes API contract

The image pin is validated before a runtime adapter builds. Reproducible
probe: `go run -tags integration ./cmd/devcheck hermes-contract
hermes-hub:test` (part of `just docker-check`) — starts `hermes gateway run
--no-supervise --force` on an isolated volume and verifies
`/v1/capabilities`, durable `Idempotency-Key` replay/conflict for
`/v1/runs`, run status + SSE events, stop/approval error semantics,
`/api/sessions` resume after restart, `/api/jobs` cron coexistence, and
non-terminal recovery as `interrupted`; plus image Hermes version and git
commit. A mock is never evidence.

Pinned capabilities: Bearer auth, `split_runtime=false`, 24 h run-idempotency
retention, `X-Hermes-Session-Id`/`X-Hermes-Session-Key` continuity headers;
no CORS, admin-config, memory-write, audio or realtime-voice API. Because
tools execute on the API-server host, this API is an internal opt-in
compatibility surface, not the private Go boundary's replacement.

## Credential Broker

Copied source is the standalone module under `services/credential-broker`
(MIT, separate from Hermes' AGPL-3.0-only). `just credential-broker-check`
runs its full unit/race/coverage/build gate, `credential-broker-docker-check`
its minimal image. No Broker service is enabled by default — config, signer
keys, provider data, contracts and materialization paths are provisioned
explicitly.

The Go SDK signs exact method, URI, body and canonical identity. Adapter
audiences: ToolHub `broker:control`, Communication Hub `broker:approve`,
runtime materializer `broker:runtime`. Enable per audience with
`HUB_CREDENTIAL_BROKER_{CONTROL,APPROVE,RUNTIME}_{URL,KEY_FILE,KEY_ID,ISSUER}`
— all three audiences required; signing keys live as private files inside
the matching Hub container, never in the repo. `api/v1.Materialized` never
passes through Hermes messages, MCP projections or job/audit ledgers.

Every credential-bearing definition binds `credential_contract_id`,
`credential_contract_revision` and an explicit `credential_contract_env`
mapping at review/bind — from a covering catalog/`contracts_dir` contract or
a `dyn-` contract ToolHub registers over `broker:control` for declared
`env`/`file`/`json` inputs (local-plane inputs — `http_header`, `brokered`,
OAuth-minted, per-request — are satisfied by their own planes and never need
a contract; a `Sharable` definition synthesizes `allow_shared`). Flow:
ToolHub creates the broker request+grant, the gateway accepts
`/credentials <request-id> <code>` (or the direct form — rendered compose
sets `BROKER_DIRECT_FORM=1`; remove it to restore the spec's trusted-channel
confirmation), the runtime acquires/materializes/releases a lease. An expired
connect link reissues on the next `status` poll. Existing local credential
records are rejected once Broker mode is on — migration is explicit.
Runtime materialization rejects a missing delivery for any referenced input.

ENV deliveries are supported. File deliveries use a reviewed read-only
runtime mount and a per-call workload the controller removes before the
lease releases — enable only when the broker materialization dir is an
explicitly shared, non-symlinked path visible to controller and daemon
(`credential_mount_root`); without it file delivery fails closed.

## Google Workspace remote MCP

`hubctl connector connect --provider google --official-mcp --product gmail`
with absolute registry/ciphertext/key paths. Products: calendar, gmail,
drive, docs, sheets — separate connection + read-only OAuth scopes each.
`--account` = expected Google subject or verified email; CLIENT_ID/SECRET
via protected `--client-file` (never chat/argv). Web OAuth client registers
exactly `http://127.0.0.1:8543/callback` (`--callback-port 8543`).

These are Google-managed services; official setup needs Developer Preview,
a Cloud project with the underlying APIs + MCP services enabled (same
project as the OAuth client) and consent config. Discovery rejects an
inaccessible/empty service. Provider schemas are snapshotted and validated
locally without remote `$ref` fetches; new/unknown and mutation tools are
excluded from read grants. Refresh/revoke use the same commands. No real
Cloud account has been tested.

## Personal Telegram account MCP

`docker/telegram-account.Dockerfile` builds the standalone pinned
Telethon virtualenv — never `FROM` the hub image; deploy by digest, not tag.
Controller path and local-controller evidence are in
docs/prepared-connectors.md. Import via `hubctl connector connect --provider
telegram --account <numeric-id> --connection <id> --from-file
<protected-session-file> --deployment-file <manifest> --endpoint <mcp>`;
the command verifies `get_me` identity before saving — a bot session or
wrong user is rejected. Read tools: get_account, list_dialogs, get_messages
(bounded before/after cursors over real history API), search_messages,
get_chat_info; bot peers excluded; message text is untrusted, media is
metadata-only. `--write` creates a separate explicit grant the server
checks independently. Credentials load per call, wiped after; no unattended
login. `connector revoke` blocks local calls and revokes ciphertext;
invalidating the MTProto session needs Telegram Devices explicitly.

## Calendar (ToolHub, M5 stage 2)

`hubctl connector connect --provider google …` with `--client-file`
(CLIENT_ID/SECRET), `--account` subject, unique `--connection`; key file
outside ciphertext dir. Prints auth URL, waits loopback callback, verifies
account before creating a read binding. `--write` is a separate mutation
grant — never upgrades read. `connector call` args: `calendar_id`,
`event_id`, `event_json`, `page_token`; list paginates 100/page via
nextPageToken; no arbitrary API URLs. `connector refresh` rotates only that
connection's token; `revoke` denies calls first, then requests Google
revocation. Verification to date uses official-shaped fixtures, not live
Google.

## Slack data (ToolHub, M5 stage 2)

`--provider slack --workspace T... --account U...|W...` — never
email/display-name identity. `--write` is an independent `chat:write` grant.
OAuth PKCE requests user_scope only; Basic client auth; a co-returned bot
token is discarded. Slack App `SLACK_BOT_TOKEN`/`SIGNING_SECRET`/`APP_TOKEN`
are communication credentials only — they cannot enable data tools. Reads:
`search`, `history`, `replies` (15/page, provider pagination); writes:
`send`, `reply`, `update` — canonical C/G/D ids + validated timestamps, no
bot posting or username/icon impersonation. `auth.test` rechecks bound
workspace+user per call; 429/errors report without mutation retry — review
an uncertain send in Slack itself. Revoke blocks locally first, then
requests `auth.revoke`. Fixtures do not prove a live workspace login.

## Documents and images

Hub `hubctl tools` surface; pinned Hermes has no document toolset and its
native vision/`image_generate` stay off `platform_toolsets`. Contract:
ADR-0027/0028/0029/0030 + SPEC-0033. Full tool/effect matrix in
docs/tools.md — bounds here:

- Documents: txt/md/csv/html/pdf/xlsx/pptx; rejected doc/docx/xls/ppt/odt/
  rtf/epub/pages (+`xslx` typo). Images: png/jpeg/webp; rejected gif/bmp/
  svg/tif/tiff/heic/avif. No OCR; pdf is wrapped text with embedded Go
  Regular font; xlsx = one worksheet inline CSV; pptx splits slides on
  blank line; conversions copy extracted text (no layout, no formulas).
  webp via `nativewebp` v1.3.0, PDF font `x/image` v0.46.0.
- Bounds fail closed: doc ≤2 MiB / 20 pages × 3000 runes; image ≤5 MiB /
  4096 px edge / 4 Mpx; concurrency 2 / 5 s; inspect+convert 30 s;
  generate/edit concurrency 1 / 60 s; prompt 1–2000 runes; no queue.
- Scope: invoking user's workspace only — another user's root, absolute
  paths, `..` fail closed; `document_edit` confined to
  `artifacts/documents/`; removal via `artifact_remove` or confirmed purge.
- `image_gen` is off by default, not self-service. Settings fields:
  `provider: cliproxy|fal`, `model`, `delivery: workspace|url`. `model:
  chat` → `model.default` only when allowlisted. CLIProxy allowlist posts
  `/images/generations` (gpt-image-* , grok-imagine-*) or `/chat/completions`
  (gemini-*-image*, `image_config.aspect_ratio 1:1`); fal allows only
  `fal-ai/flux-2/klein/9b` posting prompt+`square_hd`+4 steps+png to
  `fal.run` (`FAL_KEY`, no edit). `delivery: url` returns one provider
  http(s) URL — redirect refused, download host must be public HTTPS or the
  configured endpoint.
- On a rendered stack calls run in the `hub-media` sidecar (SPEC-0061):
  internal-network only, `media.auth` bearer, credentials materialized per
  call from broker grant `HUB_MEDIA_BROKER_GRANT` — never env. Engines
  `remote`/`fal`/`command` via `HUB_MEDIA_ENGINE`; `HUB_MEDIA_ENGINES`
  fallback order + `HUB_MEDIA_MODEL_ROUTES` logical→engine hops; video jobs
  durable under `HUB_MEDIA_DATA` with `HUB_MEDIA_JOB_TTL`, resume provider
  polling after restart. A missing provider or revoked section fails the
  next call; `image_generate`/`image_edit` leave `tools/list` on next start.
- An httptest double of these endpoints is not a live provider call.

## Web search and research

Upstream Hermes `web` toolset (`web_search`, `web_extract`) — configured,
not reimplemented; provider code lives upstream in `plugins/web/<vendor>`,
the hub embeds no vendor client. Features: `web` mounts `config/plugins/
hub-web` read-only and enables the toolset (absent = the tools do not exist;
`agent.disabled_toolsets` strips them from composite toolsets too);
`deep_research` requires `web` and mounts the `deep-research-embedded`
skill. `web:` settings (schema-1) select providers/policy — see the
Settings `WebSettings` block: backend/search_backend/extract_backend,
`search_providers` fan-out list, `keyless_fallback`/`keyless_rescue`,
`extract_char_limit`, `provider_tier`, `cache_*`, `research.*` bounds
(ceilings only — settings can tighten, never widen).

`hub-web` adds three tools inside the same toolset/gate: `web_providers`
(inventory), `web_search_multi` (`providers` list or `["all"]`, ≤8
providers, 60 s, tagged+deduped, `providers_failed`/`partial_failure`,
off-list names rejected), `web_cache` (`status`/`prune`/`clear` of
`$HERMES_HOME/cache/web`).

Keys/endpoints are runtime secrets via `secrets.<env>.env` or the self-env
form: `TAVILY_API_KEY`, `EXA_API_KEY`, `PARALLEL_API_KEY`,
`PERPLEXITY_API_KEY`, `FIRECRAWL_API_KEY`/`_URL`, `BRAVE_SEARCH_API_KEY`,
`KEENABLE_API_KEY`, `SEARXNG_URL`, `XAI_API_KEY` (+`TAVILY_BASE_URL`,
`PERPLEXITY_BASE_URL`). Never in prompts/args/results; `doctor` names a
missing required env for searxng/firecrawl. Keyless `ddgs` + ring keep it
working anonymously; no browser profile participates.

`deep-research-embedded` (`config/skills/`) is the on-disk state machine:
`plan.yaml` → `sources.yaml` → `notes/` → `gaps.md` → drafts → cited
`report.md` + verify pass; one round per durable routine wake, resumes from
`research/<slug>/state.json`. Pages are untrusted data — embedded
instructions never execute; credential-like URL params refused; private-
network targets/redirects/oversized/binary bodies fail closed upstream.
`browser_guest` is the documented fallback when extraction cannot satisfy a
source. Contract: SPEC-0035.

## Organization and user MCP

Standalone users define `mcp_servers` in their own config. In org scope
only host-owned `spaces/<org>/scope.yaml` defines them; users narrow by
`tools: {name: off}`. Every org MCP must sit in `read_only_mcp` with a
non-empty `tools.include` allowlist passed to Hermes — shared org
credentials are reserved for explicitly selected read-only tools;
write-capable servers belong on their own upstream permissions. Built-in
hub mutations are separately gated by `org_actions`.

## External MCP connections

User-owned connections go in `workspace.yaml`'s `mcp:` map + a `tools:`
`mcp-raw:` entry; secrets land in `secrets.<env>.env`:

```yaml
mcp:
  career: {url: https://your-career-host.example/mcp,
           headers: {Authorization: Bearer ${CAREER_TOKEN}}}
  local_tool: {command: node, args: [/workspace/tools/server.cjs],
               env: {TOOL_TOKEN: ${TOOL_TOKEN}}}
tools: {career: mcp-raw:career, local_tool: mcp-raw:local_tool}
```

Examples, not endpoints. An arbitrary REST API is not MCP. StdIO
executables + deps must exist in the agent image/workspace; no automatic
unpinned installs. `doctor` detects missing referenced credentials. Skills:
org skills mount via `skills.external_dirs` read-only; same-named user
skills take precedence. Connector credentials come via `hubctl secret set`
or the intercepted owner `KEY=value` message — encrypted at rest, never
returned in catalog/audit/replies; `env_update` remains only the personal-
terminal overlay, never a host `.env` editor or org-key writer.

OAuth for Google/Slack/Atlassian uses the official URLs in SPEC-0020;
remote MCP discovers `authorization_endpoint`/`token_endpoint` via RFC 9728
+ RFC 8414; tokens persist only in the encrypted store.

## Misc contract rules

- `communication-hub` never mounts scope homes. Its `telegram_auth`
  path (compiled in, default-on for telegram stacks, `telegram_auth:
  false` opts out) holds a fresh MTProto session in memory only while the owner
  confirms QR login, then signs a Telegram-only broker request — the
  session never enters spool, logs or files. The gateway sends the
  immutable job envelope to `hermes-runtime` over private Bearer HTTP;
  binding rejects mismatched user/org/actor/scope before opening a path;
  failed/uncertain results are never blindly replayed.
- HeadHunter apply: fields `vacancy_id`/`resume_id`/`message`; only HTTP
  201 is `sent: true` (receipt Location preserved — a timeout may still
  mean sent; inspect negotiations before retry). OAuth grants,
  already-applied, tests and CAPTCHA are platform responsibilities.
- Native bridges forward tools + schemas only — never resources, prompts,
  sampling or elicitation; an MCP needing those is incompatible. Remote
  providers change contracts independently — re-verify after upgrades.
- Provider content never grants permission to create/edit/send/merge/
  trigger anything.
- ToolHub CLI/container command validation rejects shell executables
  (incl. Windows `.exe`) and `.bat`/`.cmd`; arguments are literal argv —
  repository instructions never authorize host shell execution.
