---
description: Connector pipeline — review/build/admission contract, lifecycle, recipe ledger, tiers, and per-connector prepared entries.
last_verified: 2026-10-20
---

# Prepared connectors

The reviewed catalog `internal/toolhub/api/prepared/catalog.json` records
exact commits, licenses, entrypoints, credential overlays, Broker contracts,
runtime settings, egress, read probes and user handoffs — data, never
provider branches. Reviewed Broker contracts ship beside it under
`prepared/contracts/` and validate with their entries. ToolHub registers the
reviewed contract with the Broker before enrollment. Unprepared recipes use
a content-addressed contract synthesized from their declared deliveries.

## Declarative OAuth

`prepare_source` accepts a secret-free `connection_recipe` object; a pinned
repository can also declare it in root `mcp-connection.json`. The optional
`oauth` contract declares HTTPS authorize/token URLs, scopes, `client_input`
(a required file field), `client_sections`, `registered_redirect_sections`,
`state_target`, `token_file`, `token_template`, `probe_tool` and optional
`probe_args`, `access_type`, `prompt`, `account_input`. This metadata is bound
into the immutable definition and review digest. Catalog entries are defaults;
adding another compatible MCP or OAuth issuer needs no provider branch in Go.

Client sections contain standard `client_id`/`client_secret`; sections listed
under `registered_redirect_sections` must register the exact ToolHub callback.
Templates contain objects and literals with explicit `$oauth.ACCESS_TOKEN`,
`REFRESH_TOKEN`, `EXPIRES_AT_RFC3339`, `EXPIRES_AT_UNIX_MS`, `TOKEN_TYPE`,
`OAUTH_SCOPE`, `TOKEN_URL`, `SCOPES` substitutions (each with `$oauth.` prefix),
plus `$client.ID` and `$client.SECRET`. Arbitrary expressions are rejected.
Client files are collected through the protected form, never chat arguments.
The communication handoff returns the protected form as `login_url`; ToolHub
can expose it as `signin_url` for the same onboarding. A live Broker form stays
valid until its actual expiry; polling must not create a competing request.
OAuth retains PKCE, one-time state, owner authorization and network guards.
The server must pass its declared read-only probe before enablement.

Sessions remain encrypted in owner/binding-scoped Broker state. An optional
`account_input` names a required environment field for servers expecting an
email-named session file: ToolHub creates a lease-local view and copies refresh
changes back to the canonical JSON after the workload stops. It does not share
tokens between connectors or patch upstream code. Providers with different
authentication protocols still require an explicitly reviewed implementation;
this contract does not infer undocumented authentication APIs.

`context_sources` merges another exact-pinned repo into the build context
under a declared prefix (the reviewed equivalent of an upstream vendoring
step — same blob verification and path safety); overlay pins stamp the
definition and bind into the review digest. A `preflight_network` entry runs
its unauthenticated `tools/list` probe with egress + placeholder credentials
because the server must reach its API before answering; unprepared sources
keep `--network none`. Live evidence: `.work/done/CHG-0036-*`.

## Artifact pipeline (review → build → quarantine → preflight → approve)

`hubctl artifact import` drives the whole path:

- **Source**: canonical public GitHub URL + exact 40-char SHA only;
  commit/tree/blob verified via two API calls + exact-SHA raw URLs with blob
  ID checks; ≤8-request batches, bounded byte/time budgets; sensitive paths
  excluded before fetch. Content secret scan still mandatory — a token-shaped
  match whose body is one repeated char is a placeholder fixture; a varied
  body fails closed.
- **Recipe**: `GenerateArtifactRecipe` builds pinned-base Dockerfiles for
  conventional Python/Node/Go/Rust manifests (repo-pinned digest base when
  none supplied; upstream Dockerfile never required). Lockfiles mandatory
  (`package-lock`/`pnpm-lock` — pnpm 10.32.1 pinned, `Cargo.lock`, …).
  Several `bin` aliases to one file = one server. Missing egress review →
  HTTPS hosts proposed from bundled `*openapi*.json` `servers` entries,
  bound into the review digest; malformed/credentialed/oversized fail
  closed. Byte pinning does not yet fully lock transitive deps.
