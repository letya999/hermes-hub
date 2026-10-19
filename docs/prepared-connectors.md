---
description: Exact-source prepared catalog, generic lifecycle and connector runbooks.
last_verified: 2026-10-07
---
# Prepared connectors

The reviewed [catalog](../internal/toolhub/api/prepared/catalog.json) records exact
commits, licenses, inferred entrypoints, credential overlays, Broker contracts,
runtime settings, egress, read tools and user handoffs. New entries require data,
not provider branches. `context_sources` merges another exact-pinned GitHub
repository into the build context under a declared prefix — the reviewed
equivalent of an upstream Makefile vendoring step, with the same blob
verification and path safety as the primary source; overlay pins are stamped on
the definition and bound into the review digest. A reviewed entry may declare
`preflight_network` when the server must reach its API before it can answer
`tools/list` at all (a remote schema fetched at startup): the unauthenticated
probe then runs with egress and placeholder credentials, matching what the
credentialed probe already does. Unprepared sources keep `--network none`.
Live evidence is tracked in
[CHG-0036](../.work/done/CHG-0036-repository-driven-toolhub/plan.md).

Reviewed Broker contracts ship in `internal/toolhub/api/prepared/contracts/` and
are validated with their prepared entries. Provision them into the Broker's
configured contracts directory before enrollment; the Broker control API does
not expose contract registration. Missing contracts are deployment errors,
not permission to invent a provider authentication API.

File delivery uses the rendered owner-scoped shared tmpfs volume at
`/run/broker-materialized`. ToolHub and the controller resolve its Docker daemon
mountpoint from the named volume and check translated source paths and
read/write modes against the approved lease mounts. Broker state stays encrypted
between leases; its store and keys are never mounted into MCP workloads. The
controller uses the bridge binary from the deployed Hub image.

`discover` accepts `query` and returns at most five candidates: prepared matches,
matching artifact alternatives, and source-only name matches from the enabled
Official MCP Registry, ToolHive, Docker MCP Catalog or Smithery. An explicitly
enabled `github-search` adapter fills remaining slots after registry matches.
Source-only matches require the
same repository review before installation; registry names and commands grant no
installation authority. This read-only operation never creates credentials,
bindings or workloads. A failed adapter adds a bounded warning while other
sources remain available. Full unprepared registry installation remains open in
#110; a live unprepared GitHub-search result has completed the generic install
lifecycle, while an Official MCP Registry/marketplace result has not. Pass the
selected `candidate_id` and a stable
`request_key` to `prepare_source`. Candidates expire and belong to the current
principal, context, runtime and policy. A direct GitHub `source` works with all
registries disabled; for a repository with a prepared entry a bare URL selects
the reviewed pinned commit, while an explicit `/commit/<sha>` URL keeps the
generic path. Both paths re-resolve and review the source; an overlay applies
only to its reviewed commit.

`prepare_source` waits a bounded window for review+build (about 90 seconds) and
then returns the durable `preparing` record instead of holding the HTTP call:
the work continues on a detached context and the outcome — `awaiting-*` or
`failed` — is read back with `status`. A lost connection no longer loses the
result. Open owner sessions receive a best-effort MCP log when the background
prepare finishes, and communication-hub also delivers a channel notice plus a
continuation job. The log is not the only wake: a client that never set a log
level still gets the outcome in the chat.

After the generated restricted build and real MCP preflight, use
`required_credentials` if requested. Enter credentials only in the protected
Broker form. Compatible owner connections are reused. Call `confirm` with the
returned nonce, then `enable`. For prepared entries that name a reviewed
zero-argument `probe_tool`, confirmation invokes it through the Broker-backed
workload. A failed provider read leaves the binding unprojected; retry uses the
same owner connection. File-credential readiness starts and then stops the
workload before Broker checkpoints writable state. Other MCPs without a reviewed
provider probe have MCP admission evidence only; their provider access must be
verified separately. Projection changes notify the owner's open MCP session;
a pinned-Hermes fixture confirms PID/session/active-run continuity. Full #74
acceptance remains open. Installing a
connector does not authorize provider writes.

When a reviewed prepared entry declares `oauth`, a failed first provider read
can return `awaiting_oauth` with `authorization_url`. The control tool also
requests URL-mode elicitation; clients without it receive the URL in the tool
response. The user opens the provider URL in a browser on the Docker host.
ToolHub handles the one-time loopback callback, writes the upstream token
format into that owner's Broker state lease, then repeats confirm and enable.
`status` reissues an expired or process-lost link. An existing token file is
never overwritten by this handoff; diagnose a failed read instead. OAuth
metadata must be reviewed for an exact source revision, and token adapters
remain source-specific. Tool text and arbitrary MCP URLs cannot start a flow.
Admitted tool schemas and rich MCP content, including embedded file resources,
are preserved with output bounds. Schema-declared provider resource owners are
ordinary arguments; runtime identity and credential selectors remain reserved.