- **Verify**: `VerifyOCIArtifact` checks a bounded OCI tar in memory —
  SHA-256 blobs, sizes, one Linux platform, manifest/config/layer refs,
  SLSA v0.2/v1 provenance + SPDX 2.2/2.3 attestation subjects. Integrity/
  presence only — not a schema audit or trusted admission.
  `PersistOCIArtifact` stores by archive SHA-256, publish-after-complete-
  write, no replacement; objects are private quarantine, never mounted by
  workloads; `OpenStoredOCIArtifact` re-verifies on read.
- **Build**: rootless BuildKit under pinned seccomp
  `docker/seccomp-buildkit-rootless.json` — exact-SHA acceptance, internal-
  only network, read-only root, no binds/devices, bounded cpu/mem/pids/
  tmpfs, only SETUID/SETGID caps, `systempaths=unconfined`,
  `--oci-worker-no-process-sandbox` (RootlessKit bootstrap); privileged and
  `seccomp=unconfined` forbidden. Runtime MCPs use the separate
  `docker/seccomp-mcp-runtime.json` (no SETUID bootstrap). Pinned egress
  proxy dual-attaches for the TLS package-host allowlist;
  `buildkitd.toml` mirrors `docker.io` (mirror.gcr.io first). Build budget
  25 min.
- **Admission**: `artifact preflight` runs a live `tools/list` against the
  loaded image under the MCP runtime profile (HOME tmpfs, not the BuildKit
  bootstrap); `artifact approve --contract <json>` from `preflight-list` or
  a matching `review-manifest` — `--review-digest` must equal the restamped
  `ImportedReviewDigest`. Registration is immutable; contract drift rejects.
  Claimed import tools are preview-only, never register. `LoadStoredOCIArtifact`
  re-loads a verified archive into the daemon when the pinned tag is absent,
  before falling back to a digest-pinned pull; closed when none exists.

## Source resolution ledger (M13)

Every consulted source (prepared catalog, MCP Registry, ToolHive, Docker MCP
Catalog, `server.json`/MCPB, repo hints) records ranked claims — launch
ladder L0–L6, credential ladder R0–R7 — with response digest + fetch time in
the resolution ledger. Catalogs outrank repo files; mismatched candidates
stay flagged `ignored`; same-rank disagreement fails closed
(`ambiguous_entrypoint`/`ambiguous_credential_contract`); `credential_source`
records the confirmed origin. Recipes are content-addressed —
`recipes/<sha256>` shared store, onboardings carry `recipe_digest`. Trust
tier from the weakest winning rank: T0 prepared-only, T1 registry-grade +
authoritative credentials, T2 heuristic. Golden corpus:
`internal/toolhub/api/testdata/corpus/` (`GOLDEN_UPDATE=1` re-pins).

`RecipeResolver` is secret-free before source review: pins repo+subfolder+
commit, reads bounded declarations only. Compose/Docker parsed as
declarations: one MCP service selectable, sidecars recorded; host mounts,
privileged, host namespaces, shell healthchecks fail closed. Upstream
`RUN` mounts: `type=cache` inert; secret/bind/ssh/tmpfs/unknown mounts and
host/insecure/privileged options mark `review` and force the `.hub`
fallback (upstream Dockerfile then only disambiguates multi-main Go trees).
`.mcp.json`/`.env.example` yield names and launch metadata only — values
never returned, files never reach BuildKit. `*_PROXY`/`TEST_*`/`*_TEST` env
names are excluded from credential fields; deployer `*_PROXY` declarations
join `proxy_environment` (always filled with the controller's egress URL —
artifact/owner can never pick the proxy). Source scanning for custom proxy
reads (`os.Getenv`, `process.env`, `os.environ`, `env::var`, dotenv/
Dockerfile assigns) merges `*_PROXY` names into `proxy_environment` as
review candidates, never values.

## Catalog adapters and tiers

`HUB_RECIPE_CATALOGS` — comma allowlist of `mcp-registry`, `toolhive`,
`docker-mcp`, `smithery`, `docker-hub`, `ghcr`, `github-search` (`all` =
seven; github-search is source-only fallback, never install authority).
Fixed public endpoints; Smithery bearer via `HUB_SMITHERY_TOKEN_ENV`
(request-only), GHCR via `HUB_GHCR_TOKEN_ENV` (api.github.com only), Docker
Hub search via official API + OCI-distribution metadata, GitHub search via
optional `HUB_GITHUB_SEARCH_TOKEN_ENV`. Container hits need an immutable
manifest + GitHub source label + exact revision + provenance + SBOM.

Entry `tier`: `t0` reviewed prepared (carries `image {ref, digest,
provenance, sbom}` — the CI-built OCI pin, ADR-0057; runtime admission
verifies the pin, never rebuilds), `t1` operator-confirmed import
(`hubctl connector import <ref>`: `mcp-registry:`/`docker:`/`toolhive:`/
`mcpb:`/`hermes:`/`aqua:` refs; pure parse, `--confirm` stores the content-
addressed recipe), `t2` arbitrary-source path. Entries may declare
`toolsets`/`default_enabled`/`default_excluded` — install projects the
default set; `connector_enable <t> toolsets=[…]` widens. `PrefetchPlan`
warms pinned images inside the 8 GiB cap — evicts only `hermes-hub.*`
labels.

## Lifecycle

`discover` (≤5 candidates: prepared, artifact alternatives, registry
name-matches; name/command ≠ authority) → `prepare_source` (candidate_id +
stable request_key; bounded ~90 s sync window then durable `preparing`
record — detached work, outcome via `status`; channel notice + continuation
job on finish) → `required_credentials` → protected Broker form → `confirm`
(nonce) → `enable`. Direct `source` URL works registry-free; a prepared repo
URL selects its reviewed commit, explicit `/commit/<sha>` keeps the generic
path — both re-resolve and re-review.

- `status` carries `next_action`+`instructions`; retries reuse request key;
  `selector` (definition id / repo URL / basename) fails closed with
  `ambiguous_target` candidates. Re-onboarding rotates the owner connection
  in place — superseded binding revokes before replacement enables.
- Probe-with-secrets: first `tools/list` runs placeholder creds, no egress
  (or egress for `preflight_network`); post-submit it re-runs with real
  values + egress. A `tools/list` that fails naming required envs opens the
  protected form for exactly those names (either/or → alternative groups,
  stored as `credential_groups`); an answered-empty list is acceptable only
  for definitions declaring required inputs (mcp-atlassian pattern);
  unclassified failure stays hard. `AdmissionPending` marks a definition
  draft until the probe succeeds.
- Credential URLs/endpoints that are HTTPS hosts join the workload's egress
  ACL at readiness (`*_URL`, `*_ENDPOINT*`); secrets/auth'd/query'd values
  cannot. `tcp_forwards` (reviewed companion loopback relays through the
  Squid CONNECT proxy to credential-bound `host:port`) are docker-fallback
  only — stock ToolHive admission fails closed when declared.
- `oauth` entries return `awaiting_oauth` + `authorization_url` (one-time
  loopback callback; token lands in the broker lease; `status` reissues an
  expired link; existing token files never overwritten). URL-mode
  elicitation requested; plain response is the fallback.
- `rotate` re-enters the credential phase on the same onboarding (old
  workload stops first); `disable`/`revoke` flip reversibly-preserving vs
  blocking; `remove` is two-phase: preview + `remove_nonce`, nonce-carrying
  repeat runs the finalizer ladder to verified-zero (`remove_receipt`) —
  cleanup-incomplete stays visible in `connector_status`.
- Shared immutable artifacts are never deleted while another owner uses
  them; a revoked binding blocks only its deterministic ID — fresh
  onboarding rebinds fine.