For replacement, explicitly call `rotate` with the existing `onboarding_id`,
complete the Broker form, confirm and enable. The old owner workload stops
before new credentials are admitted. Ambiguous active connections fail closed.
`status` resumes onboarding; retry transient failures with the same request key
(`operation_id` resolves an onboarding by that key). Lifecycle operations also
accept a conversational `selector` — the definition id, the exact source
repository URL or its basename — which fails closed with a candidate list when
ambiguous instead of guessing.
Upgrade by reviewing the new SHA and catalog data, retaining the old artifact
and evidence. `disable` removes projection, `revoke` denies further access and
releases the workload, and `remove` is destructive two-phase: a bare call
returns a preview with `requires_confirmation` and a `remove_nonce`; only the
nonce-carrying repeat deletes the credential ciphertext, unreferenced
connection and binding rows and the workspace, leaving a scrubbed onboarding
tombstone for idempotent replay. Shared immutable artifacts must not be
deleted while another owner uses them.


## Recipe digests and source ledgers (M13)

Resolution is ledger-driven: every consulted source — prepared catalog, MCP
Registry, ToolHive, Docker MCP Catalog, `server.json`/MCPB manifests and
repo hints — records ranked claims (launch ladder L0–L6/hint, credential
ladder R0–R7) with its response digest and fetch time in the resolution
ledger. Catalogs are queried before repository files, so a repo hint can
never outrank a registry answer; mismatched candidates stay in the ledger
flagged `ignored`. Two disagreeing same-rank claims fail closed
(`ambiguous_entrypoint` / `ambiguous_credential_contract`) instead of
first-wins, and `credential_source` now records an owner-words (R7) ledger
entry naming the confirmed source rather than silently picking a file.

The resolved recipe is content-addressed: identical claim sets always pin
the same sha256 digest, and the store keeps one shared copy under
`recipes/<sha256>` — onboardings carry `recipe_digest` only. Trust derives
from the weakest winning rank: T0 prepared-catalog-only, T1 registry-grade
launch plus authoritative credentials, T2 anything heuristic. A golden
corpus of recorded real targets (`internal/toolhub/api/testdata/corpus/`)
pins every recipe digest; `GOLDEN_UPDATE=1` re-pins after an intentional
model change.

## Human action surface (M13)

Every moment only a human can finish — entering a credential, confirming an
install, approving a removal, completing OAuth — is a one-time link carrying
`?t=<token>` on a human-action page: `/credentials`, `/confirm`, `/remove`,
`/connect`, `/device`. The token resolves the stored nonce of that
onboarding and that operation only; spent or expired links answer `410
Gone`, a POST without the token is rejected, and each page re-checks the
onboarding's principal, context and runtime. No link token, nonce, OAuth
state or device code ever appears in an agent-facing control result.

The link is delivered on the channel the install was requested from: the
prepare-outcome notice routes by the initiating message job's channel.
Channels declaring `Caps().Actions` may render the link as an actionable
affordance; every other channel receives the same plain text — the link
itself is the affordance. `hubctl connector link --onboarding <id>` prints
the URL for scripts, and `--confirm` finishes an awaiting-confirmation
onboarding without a browser.

When `HUB_PUBLIC_ORIGIN` is set (https only) the minted links use that
origin, and `HUB_PUBLIC_LISTEN` binds a second listener that serves only
the human-action paths — control and gateway routes answer 404 there. The
connect page offers two dead-redirect alternatives: an RFC 8628 device flow
(`/device/<id>` shows the user code and verification URI while the hub
polls) and a paste-the-redirect form that validates the pasted `state` and
redirect origin before completing through the shared PKCE callback.

The credential form renders from the recorded recipe and ledger: source and
pinned revision, trust tier, egress hosts, and per-field provenance with
its rank and HTTPS `token_url` help link. Heuristic-only (R6) fields are
shown as suggestions to verify, never required. Submitted values are probed
with a read-only `tools/list` before anything persists — a failed probe
leaves no ciphertext, connection or Broker credential and re-renders the
form with the entered values page-side.

## Catalog tiers (M13 P5)

Entries declare a `tier`: `t0` for the reviewed prepared catalog, `t1` for
operator-confirmed imports, `t2` for the arbitrary-source path. A T0 entry
carries `image` — `{ref, digest, provenance, sbom}` — the CI-built OCI pin.
`ref` is the `<repo>@sha256:…` form the registry returns; the definition
splits it into the bare repository image and the admitted digest, and
runtime admission verifies the pin instead of rebuilding — a pinned entry
never reaches the restricted builder or a repository fetch
([ADR-0057](adr/ADR-0057-ci-built-connector-images.md)). Entries without an
image pin keep the existing review-and-build path.