- SSE/HTTP-transport servers: probed over stdio first; a confirmed stdio
  answer rewrites transport; otherwise `hubctl mcp-bridge` sidecar relays
  stdio↔loopback inside the workload's netns (`source.network_transport`
  recorded; bridge client never honors proxy env; HTTP/SSE stays loopback).
  Stdio companion synthesizes `-32601` for `server/discover` and falls back
  to `initialize` (rust-mcp-filesystem hangs otherwise).
- Generic admission: `hubctl connector generic-controller` — trusted
  definition + ToolHive/seccomp paths + `max_active`/`idle_ttl_seconds` in
  JSON config; creates owned internal network + pinned egress proxy + one
  ToolHive workload per id, injects creds via ToolHive env secret provider
  (never RunConfig), returns loopback MCP only after inspect proves digest/
  limits/non-root/ro-root/caps/NNP/seccomp/net/volumes. Host-state mounts
  and shared+credentialed workloads rejected; stateful fallback uses
  controller-owned volumes. `dynamic_definitions: true` (loopback only)
  makes each admission carry the full immutable definition — no per-MCP
  controller rewrite. `docker_fallback: true` (stock ToolHive gap): the
  controller applies the same create-time profile itself; companion bridge
  needs a Linux ELF `bridge_binary` (`hubctl artifact linux-bridge`), one
  workload/binding, owner `credentials.env`, `--env-file` injection, named
  volumes for stateful only; an MCP container receiving a forwarding secret
  fails inspect.
- Trusted-artifact caveat: recipe validation + a claimed tool list is never
  admission evidence — remaining verification tracked in
  `.work/done/CHG-0025-trusted-artifacts/verification.md`.
- ToolHub endpoint holds one stateful MCP server per owner token;
  projection changes emit `notifications/tools/list_changed`; persisted
  store reloads without restart. Cell/endpoint behavior: services/toolhub.md.

## Human action links (?t=)

Every human-only step is a one-time `?t=<token>` page — `/credentials`,
`/confirm`, `/remove`, `/connect`, `/device`: resolves the stored nonce of
that onboarding+operation only; spent/expired → 410; POST without token
rejected; each page re-checks principal/context/runtime. No link material
ever reaches an agent-facing result. Delivered on the requesting channel —
`Caps().Actions` channels may render an affordance; others get the plain
link. `HUB_PUBLIC_ORIGIN` (https) + `HUB_PUBLIC_LISTEN` host the pages on a
second listener where control/gateway routes 404. `/connect` offers the
RFC 8628 device flow (`/device/<id>`) and a paste-the-redirect form
validating `state`+origin. The credential form renders from the recipe
ledger: pinned source, trust tier, egress hosts, per-field provenance +
HTTPS `token_url` help; heuristic-only (R6) fields show as suggestions.
Submitted values probe read-only before persisting — failure leaves no
ciphertext/connection/broker credential and re-renders values page-side.

## Bundle entries (per-connector notes)

`discover` accepts each bundle id; `blocked` entries have no candidate.
One-op start: `prepare_source` with the repo URL + stable `request_key`.
Below: source, entry, contract, probe, handoff essence.

### personal-telegram

Catalog id `telegram`; runtime definitions are `telegram-account-read`/`-write`.
`letya999/telegram-mcp` pinned `26f1632b` — generated Python artifact,
`proxy` extra, `/opt/venv/bin/telegram-mcp` stdio. Broker contract
`telegram-session` (API ID/hash + StringSession + expected numeric user ID;
server verifies before serving MCP). Connects before `tools/list` — first
prepare returns a credential request; `list_accounts` is the read probe.
`TELEGRAM_EXPOSED_TOOLS=read-only` fixed, transcription off, per-owner
workload with private egress proxy; upstream read-only does not filter bot
peers (use the account adapter for that). Never reuse one StringSession
across IPs — dedicated device per Hub (generate locally via the fork's
`session_string_generator.py --qr`, `TELEGRAM_DEVICE_MODEL=hermes-hub
ToolHub`; enter values only in the Broker form). With `ingress: [telegram]`
the gateway offers a loopback QR page for `intake: telegram_qr` contracts —
session stays in memory, goes Broker-only; `telegram_auth: false` disables;
API pair via `telegram-auth.<env>.env` (`HUB_TELEGRAM_AUTH_API_ID/HASH`).
Rotation: new device session + `rotate` + form; terminate the old device in
Telegram yourself. Send upgrade: `prepare_source` `{"onboarding_id":…,
"telegram_send":true}` → `read-only+send_message` re-review on the same pin,
retains session, only `send_message` may be added; verify with a Saved
Messages send (`chat_id:"me"`, unique `operation_id`). Event tools:
`wait_for_new_message`/`wait_for_settled_message` bounded polls,
`requeue_event` (id only), `incoming_feed_status` — `watch_subscribe`
polls them; `delivery=stream` holds one outstanding bounded wait per
subscription (seconds, no persistent workload); dedup via durable seen-set;
no always-on push. Never copy `.session`/OTP/API hash into repo or chat.

### playwright-mcp

The managed `browser` surface.

`microsoft/playwright-mcp` `ab6d67cb` — T0 zero-build,
`mcr.microsoft.com/playwright/mcp@sha256:6907…cb` with `--caps vision,pdf`
+ bounded `--idle-timeout`. Per-connection isolated workload: own Chromium +
`connection-state` profile + `downloads/`. `egress_class: web` — arbitrary
HTTPS through audited per-workload proxy, deny-first on private/loopback/
link-local/CGNAT/metadata/hub-internal + per-request DNS; connection-level
`egress_allow`/`egress_deny` refine, deny wins. Default projection read-only
(navigate/snapshot/tabs/wait); `act` needs `connector_allow_writes`; `eval`/
`network` are separate toolsets hidden until enabled. Optional
`browser-session` contract imports Playwright storage-state through the
protected form. `connector_status` on a web connection mints a one-time
short-TTL owner-bound viewer link (`GET /viewer/<conn>?t=` — 410 on reuse).
`shm_size_mib` bounded (1024; never above workload memory).

### notion

`makenotion/notion-mcp-server`, Node `bin/cli.mjs`. Contract `notion-token`
→ `NOTION_TOKEN`. Share root/pages with the integration first; verify
identity + search + one page read; empty search usually means nothing
shared. Never create/edit pages as a test.

### google-calendar

`nspady/google-calendar-mcp`, Node `build/index.js`. Calendar only.
Contract `google-file-session` (OAuth client JSON + owner/binding state;
token path inside broker state mount; file creds read-only, mutable state
checkpointed after writer stops). Token transfer is local owner-authorized
only. With client JSON but no token, confirm returns the Google auth link
(`calendar.readonly`; callback `http://127.0.0.1:8090/oauth/callback` —
browser on the Docker host). Verify `list-calendars` + bounded
`list-events`. Expired token → diagnose or rotate, never a second
connection. The Google Calendar API must be enabled in the OAuth client's
Cloud project. Live dev acceptance on 2026-10-20 verified browser consent,
confirmation, `list-calendars` and a one-day `list-events` read.

Pending OAuth reuses the Broker grant's recorded binding ID and policy epoch,
even when the initial read probe has not yet enabled a binding. Rotating the
credential reference on that retry would select a different binding-scoped
state slot and hide the freshly checkpointed token from the MCP server.

Protected form, confirmation and OAuth links arrive as new chat messages when
issued. If a pending confirmation or OAuth link is lost, call ToolHub `status`
with the onboarding ID and `resend: true`; the channel posts a fresh link.

### google-workspace