`hubctl connector import <ref>` turns a T1 ref into a recipe draft:
`mcp-registry:<name>@<version>`, `docker:mcp/<name>@<digest>`,
`toolhive:<name>`, `mcpb:<owner>/<repo>@<tag>`, `hermes:<name>@<commit>`
(the upstream `optional-mcps` tree) and `aqua:<pkg>@<version>`. Parsing is
pure — the ledger records only what the ref asserted, so identical refs give
identical review digests. Without `--confirm` the command prints the digest
and writes nothing; `--confirm` stores the content-addressed recipe.

Catalog entries may carry `toolsets`, `default_enabled` and
`default_excluded`: install projects the default set only, and
`connector_enable <target> toolsets=[<set>]` widens a binding to the named
sets — a `default_excluded` tool appears only through an explicitly enabled
toolset. `PrefetchPlan` computes which pinned images to warm inside the 8
GiB cache cap: it evicts only hub-labelled (`hermes-hub.*`) images, never a
foreign one, and skips a target rather than breach the budget.

## Bundle IDs and one-operation start

Call ToolHub `prepare_source` once with the repository URL shown in a ready
entry's handoff, for example `{"source":"https://github.com/letya999/telegram-mcp","request_key":"telegram-personal-1"}`.
This starts the same exact-source review, restricted build, Broker, preflight,
confirmation and owner binding as a direct GitHub install. Continue with
`status`, `required_credentials`, the protected Broker form, `confirm` and
`enable`; `prepare_source` alone does not connect an account. `discover` accepts
each of the twelve bundle IDs and returns its pin or a `blocked` reason.
`sql` discovers the reviewed `dbhub` entry; `txttsql` is separate. Notion and
Google Calendar remain additional prepared entries. An entry with status
`blocked` has no selectable `candidate_id`.

## Personal Telegram