`taylorwilsdon/google_workspace_mcp`, pinned v2.1.1 source, Python
`workspace-mcp --transport stdio --single-user --permissions ...`. The prepared
recipe selects read-only Calendar, Drive, Gmail, Docs, Sheets, Slides and Tasks
permissions plus Google identity scopes. Contract `workspace-file-session`
collects `GOOGLE_CLIENT_SECRET_PATH` as JSON upload and `USER_GOOGLE_EMAIL`.
Both inputs are required for this workload: leave the Broker form's "field not
required" opt-out unchecked. If delivery is incomplete at confirmation,
ToolHub returns to credential collection and issues a new protected form.
The generic OAuth template emits the upstream flat authorized-user session;
the lease-local account file supports upstream token refresh without changing
its implementation. Read probe: `list_calendars`. Calendar, Drive, Gmail, Docs,
Sheets, Slides and Tasks APIs must be enabled in the OAuth client's project.
Google consent and any organization policy still apply. OAuth apps in Testing
can issue short-lived refresh tokens; installation cannot bypass that policy.
Source build and real unauthenticated MCP preflight passed on 2026-10-20;
provider-account acceptance is tracked separately in CHG-0144.

### github

`github/github-mcp-server`, generated Go recipe (main package + literal
`stdio`). Contract `github-pat` → `GITHUB_PERSONAL_ACCESS_TOKEN`. OAuth/App
needs a matching reviewed contract. Verify `get_me` + one authorized repo
read; mutations are separate actions.

### gitlab

`zereight/gitlab-mcp`, both bin aliases → `build/index.js`. Contract
`gitlab-pat`: `GITLAB_PERSONAL_ACCESS_TOKEN` (+`GITLAB_TOKEN` alias),
non-secret `GITLAB_API_URL`/`GITLAB_ENDPOINT` for plain-HTTP self-hosted
behind managed loopback forward `18929`. Undeclared upstream env knobs stay
optional. Entered HTTPS host joins egress; secrets/queries cannot. Verify
whoami after rotation; on failure fix creds/host — never relax egress.

### atlassian

`sooperset/mcp-atlassian`, `/opt/venv/bin/mcp-atlassian` stdio. Registers
zero tools until config exists — the review collects the full six-field
contract. `atlassian-env`: `JIRA_*` or `CONFLUENCE_*` complete block
satisfies alternative groups (every field optional, but an empty submission
rejected). `atlassian.net|com` pinned; each entered URL host joins egress —
self-hosted works unchanged. Probe follows the delivered block
(`jira_get_all_projects`/`confluence_list_page_templates`) — a bad token
still passes `tools/list`; on failure rotate the same connection.

### txt-to-sql

Catalog id `txttsql`; the primary SQL connector.

`letya999/txttsql-mcp` (AGPL-3.0-only), Rust `/app/target/release/
txttsql-mcp --config /app/config.toml` — repo builds into an immutable
artifact, never mounted. Config resolve: existing `config.toml` wins, else
`TXTTSQL_SOURCES` single-line JSON; missing → zero-source server that still
answers `initialize`/`tools/list`. Contract `txttsql-sql`: sources JSON +
optional `TXTTSQL_PASSWORD_{1,2,3}` env refs, optional `config.toml`
read-only at `/app/config.toml`, CA bundle `/run/txttsql/ca.pem`.
`tcp_forwards` 15432–15434 relay through Squid CONNECT to credential-bound
`TXTTSQL_ENDPOINT{,_2,_3}` — DSNs point at `127.0.0.1:<port>`, real
endpoints live only in the form; catalog egress sentinel `127.0.0.1`; TLS
verify makes the name an IP literal (`verify-ca`/`ca_file` per source).
Read-only enforced server-side (upstream RO transactions +
`allowed_schemas`/`allowed_tables`, `max_rows`, `timeout_seconds`,
`max_connections`) — no write profile. Probe `list_sources`, then
`list_tables` + one bounded SELECT.

### grafana

`grafana/mcp-grafana` pinned (Apache-2.0), Go `/app/mcp-server` stdio only —
upstream HTTP/SSE never enabled (auth disjoined from Grafana creds).
Contract `grafana-sat`: `GRAFANA_URL`, `GRAFANA_SERVICE_ACCOUNT_TOKEN`,
optional `GRAFANA_ORG_ID`, non-secret `GRAFANA_ENDPOINT` (real host:port →
forward `13000`, URL field `http://127.0.0.1:13000`). `GRAFANA_SOCKS5_PROXY`
deliberately claimed so scanning cannot inject the HTTP Squid URL into a
socks5-only slot. `--enabled-tools=search,datasource,dashboard,folder,
alerting,prometheus,loki` + `--disable-write`; usage stats + Loki guardrail
`enforce`. Probe `list_datasources` + bounded `search_dashboards`/
`query_prometheus`; rotate = new SA token + `rotate`.

### prometheus

`prometheus/prometheus-mcp` pinned (Apache-2.0), Go `/app/mcp-server` stdio;
`context_sources` vendors `prometheus/docs` at the Makefile `DOCS_VERSION`
commit under `cmd/prometheus-mcp/external/docs`. Contract
`prometheus-url`: `MCP_SERVER_PROMETHEUS_URL` + `PROMETHEUS_ENDPOINT`
(forward `19090` for plain HTTP). `MCP_SERVER_MCP_TOOLS=list_targets` keeps
read set only (query/metadata/series/docs/runbooks + target probe);
`MCP_SERVER_DANGEROUS_ENABLE_TSDB_ADMIN_TOOLS` never set; truncation 500.
Probe `list_targets` + one `query`; remote-write/admin are separate grants.

### dbhub

The fallback SQL connector.

`bytebase/dbhub` (MIT), pnpm workspace build → `node /app/dist/index.js`
stdio; HTTP transport never enabled. Exits without config — reviewed
`preflight_environment` injects `DSN=sqlite:///:memory:` for the
unauthenticated probe only. Legacy `READONLY`/`MAX_ROWS` env rejected:
env-mode DBHub has no readonly policy — the DSN's database role must be
read-only (contract requirement). Contract `dbhub-sql`: `DSN`, `DBHUB_-
ENDPOINT` + `DBHUB_SSH_ENDPOINT` → forwards `15432` (db) / `15433` (bastion),
optional `SSH_USER`/`SSH_PASSWORD`/`SSH_PASSPHRASE`/`SSH_KEY`
(`/run/dbhub/id_rsa`), `SSH_HOST`/`SSH_PORT`, CA `/run/dbhub/ca.pem`.
Managed tunnel: `SSH_HOST=127.0.0.1 SSH_PORT=15433`. Verify `tools/list`,
one `SELECT 1` through the forward, the tunnel when SSH configured.

### datalens

`datalens-tech/datalens-mcp` (MIT) pinned — `node /app/dist/index.js`.
Five-tool gateway; upstream `x-mcp-scope` enforced server-side (write via
`invoke_read_command` rejected by the server); effects from annotations;
live schema verified fully classified (51/24/41, unclassified fails
startup). Contract `datalens-auth`: `DATALENS_ORG_ID` (`x-dl-org-id` tenant
pin), `DATALENS_API_AUTH_HEADER` (full value), optional `DATALENS_API_URL`
(internal HTTPS joins egress; `OAuth <token>` vs cloud `Bearer <iam-token>`
— 12 h rotation routine). Fixed: `DATALENS_YC_STATIC_AUTH=1`,
`DATALENS_MAX_RESPONSE_CHARS=100000`, `NODE_USE_ENV_PROXY=1` (calls cross
the Squid ACL: `api.datalens.tech` + `registry.npmjs.org` upgrade notice).
Declares `preflight_network` (schema fetch precedes MCP). Probe
`list_commands` + one `invoke_read_command` (`getWorkbooksList`); writes/
privileged stay a separate instruction.

### Blocked entries

`slack` (awaiting reviewed
recipe + user-token overlay + preflight), `careergo` (no exact source/
license/contract), `agent-browser` (no published runnable image —
discoverable with reason/runbook, never installable; use playwright-mcp).
Catalog placeholders grant nothing.