Source: [letya999/telegram-mcp](https://github.com/letya999/telegram-mcp),
commit `26f1632b2b07cca16fa8f172fe645477921db9ed` (Apache-2.0).
Generated Python artifact installs the source-declared `proxy` extra and runs
`/opt/venv/bin/telegram-mcp` over stdio. This is a personal MTProto session,
separate from Communication Hub's Bot API token and scope. The reviewed Broker
contract `telegram-session` accepts API ID, API hash, StringSession and expected
numeric user ID in the protected form. The server verifies the account ID
before it serves MCP. It connects to Telegram before `tools/list`, so the first
prepare returns a protected credential request and the real tool list is
admitted only after the owner submits a working session. `list_accounts` is
the read probe; verify one bounded dialog/history read after enablement.
ToolHub obtains a short-lived Broker lease for this admission probe, releases
it and revokes its temporary grant before confirmation. Confirmation creates
the separate owner workload grant. If admission fails, `status` records a
generic failure without upstream error text; `rotate` opens a new Broker
request. The ToolHub localhost credential form is not used for this contracted
entry.

The source's `TELEGRAM_EXPOSED_TOOLS=read-only` is fixed for this prepared
entry, with transcription off. The generic workload is per owner and the
controller supplies the private egress proxy host and port to Telethon. The
session string is encrypted by Broker at rest; temporary caches and locks stay
inside that owner's workload. The upstream read-only mode does not filter bot
peers; use the existing account adapter when that narrower dialog policy is
required. Do not run the same StringSession simultaneously
through JobFetch or another IP: create a distinct Telegram device session for
the Hub. The `job_ftch` pattern uses a local Telethon `.session` file plus API
ID/hash; use it only as a local format reference. For a fresh Hub device, run
the pinned fork's `session_string_generator.py` locally. Put API ID/hash from
`my.telegram.org/apps` in its ignored `.env`, set
`TELEGRAM_DEVICE_MODEL=hermes-hub ToolHub`, then run
`uv run session_string_generator.py --qr` (or `--phone`) in that directory.
Scan the QR in Telegram Devices or enter the phone code and two-factor password
in that local terminal. Decline its optional `.env` write if you do not need a
local copy. Enter the resulting StringSession and numeric `get_me().id` only
in the Broker form. For the same account, the local `job_ftch` login helper's
`Authenticated as: ... id=...` output is another way to obtain that ID. The
fork's generator creates a new session; it does not convert an existing
`.session` file. On stacks with `ingress: [telegram]` the standard
Communication Hub already offers a local QR page when ToolHub sends a
credential request whose reviewed contract declares `intake: telegram_qr`
(the `telegram-session` contract does); set `telegram_auth: false` to disable
it. The QR flow
uses Go MTProto in the same gateway process, shows the QR only at the
loopback-bound page, handles a two-step password there and creates a new
Telethon-compatible StringSession in memory. After the owner confirms the
numeric account ID, the gateway signs a Telegram-only submission to Credential
Broker. The Broker enforces the exact owner, reviewed `telegram-session`
contract, pending request and expiry. The session never enters bot messages,
job state, logs or a local file. A new QR can be requested if the process or
request expires. The existing protected Broker form remains the fallback.
The QR invitation is sent to the Telegram chat. If the Hub has no configured
application API pair, enter API ID/hash on that page. Scan the QR in Telegram
Devices. Once the page says
the session was transferred, ask the bot to continue the Telegram connection.
For a simpler local setup, put `HUB_TELEGRAM_AUTH_API_ID` and
`HUB_TELEGRAM_AUTH_API_HASH` in the ignored
`spaces/<user>/telegram-auth.<dev|prod>.env` file. `hubctl up` preserves this
file, whereas it regenerates `communication.<dev|prod>.env`.
The QR starts after the owner clicks the invitation, without asking for these
values in the browser. The Telegram account is selected by the account that
scans the QR; the API pair identifies the client application, not that account.
While the QR invitation is active, the gateway does not send a second Broker
form through an automatic continuation.
Never copy a `.session` file, OTP or API hash into the repository or a channel.

For reconnect, call `status`, check the expected user ID and restart the owner
workload. For rotation or re-authentication, create a new Telegram device
session locally, call ToolHub `rotate` for the onboarding ID and submit it in
the Broker form. `disable` unprojects tools, `revoke` blocks calls and releases
the workload, and `remove` cleans the installation. In Telegram Devices,
terminate the old device session after local revoke; Broker revoke alone
cannot invalidate MTProto authorization at Telegram. Upgrade by changing the
reviewed commit explicitly, retaining the old artifact/review digest for
rollback, then repeating admission and read verification. The prepared entry
defaults to reads. On an explicit user request to enable sending, call
`prepare_source` with `{"onboarding_id":"<enabled Telegram onboarding>","telegram_send":true}`.
This reviews the same pinned artifact with `TELEGRAM_EXPOSED_TOOLS=read-only+send_message`
using a temporary owner-bound Broker lease. It retains the existing session;
no QR login, session export or credential submission is needed. Only
`send_message` may be added; read tools, image, entrypoint and credential
contract must remain identical. Confirm the returned nonce and call `enable`
for the new onboarding ID. The existing connection receives a new binding
grant and the old binding loses authorization. A failed probe leaves the
working read connection intact. Concurrent retries reuse one upgrade.
Use the new onboarding ID for subsequent lifecycle operations.

Test an explicitly authorized send to Saved Messages (`chat_id: "me"`) with a
unique `operation_id` and verify its provider message-ID receipt. Retrying
the same operation ID uses Telegram's deduplication. Other recipients need
the user's actual sending instruction. Reply and delete remain unavailable
through this prepared mode; the separate Telegram account write definition
describes those broader effects.

Handoff: “Install the pinned `letya999/telegram-mcp` personal connector with
`prepare_source`. I will enter API ID, API hash, a separate StringSession and
expected user ID only in the protected form. Verify `list_accounts` and a
bounded history read; leave sends and deletes disabled.”

The read set also carries four event tools for hub event subscriptions:
`wait_for_new_message` and `wait_for_settled_message` return a bounded batch
of new events for one poll, `requeue_event` puts one dropped event back by
its id only, and `incoming_feed_status` reports feed health. An event
subscription (`watch_subscribe`) polls one of these projected read tools at
its own schedule cadence through ToolHub's read-only `/v1/tool-call`; the
hub deduplicates event ids in its durable seen-set, fans out deliveries or
one bundled agent job, and marks the subscription degraded on a connector
auth failure. With `delivery=stream` the hub keeps one bounded wait call
outstanding per live subscription, so `wait_for_settled_message`-class
tools deliver settled bursts in seconds without a persistent workload;
otherwise the source is polled at the subscription's cadence. There is
still no always-on push ingress, and `requeue_event` receives only the
event id or chat id, never owner content.

## Blocked bundle entries

`google-workspace` is blocked as a *single generic prepared artifact*: the
Google Calendar entry only covers Calendar, while the existing official remote
Workspace services use a separate opt-in ToolHub path. A reviewed exact source,
OAuth/Broker contract and product tool schemas are needed before promotion.
`slack` is blocked pending a reviewed `korotovsky/slack-mcp-server` generic
recipe, user-token Broker overlay and provider preflight; the current Slack
data OAuth path remains opt-in. `careergo` is blocked pending an exact MCP
source, license, tool contract and JobFetch import API. No credentials or
approval may be inferred from these catalog placeholders.
`agent-browser` is blocked pending a reviewed runnable connector image: no
published image exists for the `vercel-labs/agent-browser` source, so the
entry stays discoverable with its reason, runbook and handoff — it is never
installable. The installable browser connector is `playwright-mcp` below.

## Playwright MCP (browser)

Source: [microsoft/playwright-mcp](https://github.com/microsoft/playwright-mcp),
commit `ab6d67cbf8b619338f096980806fdbe29432cc4d` (Apache-2.0). T0 zero-build
entry running the digest-pinned MCR image
`mcr.microsoft.com/playwright/mcp@sha256:690763cbb437ee7233f538a953bb5390bf03791eb5c965b471e59d17a6132fcb`
with `--caps vision,pdf` and a bounded `--idle-timeout`. A browser is an
ordinary connector: `install` / `enable` / `disable` / `reauth` / `remove`,
the operation journal, ownership labels, finalizers and verified-zero cleanup
behave exactly as on any other connection, and the pre-existing hub-owned
`browser` / `browser_guest` runtime is untouched.

Each connection gets an isolated workload — a per-connection Chromium with
its own `connection-state` browser profile (plus a `downloads` subdirectory),
so removing the connector removes the profile through the usual finalizer.
Web egress is `egress_class: web`: arbitrary outbound HTTPS through the
audited per-workload proxy, deny-first on private, loopback, link-local,
CGNAT, metadata and hub-internal destinations, with per-request DNS checks;
connection metadata may carry bounded `egress_allow` / `egress_deny`
refinements where deny always wins.

Default projection is read-only (`browser_navigate`, `browser_snapshot`,
tabs, wait, …). Interaction tools are the `act` toolset and additionally
need the human-confirmed `connector_allow_writes` step; `eval`
(`browser_evaluate`) and `network` (`browser_network_requests`, …) are their
own toolsets and stay hidden until enabled — on both the list and the
dispatch path. An optional `browser-session` Broker contract imports a
Playwright storage-state file (and origin allow/block lists) through the
protected form; no token or link material reaches the model.

`connector_status` on a web connection mints a one-time, short-TTL,
owner-bound viewer link delivered on the owner's channel — `GET
/viewer/<connection>?t=<nonce>` serves the viewer exactly once (410 gone on
reuse or expiry, 403 on a wrong nonce); it is never an unauthenticated
published port. Chromium needs a larger `/dev/shm`, so the definition
carries a bounded `shm_size_mib` budget (1024 MiB here, capped globally and
never above workload memory): the Docker fallback passes `--shm-size` to the
workload container only, and the ToolHive path forwards the flag only after
its capability probe advertises it — otherwise it fails closed or falls
back.

## Notion

Source: [makenotion/notion-mcp-server](https://github.com/makenotion/notion-mcp-server).
Generated Node entrypoint: `bin/cli.mjs`. Broker: `notion-token`, delivering
`NOTION_TOKEN`. Share the intended root/pages with the integration first.
Verify identity, search shared content and retrieve an authorized page. An empty
search may mean no shared pages. Never create/edit pages as an access test.

Handoff: “Install https://github.com/makenotion/notion-mcp-server using my
existing Notion connection. Verify a read of the shared root/pages.”

## Google Calendar

Source: [nspady/google-calendar-mcp](https://github.com/nspady/google-calendar-mcp).
Generated Node entrypoint: `build/index.js`. This entry covers Calendar only;
full Google Workspace needs a separately selected repository. Broker:
`google-file-session`, supplying OAuth client JSON and owner/binding state.
The reviewed token path is inside the Broker state mount. File credentials are
read-only; mutable state is checkpointed only after the writer stops. Existing
tokens may be transferred locally with owner authorization, never through chat.
With a client JSON but no token, confirmation returns a Google authorization
link for the `calendar.readonly` scope. Google redirects to
`http://127.0.0.1:8090/oauth/callback`; the browser must run on the Docker host.
After consent, ToolHub stores the token under the upstream `normal` account
and checks the read probe. Verify `list-calendars` and bounded `list-events`.
An existing but expired or invalid token requires diagnosis or explicit
rotation; placeholders are only for MCP preflight. This path has mock OAuth
coverage but requires a real account consent for live integration evidence.

Handoff: “Install https://github.com/nspady/google-calendar-mcp; reuse my OAuth
client and token state through Broker. Verify my calendars and events.”

## GitHub

Source: [github/github-mcp-server](https://github.com/github/github-mcp-server).
Generated Go recipe selects the MCP main package and literal `stdio` argument.
Upstream Dockerfile directives never become build authority. Broker:
`github-pat`, delivering `GITHUB_PERSONAL_ACCESS_TOKEN`. OAuth/App alternatives
need a matching reviewed contract; source metadata alone does not implement an
OAuth flow. Verify `get_me` and a file read in an authorized repository.
Issue, PR, review, branch and content mutations are separate actions.

Handoff: “Install https://github.com/github/github-mcp-server with my existing
Broker token; verify identity and a safe repository read.”

## GitLab

Source: [zereight/gitlab-mcp](https://github.com/zereight/gitlab-mcp). Both bin
aliases resolve to `build/index.js`. Generated Node build ignores upstream cache
mounts as installation authority. Broker: `gitlab-pat`, delivering
`GITLAB_PERSONAL_ACCESS_TOKEN`, compatibility alias `GITLAB_TOKEN`, non-secret
`GITLAB_API_URL` and `GITLAB_ENDPOINT` — the real host:port of a plain-HTTP
self-hosted instance behind the managed loopback forward on `18929`. Upstream
env knobs the contract does not declare (for example `GITLAB_PROJECT_ID` from
`docker/.env.example`) stay optional extras, never required form inputs.
Confirm the intended HTTPS API URL in the protected form;
its host becomes an egress destination. Secrets, authenticated URLs and URLs
with queries cannot expand egress. Verify current-user/whoami after rotation.
On failure, check credentials and the confirmed host on the same connection;
do not relax egress or silently fall back to another GitLab host.

Handoff: “Install https://github.com/zereight/gitlab-mcp; reuse or rotate my
existing connection, confirm the API URL and verify whoami.”

## Atlassian

Source: [sooperset/mcp-atlassian](https://github.com/sooperset/mcp-atlassian).
Generated Python image entrypoint: `/opt/venv/bin/mcp-atlassian` (stdio). The
server registers **zero** tools until connection config exists, so review
collects the full six-field contract instead of trusting placeholder probing.
Broker: `atlassian-env`, delivering `JIRA_URL`, `JIRA_USERNAME`,
`JIRA_API_TOKEN`, `CONFLUENCE_URL`, `CONFLUENCE_USERNAME` and
`CONFLUENCE_API_TOKEN`. The products are alternatives: a complete Jira block
or a complete Confluence block satisfies the credential groups, so the form
marks every field optional while ToolHub rejects a submission with no full
block. `atlassian.net`/`atlassian.com` are pinned egress; each entered URL's
host joins egress automatically, so self-hosted Server/DC instances work
without relaxations. The provider probe follows the delivered block —
`jira_get_all_projects` or `confluence_list_page_templates` — because a bad
token still passes `tools/list`; on failure rotate the same connection,
never create a second one.

Handoff: “Install https://github.com/sooperset/mcp-atlassian; reuse or rotate
my existing connection, fill the Jira block, the Confluence block or both in
the protected form and verify the matching read probe.”

## TXT-to-SQL

Source: [letya999/txttsql-mcp](https://github.com/letya999/txttsql-mcp), the
reviewed primary SQL engine (AGPL-3.0-only). Generated Rust entrypoint
`/app/target/release/txttsql-mcp` plus the upstream `--config
/app/config.toml` tail; the repository is built into an immutable artifact
and never mounted at runtime. Upstream `Config::resolve` keeps the long-lived
server up without a file: an existing `config.toml` wins, otherwise
`TXTTSQL_SOURCES` — the single-line JSON form of `[[sources]]` — configures
databases, and a fully missing configuration degrades to a zero-source server
that still answers `initialize`/`tools/list` and returns typed tool errors
instead of exiting. Broker: `txttsql-sql`, delivering the sources JSON, three
optional `TXTTSQL_PASSWORD_{1,2,3}` env values referenced from inside it
(`{"kind":"env","name":"TXTTSQL_PASSWORD_1"}`), an optional full `config.toml`
mounted read-only at `/app/config.toml` (per-user memory, plugins, ontology,
metadata providers) and an optional CA bundle at `/run/txttsql/ca.pem`.

PostgreSQL, CockroachDB and ClickHouse drivers do not speak HTTP CONNECT, so
the entry declares `tcp_forwards`: companion loopback listeners `15432`,
`15433` and `15434` inside the workload network, each relaying accepted
connections through the workload's reviewed Squid CONNECT proxy to exactly
one credential-bound `host:port` held in `TXTTSQL_ENDPOINT{,_2,_3}`. The same
non-secret value joins the egress ACL at readiness, so a forward can only
reach what admission allowed — there is no direct TCP bypass. Each
`[[sources]]` entry in the JSON points at `127.0.0.1:<forward port>` while
the real endpoint lives in the protected form; host, database, role and
private destinations stay outside tool arguments. The catalog `egress`
sentinel is `127.0.0.1`: every real destination is credential-derived. When
TLS verifies a certificate hostname, the forward makes the server name an IP
literal — `verify-ca` (CA validation without hostname) and per-source
`ca_file` remain available, and `allow_insecure` stays an explicit operator
choice.

Read-only is enforced server-side by upstream read-only transactions plus
per-source `allowed_schemas`/`allowed_tables` — not SQL string matching —
together with `max_rows`, `timeout_seconds` and `max_connections` bounds.
There is no write profile: mutations stay a separately granted action.
Verify `list_sources` (the readiness probe), then `list_tables` and one
bounded `SELECT` through the forward. Memory and ontology stay
file-configured opt-ins on read-only mounts, not default state.

Handoff: “Install https://github.com/letya999/txttsql-mcp as the reviewed
read-only SQL engine. Fill the sources JSON and the matching endpoints in the
protected Broker form, verify list_sources and one safe SELECT, then do not
change provider data without an explicit instruction.”

## Grafana

Source: [grafana/mcp-grafana](https://github.com/grafana/mcp-grafana), pinned to
the reviewed commit (Apache-2.0). Generated Go entrypoint `/app/mcp-server`
on stdio; upstream's HTTP/SSE transports are never enabled — the review
keeps transport server auth and Grafana credentials disjoint. Broker:
`grafana-sat`, delivering `GRAFANA_URL`, `GRAFANA_SERVICE_ACCOUNT_TOKEN`,
optional `GRAFANA_ORG_ID` and non-secret `GRAFANA_ENDPOINT`. For `https://`
Grafana the URL host joins the egress ACL automatically (mcp-grafana honours
the standard `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` set). For a plain-HTTP
Grafana — like the local demo stack — the contract carries the real
`host:port` in `GRAFANA_ENDPOINT`, a companion `tcp_forwards` listener on
workload loopback `13000` relays it through Squid CONNECT, and the URL field
stays `http://127.0.0.1:13000`: the destination lives in the protected form,
never in tool arguments.

`GRAFANA_SOCKS5_PROXY` is claimed as a deployer-owned environment name on
purpose: source scanning detects the `*_PROXY` name and would inject the
HTTP Squid URL into a slot that only accepts `socks5://`/`socks5h://` and
fails closed otherwise. Upstream usage
statistics are disabled and the Loki cost guardrail is set to `enforce`
(`GRAFANA_USAGE_STATS`, `GRAFANA_LOKI_GUARDRAIL_MODE`). The reviewed
entrypoint argv extends the inferred binary with upstream's own gates —
`--enabled-tools=search,datasource,dashboard,folder,alerting,prometheus,loki`
keeps only the read categories and `--disable-write` drops the mutation
tools inside them, so the projected contract carries no create/update/delete
and no `grafana_api_request` escape hatch. Verify `list_datasources` (the
readiness probe), then a bounded `search_dashboards` or `query_prometheus`.
Rotate by issuing a new service-account token and calling `rotate` on the
same connection.

Handoff: "Install https://github.com/grafana/mcp-grafana with my existing
Grafana service account. In the protected Broker form give the Grafana base
URL and service account token, optionally the organization ID; for a
plain-HTTP Grafana also give the real host:port endpoint and set the URL to
the managed forward http://127.0.0.1:13000. Verify list_datasources and a
bounded read; dashboard or datasource changes stay a separate explicit
instruction."

## Prometheus

Source: [prometheus/prometheus-mcp](https://github.com/prometheus/prometheus-mcp),
pinned to the reviewed commit (Apache-2.0). The binary embeds a
`prometheus/docs` snapshot via `go:embed` that upstream's `make docs` vendors
before compiling; the entry reproduces it with a reviewed `context_sources`
overlay — `prometheus/docs` pinned to the Makefile's `DOCS_VERSION` commit,
merged under `cmd/prometheus-mcp/external/docs`. Generated Go entrypoint
`/app/mcp-server`; transport stays `stdio`. Broker: `prometheus-url`,
delivering `MCP_SERVER_PROMETHEUS_URL` (the binary's real env prefix is
`MCP_SERVER_`) and non-secret `PROMETHEUS_ENDPOINT` for the same
managed-forward pattern on loopback `19090` (`http://127.0.0.1:19090` in
the URL field when the target is plain HTTP). The entry registers only
the read toolset — `MCP_SERVER_MCP_TOOLS=list_targets` keeps the core
read tools (query, range/metadata/labels/series, docs, runbooks) plus the
target probe, so dangerous TSDB admin tools are never advertised; the
`MCP_SERVER_DANGEROUS_ENABLE_TSDB_ADMIN_TOOLS` escape hatch is never set.
Docs auto-update is off and result truncation is bounded
(`MCP_SERVER_PROMETHEUS_TRUNCATION_LIMIT=500`). Verify `list_targets`
(the readiness probe) and one bounded `query`. Remote-write and admin
operations are a separate grant.

Handoff: "Install https://github.com/prometheus/prometheus-mcp against my
Prometheus. In the protected Broker form give the base URL; for a
plain-HTTP Prometheus also give the real host:port endpoint and set the
URL to the managed forward http://127.0.0.1:19090. Verify list_targets
and one bounded query; TSDB admin tools stay disabled."

## DBHub

Source: [bytebase/dbhub](https://github.com/bytebase/dbhub), the reviewed
fallback SQL engine (MIT) — a separate prepared entry selected explicitly,
never a silent provider failover. Generated pnpm workspace build runs
`node /app/dist/index.js` on stdio; this entry never enables the HTTP
transport (unauthenticated by default upstream). DBHub exits without any
configuration, so the reviewed `preflight_environment` injects
`DSN=sqlite:///:memory:` for the unauthenticated `tools/list` probe only —
owner credentials always override it. This revision rejects the legacy
`READONLY` and `MAX_ROWS` environment variables: env-mode DBHub has no TOML
`[[tools]] readonly = true` policy, so read-only must be enforced by the
database role named in the DSN — an explicit requirement of this contract,
not a recommendation.

Broker: `dbhub-sql` — secret `DSN`, non-secret `DBHUB_ENDPOINT` and
`DBHUB_SSH_ENDPOINT` host:port values bound to the two `tcp_forwards`
(database on `15432`, SSH bastion on `15433`), optional `SSH_USER`,
`SSH_PASSWORD`, `SSH_PASSPHRASE`, an `SSH_KEY` private-key file at
`/run/dbhub/id_rsa`, optional `SSH_HOST`/`SSH_PORT` overrides and a CA bundle
at `/run/dbhub/ca.pem`. Direct path: the DSN targets `127.0.0.1:15432` and
the endpoint field carries `db.example.com:5432`. Managed-tunnel path:
`SSH_HOST=127.0.0.1`/`SSH_PORT=15433` make DBHub's ssh2 client dial the
forwarded bastion while the DSN addresses the database as the bastion sees
it. Verify `tools/list`, one bounded `SELECT 1` through the forward, and the
tunnel when `DBHUB_SSH_ENDPOINT` is configured.

Handoff: “Install https://github.com/bytebase/dbhub as the fallback
read-only SQL engine. Fill the DSN and endpoint in the protected Broker form
against a read-only database role, verify one safe SELECT, then do not change
provider data without an explicit instruction.”

## DataLens

Source: [datalens-tech/datalens-mcp](https://github.com/datalens-tech/datalens-mcp),
the vendor-org server the Yandex Cloud documentation links to (MIT), pinned to
the reviewed commit. Generated Node entrypoint `node /app/dist/index.js` from
`npm ci` + `npm run build` (tsc). The server is a five-tool gateway over the
public API: `list_commands` and `describe_commands` (discovery),
`invoke_read_command`, `invoke_write_command` and `invoke_privileged_command`.
Every API operation's `x-mcp-scope` from the live OpenAPI schema is enforced
server-side before the call runs — a write command named through
`invoke_read_command` is rejected by the server, not by the model. ToolHub
effects come from upstream annotations: the two discovery tools and
`invoke_read_command` are `read`, write and privileged invocation are `write`.
The live schema at `api.datalens.tech/json/` was verified fully classified
(51 read / 24 write / 41 privileged, none unclassified); a schema with no
classified commands fails startup, so the gateway can never expose an
unclassified operation.

Broker: `datalens-auth`, delivering `DATALENS_ORG_ID` (the `x-dl-org-id` tenant
pin — env, never a model argument), `DATALENS_API_AUTH_HEADER` (the complete
Authorization value) and optional `DATALENS_API_URL`. Cloud installs send
`Bearer <iam-token>`; IAM tokens expire in 12h, so rotation via the `rotate`
operation is a routine action, not an exception. Internal installations point
`DATALENS_API_URL` at their HTTPS endpoint — its host joins the egress ACL at
readiness — and authenticate with `OAuth <token>` in the same header field.
Folder and workbook boundaries stay on the provider side: the IAM/OAuth
account's permissions decide what reads return.

Fixed envs: `DATALENS_YC_STATIC_AUTH=1` (the image carries no `yc` CLI),
`DATALENS_MAX_RESPONSE_CHARS=100000` (bounded responses; hard upstream caps
stay 10 MiB API / 20 MiB schema) and `NODE_USE_ENV_PROXY=1` so Node's `fetch`
honors the injected `HTTP(S)_PROXY` and every call crosses the workload's Squid
ACL (`api.datalens.tech` plus `registry.npmjs.org` for the bounded,
credential-free upgrade notice). The server fetches the OpenAPI schema before
it speaks MCP, so this entry declares `preflight_network`: the unauthenticated
tools/list probe runs with egress and placeholder credentials. Verify
`list_commands` (the readiness probe — it also proves the schema fetch), then
one real read such as `invoke_read_command` with `command_name`
`getWorkbooksList`. Workbook, dashboard, dataset and permission changes are
write/privileged effects — a separate explicit instruction.

Handoff: “Install https://github.com/datalens-tech/datalens-mcp; reuse or
rotate my existing Broker connection. In the protected Broker form give the
organization ID and the Authorization header value — Bearer <iam-token> for
cloud (IAM tokens expire in 12h) or OAuth <token> with an API URL override for
an internal installation. Verify list_commands and one read through
invoke_read_command, for example getWorkbooksList.”
